# Post-Auth Mount-Session Implementation Reflection

**Date:** 2026-05-09  
**Reviewer:** Claude (peer review pass — Phase 5, §9)  
**Contract:** `docs/post-auth-mount-session-contract.md`  
**Worktrees read:**
- relayfile: `.workflow-worktrees/post-auth-mount-session/relayfile`
- cloud: `.workflow-worktrees/post-auth-mount-session/cloud`
**E2E evidence:** `docs/evidence/post-auth-mount-session-e2e.log` → `POST_AUTH_MOUNT_E2E_OK`

---

## 1. Alternatives chosen from cloud#498

The original PR `feat/relayfile-mount-workspace-spec` (cloud#498) proposed:
- `mode: "poll" | "stream"` on the wire
- A single `mountWorkspace(...)` surface without explicit provider verification semantics
- `expiresAt` present in the Cloud response but not promoted to the SDK handle

The implementation made the following binding choices against the contract:

| Decision | cloud#498 proposal | Contract (adopted) | Implemented? |
|---|---|---|---|
| Mode enum | `"poll" \| "stream"` | `"poll" \| "fuse"` | ✓ Both layers |
| `stream` rejection | implicit | `400 invalid_mode` / `InvalidMountModeError` before HTTP | ✓ |
| `expiresAt` on handle | Cloud-only | `MountedWorkspaceHandle.expiresAt` + `suggestedRefreshAt` | ✓ |
| Provider verification | unspecified | Explicit `ProviderNotConnectedError` / `ProviderNotReadyError` gate | ✓ |
| Token minting | undetermined | Delegates to existing `createWorkspaceJoinAccess` path | ✓ |

The decision to reuse `createWorkspaceJoinAccess` (rather than a new mint path) is correct and safe: it inherits the scopes subset-check, the `expiresAt` / `suggestedRefreshAt` logic from `relay-workspaces.ts:586`, and keeps the token-issuance path single. The SDK correctly hits the cloud route via `WorkspaceHandle.requestJson` so retries, token refresh, and timeouts are inherited.

---

## 2. Poll / fuse / stream semantics — are they clear?

**Yes, they are clear and correctly implemented.**

### SDK layer (`setup.ts`)
`normalizeMountModeInput` (setup.ts:1178) throws `InvalidMountModeError` **before** any HTTP call for any mode outside `{"poll", "fuse"}`. The test "mountWorkspace rejects unsupported stream mode before any HTTP request" (setup.test.ts:935) confirms no fetch is issued.

### Cloud layer (`relayfile-mount-session.ts`)
`validateMountSessionBody` (relayfile-mount-session.ts:278) rejects any mode outside `{"poll", "fuse"}` with `400 invalid_mode`. The test confirms `mode: "stream"` returns `{ error: "invalid_mode" }`.

### Fuse unavailability
`RelayfileMountProcessInstance.isFuseUnavailable()` (mount-launcher.ts:266) scans `outputBuffer` for the exact string `"fuse mode is not available in this build"` when mode is `"fuse"`, and `waitForReady` translates it to `MountModeUnavailableError` (never silently falls back to `poll`). Contract §7 requirement satisfied.

### Websocket invalidation channel
The contract (§2) correctly notes that the existing `websocketReconcileEvery` is an optimization on top of `poll`, not a user-visible mode. The implementation does not expose it as a distinct mode value. Clean.

**Verdict: poll/fuse/stream semantics are unambiguous in both layers.**

---

## 3. Provider verification and token expiry supervision — are they coherent?

### Token expiry: coherent with one clarification

`expiresAt: access.tokenExpiresAt` and `suggestedRefreshAt: access.suggestedRefreshAt` flow from `createWorkspaceJoinAccess` → cloud response → SDK `validateMountSessionResponse` → `MountSessionResult` → `MountedWorkspaceHandleImpl` properties. The test at setup.test.ts:1085 confirms `status()` returns `expiresAt: "2026-05-09T11:00:00.000Z"` from state.json AND that `handle.expiresAt` carries the value from the mount-session response as a stable property.

**Minor issue:** `RelayfileMountProcessInstance.status()` (mount-launcher.ts:199) passes `expiresAt: null, suggestedRefreshAt: null` when computing status internally. This is intentional: the launcher instance doesn't hold the session metadata. `MountedWorkspaceHandleImpl.status()` (setup.ts:907) correctly passes `this.expiresAt` and `this.suggestedRefreshAt` to `readMountedWorkspaceStatus` directly — so consumers of `handle.status()` always receive the correct values. The `null` in the launcher's own `status()` method is only visible if a caller holds a `MountLauncherInstance` directly, which is not the intended public surface.

### Provider verification: coherent but with a latent coupling

`verifyWorkspaceProviderReady` (setup.ts:1477) calls `workspace.getConnectionStatus` and routes on `status.state`:
- `status.ready === true` → proceed
- `status.state === "not_connected"` → throw `ProviderNotConnectedError`
- otherwise (or timeout exhausted) → throw `ProviderNotReadyError`

The contract §6 says the SDK should call `isConnected(provider, ...)` as the not-connected detection step. The implementation instead reads `state === "not_connected"` from the status response. This is a **latent coupling to a specific string value** from the integration API. If the API returns `state: "disconnected"`, `state: "error"`, or `state: "not_found"` for an un-connected provider, `ProviderNotReadyError` is thrown instead of `ProviderNotConnectedError`. This breaks the consumer's ability to distinguish "go connect the provider" from "wait for sync to finish."

The test at setup.test.ts:983 passes `state: "not_connected"` in the mock, so the test only proves the happy path of the state-string check.

**Error gate ordering:** Provider verification happens **before** the mount-session POST (ensureMountedWorkspace calls `resolveWorkspaceForMount` then `verifyWorkspaceProviderReady` before calling `mountWorkspace`). Contract §6 requirement "error is thrown before the mount-session POST runs; no token is minted" — satisfied. The test at setup.test.ts:983 confirms `fetchMock` is called exactly twice (join + status check), not three times.

---

## 4. Divergences between cloud and relayfile that must be fixed before PR

### D1: `relayfileBaseUrl` trailing slash — cloud does not strip it (SHOULD fix)

**Contract §3.5:** "`relayfileBaseUrl` is the **stripped-trailing-slash** form so SDK callers can concatenate paths safely."

**Cloud route (route.ts:98):**
```ts
relayfileBaseUrl: access.relayfileUrl,
```
No strip applied. If `createWorkspaceJoinAccess` returns a URL with a trailing slash, the cloud response would violate the contract. The SDK's `validateMountSessionResponse` (setup.ts:1032) does call `stripTrailingSlash(...)`, so the SDK consumer is protected. Non-SDK callers are not.

**Fix:** In `route.ts`, change to:
```ts
relayfileBaseUrl: access.relayfileUrl.replace(/\/+$/, ""),
```

### D2: Missing cloud route tests — several §8.1 scenarios are absent (MUST fix)

The following test cases from §8.1 are **absent** from `tests/relayfile-mount-session-route.test.ts`:

| §8.1 scenario | present? |
|---|---|
| 200 minimal valid → full shape incl. `expiresAt`, `suggestedRefreshAt`, `relayfileBaseUrl` (no trailing slash), default `mode === "poll"`, default `remotePath === "/"` | ✓ |
| 200 scoped `scopes: ["fs:read"]` → response carries narrowed scopes | ✗ |
| 200 anonymous workspace + valid Relayfile JWT → mints a session | ✗ |
| 401 anonymous workspace + no auth header | ✗ (§3.1 requires 401 for nil-owner + no auth, distinct from the covered generic 401) |
| 401 invalid/expired Relayfile JWT | ✗ |
| 403 valid JWT but wrong workspace | ✓ |
| 404 unknown workspace id | ✗ |
| 400 `mode === "stream"` → `invalid_mode` | ✓ |
| 400 `localDir` is `..`, `/`, contains NUL, `..` segment, or > 1024 chars | partial (only "missing" tested) |
| 400 malformed `remotePath` → `invalid_remote_path` | ✗ |
| 400 `scopes` exceeds caller grant → `invalid_scopes` | ✓ |
| `relaycastBaseUrl` absent when `resolveConfiguredRelaycastUrl()` returns empty | ✗ |

### D3: SDK missing test — `MountSessionInputError` when no `provider` and `verifyProvider` defaults to `true` (MUST fix)

**Contract §6:** "the SDK MUST NOT silently downgrade `verifyProvider: true` to `false` when no `provider` is supplied — it throws `MountSessionInputError('provider required when verifyProvider=true')`"

**Implementation (setup.ts:319):** Correctly throws. But there is **no test** covering this path in `setup.test.ts`. The three `ensureMountedWorkspace` tests cover: not-connected provider, not-ready provider, and `verifyProvider: false`. None calls `ensureMountedWorkspace({ workspaceId, localDir, launcher })` without `provider` while `verifyProvider` defaults to `true`.

### D4: Provider not-connected detection is coupled to `state: "not_connected"` string (SHOULD document or fix)

As described in §3 above — the implementation uses a direct string comparison rather than the `isConnected()` indirection the contract specifies. This creates a silent error-routing failure if the integration API changes the state string for un-connected providers. The fix is either:

**Option A (minimal):** Add a comment in `verifyWorkspaceProviderReady` documenting the coupling:
```ts
// The integration status API uses state="not_connected" to signal
// a provider that has never been connected. Any other non-ready state
// (e.g. "error", "syncing") maps to ProviderNotReadyError per §6.
```

**Option B (correct):** Mirror the `isConnected()` check first:
```ts
const connected = await workspace.isConnected(provider, connectionId)
if (!connected && status.state === "not_connected") { ... }
```
But `isConnected()` itself calls `getConnectionStatus`, so this doubles the round-trip. Option A is preferred for v1.

---

## 5. Exact recommended fixes

### Fix 1 — cloud route: strip trailing slash on `relayfileBaseUrl` (SHOULD, low risk)

**File:** `packages/web/app/api/v1/workspaces/[workspaceId]/relayfile/mount-session/route.ts:98`

```diff
-      relayfileBaseUrl: access.relayfileUrl,
+      relayfileBaseUrl: access.relayfileUrl.replace(/\/+$/, ""),
```

### Fix 2 — cloud tests: add missing §8.1 coverage (MUST)

Add to `tests/relayfile-mount-session-route.test.ts`:

1. **200 scoped** — call with `body: { localDir: "...", scopes: ["relayfile:fs:read:/src/*"] }`, assert response `scopes` carries the requested value.
2. **200 anonymous workspace + valid Relayfile JWT** — set `workspace.createdBy = NIL_UUID`, mint a valid JWT for that workspace, assert 200.
3. **401 invalid Relayfile JWT** — `relayfileVerifyMock.mockResolvedValue(null)`, assert 401.
4. **404 unknown workspace** — `registryGetMock.mockResolvedValue(null)`, assert 404 `workspace_not_found`.
5. **400 localDir exhaustive** — `body: { localDir: "/" }`, `body: { localDir: "/proc/maps" }`, `body: { localDir: "a".repeat(1025) }`, `body: { localDir: "../etc" }` — each asserting `invalid_local_dir`.
6. **400 malformed remotePath** — `body: { localDir: "/tmp/x", remotePath: "../secret" }` — asserting `invalid_remote_path`.
7. **`relaycastBaseUrl` absent** — `vi.stubEnv("RELAYCAST_URL", "")`, assert response has no `relaycastBaseUrl` key.

### Fix 3 — SDK tests: add `MountSessionInputError` test for missing `provider` (MUST)

Add to `packages/sdk/typescript/src/setup.test.ts`:

```ts
it("ensureMountedWorkspace throws MountSessionInputError when verifyProvider=true and no provider is given", async () => {
  queueFetch(makeJoinResponse())
  const setup = new RelayfileSetup({ retry: { maxRetries: 0, baseDelayMs: 1 } })
  await expect(
    setup.ensureMountedWorkspace({
      workspaceId: "ws_123",
      localDir: "/tmp/test",
      launcher: createLauncherStub().launcher
    })
  ).rejects.toMatchObject({
    code: "mount_session_input_error"
  })
})
```

### Fix 4 — SDK: document `state: "not_connected"` coupling (SHOULD)

Add a comment above the state-string check in `verifyWorkspaceProviderReady` (setup.ts:1503):

```ts
// Integration status API signals an un-connected provider with
// state="not_connected". Any other non-ready state (e.g. "syncing",
// "error") maps to ProviderNotReadyError per contract §6.
if (status.state === "not_connected") {
```

---

## Summary table

| Area | Status | Action |
|---|---|---|
| Mode names (`poll`/`fuse`, never `stream`) | ✓ Clear and correct | None |
| `expiresAt` / `suggestedRefreshAt` on handle | ✓ Correctly propagated | None |
| `stop()` idempotency + no-delete guarantee | ✓ | None |
| Auth logic (401/403/404) | ✓ Reasonable reconciliation of §3.1 vs §3.7 | None |
| `relayfileBaseUrl` trailing slash — cloud | Gap: not stripped | Fix 1 |
| Cloud route test coverage | Missing 7 §8.1 cases | Fix 2 |
| SDK: no test for `MountSessionInputError` on missing provider | Missing 1 §8.2 case | Fix 3 |
| Provider not-connected detection (`state === "not_connected"`) | Latent coupling | Fix 4 (comment) |
| E2E evidence | `POST_AUTH_MOUNT_E2E_OK` | None |

**Fixes 2 and 3 are blocking (MUST). Fixes 1 and 4 are non-blocking (SHOULD) but recommended before PR merge.**

## Relayfile worker handoff

- Fixes 1 and 2 are cloud-only. They require changes in the cloud repo and are deferred to `cloud-impl` in later gates; this reflection doc is the handoff artifact for that work.
- Fix 3 is relayfile-local and is handled in this worktree by adding the missing SDK guardrail coverage.
- Fix 4 remains relayfile-local and is handled here as inline documentation of the current `state === "not_connected"` contract coupling.

---

POST_AUTH_MOUNT_REFLECTION_COMPLETE
