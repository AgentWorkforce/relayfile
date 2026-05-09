# Post-Auth Mount-Session Contract (cloud#498)

**Status:** v1 acceptance contract (locked for implementation)
**Date:** 2026-05-09
**Tracks:** AgentWorkforce/cloud#498 (`docs(relayfile): propose post-auth workspace mount DX`)
**Owners:** Relayfile SDK (`@relayfile/sdk`), Relayfile Cloud (`agentrelay.com/cloud`), Relayfile mount (`cmd/relayfile-mount`, `internal/mountsync`).
**Audience:** Implementers of the new mount-session route, SDK `mountWorkspace` / `ensureMountedWorkspace` surface, and the launch helper that supervises the local mirror.

This document is normative. **MUST**, **SHOULD**, and **MAY** carry RFC 2119
weight. It is the executable acceptance contract for cloud#498 and the
relayfile-side companion PRs.

---

## 1. Review summary — cloud#498

PR `feat/relayfile-mount-workspace-spec` proposes a single post-auth
`mountWorkspace(...)` API for sandbox integrators (Daytona, E2B, etc.) so
that an already-authorized workspace can be attached to a local directory
without re-running login or assembling low-level mount pieces by hand.

The spec is design-only (one new doc, `docs/architecture/relayfile-post-auth-mount-dx.md`,
+319 lines). All CI checks pass. Reviews:

- **CodeRabbit** — one actionable + two nitpicks (CHILL profile).
- **Devin Review** — clean.

### 1.1 CodeRabbit findings — accepted into v1

Both nitpicks are accepted as **MUST** for v1; the contract below absorbs
them rather than leaving them dangling on the doc.

1. **Expose `expiresAt` on the handle/status (CodeRabbit, lines 116–131).**
   The Cloud response carries an `expiresAt` (line 167 of the proposal).
   `MountedWorkspaceHandle.status()` and the handle itself **MUST** surface
   it so a sandbox supervisor can pre-empt expiry rather than catch a
   mid-flight 401. The contract additionally promotes `suggestedRefreshAt`
   from the Cloud join response (`packages/core/src/workspace/registry.ts:443`)
   to the handle, so supervisors do not have to compute their own refresh
   horizon.

2. **Define provider verification failure (CodeRabbit, lines 97–110).**
   `ensureMountedWorkspace` says it "optionally verifies the requested
   provider is already connected" but is silent on what happens when the
   provider is not connected. v1 **MUST** be explicit: when
   `verifyProvider: true` (the default for `ensureMountedWorkspace`), an
   unconnected provider throws `ProviderNotConnectedError`; a connected
   but not-yet-ready provider throws `ProviderNotReadyError`. With
   `verifyProvider: false`, the SDK proceeds and the handle does **not**
   imply provider readiness — see §6.

The CodeRabbit grammar nit on `app/web` is editorial-only and does not
gate this contract.

### 1.2 Out of scope for v1

- `relayfile-cloud-host` orchestration / Daytona-specific glue — separate doc.
- A new public Cloud SDK package surface; mount-session is consumed via
  `@relayfile/sdk` and the existing `RelayfileSetup` / `WorkspaceHandle`.
- Per-mount-session token downscoping. v1 reuses the workspace JWT path
  (`agent-workspace-golden-path-acceptance.md §1`); scoped variants belong
  to a follow-up that builds on `WorkspaceHandle.agentInviteScoped`.

---

## 2. Reconcile mode names — `poll | fuse`, never `stream`

PR 498 uses `mode: "poll" | "stream"`. The shipping mount loop
(`cmd/relayfile-mount/main.go`, `internal/mountsync/syncer.go`) only
implements `poll | fuse`, where `fuse` itself is gated by build tags and
defaults to `errFuseModeUnavailable` in the OSS build
(`cmd/relayfile-mount/main.go:35`).

**Decision (binding for v1):** the wire and SDK contract use
`mode: "poll" | "fuse"`. `stream` is **NOT** introduced. The polling
loop already opens an optional websocket invalidation channel
(`websocketReconcileEvery`, `cmd/relayfile-mount/main.go:31`); that is an
optimization on top of `poll`, not a separate user-visible mode. We will
not ship a name that promises real-time semantics we cannot deliver
end-to-end.

- `mode` defaults to `"poll"` everywhere — Cloud route, SDK input, handle
  state, env block (`RELAYFILE_MOUNT_MODE`).
- `"fuse"` is accepted at the API boundary but the launch helper (§7)
  **MUST** detect `errFuseModeUnavailable` and surface a typed
  `MountModeUnavailableError` rather than silently downgrade.
- If a future PR wires a true streaming mode, it gets its own enum value
  and a separate acceptance bump. Until then, anyone passing `"stream"`
  receives `400 invalid_mode` from the Cloud route and a typed
  `InvalidMountModeError` from the SDK.

---

## 3. Cloud API acceptance — `POST /api/v1/workspaces/{workspaceId}/relayfile/mount-session`

### 3.1 Auth and access

- The route **MUST** call `resolveRequestAuth(request)`
  (`packages/web/lib/auth/request-auth.ts:312`). Anonymous requests
  with no Authorization header receive `401 Unauthorized`.
- Workspace lookup **MUST** go through `createCloudWorkspaceRegistry().registry.get(workspaceId)`.
- For workspaces with a real owner (`createdBy !== "00000000-0000-0000-0000-000000000000"`),
  the route **MUST** require `hasWorkspaceOwnerAccess(workspace, auth.userId)`
  OR a verified Relayfile JWT bound to this workspace
  (`hasWorkspaceAccess(auth, workspaceId)`), matching the existing
  `connect-session` route shape.
- Anonymous workspaces (nil-UUID owner) remain joinable today
  (`packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts:99`).
  The mount-session route **MUST** mirror that: for nil-UUID owners,
  permit any caller who supplies a valid Relayfile JWT for the workspace,
  but **MUST NOT** mint a fresh mount session for completely unauthenticated
  callers. Rationale: this route hands out a token; we never mint tokens
  for an unauthenticated request, even on anonymous workspaces.
- On any auth/owner failure return `404 Workspace not found` (avoids
  leaking workspace existence) for non-anonymous workspaces and `401`
  for missing credentials, matching the join route's behavior.

### 3.2 Request validation

```ts
type MountSessionRequest = {
  localDir: string;            // required, see §3.3
  remotePath?: string;         // default "/", see §3.4
  mode?: "poll" | "fuse";      // default "poll"; reject "stream"
  agentName?: string;          // optional; defaults to "relayfile-mount"
  scopes?: string[];           // optional; subset of caller's grant
  provider?: WorkspaceIntegrationProvider; // optional; for diagnostics only
};
```

- Reject (`400 invalid_request`) when the body is not an object, when
  required fields are missing, or when types are wrong.
- Reject (`400 invalid_mode`) for any `mode` outside `{poll, fuse}`.
- Reject (`400 invalid_scopes`) if `scopes` is present but
  `areValidRequestedScopes(body.scopes)` is false (reuse the join-route
  helper from `packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts`).
- `provider` is a hint only; an unknown provider returns
  `400 unknown_provider` to match the existing `connect-session` 409 shape
  is **NOT** required — the route does not gate on it.

### 3.3 `localDir` safety

`localDir` is a hint that the caller intends to write to. The route does
not touch the filesystem; the launcher does. But the route **MUST**
reject obviously dangerous values so they never round-trip through the
session record:

- `localDir` **MUST** be a non-empty string, ≤ 1024 chars after trim.
- Reject `400 invalid_local_dir` for any of: empty after trim, contains a
  NUL byte, contains `..` segments, is a Windows drive root (`C:\`),
  resolves under `/proc`, `/sys`, `/dev`, `/etc/relayfile`, `~`/home of
  the cloud process, or is exactly `/`.
- The route **MUST NOT** attempt to canonicalize the path on the server —
  the cloud has no view of the caller's filesystem. It records the
  string as-is after the validation above.

### 3.4 `remotePath` normalization

- Default `"/"` if absent or empty after trim.
- Normalize via existing helpers used by `mount-harness.ts` (forward
  slashes, leading `/`, no trailing slash except root, no `..`).
- Reject `400 invalid_remote_path` if normalization fails or path
  contains a NUL byte.

### 3.5 Token minting and response

The handler **MUST** call `createWorkspaceJoinAccess` with the workspace
permissions and any caller-requested `scopes` (subset-checked against the
caller's JWT — same enforcement as `agentInviteScoped`). It then returns:

```ts
type MountSessionResponse = {
  workspaceId: string;
  relayfileBaseUrl: string;     // = access.relayfileUrl (no trailing slash)
  relayfileToken: string;        // = access.token
  wsUrl: string;                 // = access.wsUrl
  remotePath: string;            // normalized (§3.4)
  localDir: string;              // echoed unchanged (§3.3)
  mode: "poll" | "fuse";
  scopes: string[];              // = access.scopes
  tokenIssuedAt: string | null;  // ISO from createWorkspaceJoinAccess
  expiresAt: string | null;      // = access.tokenExpiresAt
  suggestedRefreshAt: string | null;
  relaycastApiKey: string;       // for parity with join, used by harness
  relaycastBaseUrl?: string;     // only when configured (§3.6)
};
```

- `expiresAt` and `suggestedRefreshAt` **MUST** come from
  `createWorkspaceJoinAccess` (`packages/web/lib/relay-workspaces.ts:586`)
  and **MUST NOT** be invented.
- `relayfileBaseUrl` is the **stripped-trailing-slash** form so SDK
  callers can concatenate paths safely.
- The route **MUST NOT** include the Cloud access token, refresh token,
  or any user PII in the response.

### 3.6 `relaycastBaseUrl`

Reuse `resolveConfiguredRelaycastUrl()`
(`packages/web/lib/workspace-registry.ts`). Include the field iff the
helper returns a non-empty value, exactly mirroring the join route at
`packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts:121-133`.

### 3.7 Errors

| Status | Code                          | Trigger                                                   |
|-------:|-------------------------------|-----------------------------------------------------------|
| 400    | `invalid_request`             | malformed JSON / bad shape                                 |
| 400    | `invalid_mode`                | `mode` ∉ {poll, fuse}                                       |
| 400    | `invalid_local_dir`           | localDir fails §3.3                                         |
| 400    | `invalid_remote_path`         | remotePath fails §3.4                                       |
| 400    | `invalid_scopes`              | requested scopes invalid or exceed caller grant             |
| 401    | `unauthorized`                | no/invalid auth                                             |
| 403    | `forbidden`                   | auth present but not owner / not workspace-bound JWT        |
| 404    | `workspace_not_found`         | unknown workspace                                           |
| 500    | `mount_session_failed`        | join/access mint failed                                     |

### 3.8 OpenAPI + contract drift

- Add the path entry under `openapi/relayfile-v1.openapi.yaml` (per
  CLAUDE.md OpenAPI guidance) **on the relayfile side** if and only if
  this route is also exposed by the relayfile self-hosted server. It is
  **not**: this route lives in the cloud control plane (Next.js). The
  cloud repo has its own contract surface; document the route in
  `cloud/docs/openapi/...` if/when that file exists, otherwise the unit
  tests in §8 are the contract.
- `scripts/check-contract-surface.sh` covers the relayfile OSS server
  only, so this route does not interact with it.

---

## 4. SDK acceptance — `RelayfileSetup.mountWorkspace` and `ensureMountedWorkspace`

These additions live in `packages/sdk/typescript/src/setup.ts` and are
exported alongside `RelayfileSetup`/`WorkspaceHandle`. They **MUST NOT**
break the v0.6.x contract (`docs/sdk-setup-client-acceptance.md`).

### 4.1 `RelayfileSetup.mountWorkspace`

```ts
interface MountWorkspaceInput {
  workspace?: WorkspaceHandle;          // preferred when caller already has one
  workspaceId?: string;                 // alternative — SDK will join internally
  localDir: string;
  remotePath?: string;                  // default "/"
  mode?: "poll" | "fuse";               // default "poll"
  background?: boolean;                 // default true; false runs synchronous probe-only
  agentName?: string;
  scopes?: string[];
  signal?: AbortSignal;
  launcher?: MountLauncher;             // injected for tests/sandboxes
  readyTimeoutMs?: number;              // default 60_000
}

class RelayfileSetup {
  mountWorkspace(input: MountWorkspaceInput): Promise<MountedWorkspaceHandle>;
  ensureMountedWorkspace(input: EnsureMountedWorkspaceInput): Promise<MountedWorkspaceHandle>;
}
```

Acceptance:

- Exactly one of `workspace` / `workspaceId` **MUST** be provided; both
  or neither throws `MountSessionInputError`.
- When `workspaceId` is provided, the SDK **MUST** call
  `joinWorkspace(workspaceId, { agentName, scopes })` to obtain a handle
  — same behavior as `RelayfileSetup.joinWorkspace`.
- The SDK then **MUST** `POST /api/v1/workspaces/{id}/relayfile/mount-session`
  using `WorkspaceHandle.requestJson` so it inherits retries, timeouts,
  and token refresh.
- Validation errors from the route (`400 invalid_*`) **MUST** be surfaced
  as typed errors (`InvalidMountModeError`, `InvalidLocalDirError`,
  `InvalidRemotePathError`) — never an opaque `CloudApiError(400)`.
- The SDK **MUST NOT** start the mount loop itself. It calls the
  `launcher` (defaults to the bundled launcher, see §7) and returns the
  handle only after readiness is confirmed.
- `background: false` skips the long-running launcher and returns a
  handle whose `status().ready === true` reflects a single one-shot
  probe; `stop()` is a no-op in that case. This mode is the test/CI
  hook (`relayfile-mount --once`).
- `signal` cancellation **MUST** abort both the mount-session POST and
  the launcher start; partial state is cleaned up via `stop()`.

### 4.2 `RelayfileSetup.ensureMountedWorkspace`

```ts
interface EnsureMountedWorkspaceInput extends MountWorkspaceInput {
  provider?: WorkspaceIntegrationProvider; // required when verifyProvider=true
  verifyProvider?: boolean;                // default true
  providerReadyTimeoutMs?: number;         // default 0 (no extra wait)
}
```

Acceptance:

- `verifyProvider` defaults to **`true`**. When true, `provider` is
  required; the SDK calls `WorkspaceHandle.isConnected(provider, ...)`
  and `getConnectionStatus` to confirm `ready === true` BEFORE issuing
  the mount-session request. See §6 for failure semantics.
- `verifyProvider: false` skips the precheck. The handle is returned
  without the SDK making any provider claim. Callers who flip this off
  are responsible for handling unconnected/empty mounts.
- After verification, behavior is identical to `mountWorkspace`. The
  function does not "ensure" anything beyond the verification gate; it
  is sugar for `verify-then-mount`, not a long-lived watchdog.

### 4.3 Type exports

`packages/sdk/typescript/src/setup-types.ts` adds:

- `MountWorkspaceInput`, `EnsureMountedWorkspaceInput`,
  `MountedWorkspaceHandle`, `MountedWorkspaceStatus`, `MountLauncher`.

`packages/sdk/typescript/src/setup-errors.ts` adds:

- `MountSessionInputError`, `InvalidMountModeError`,
  `InvalidLocalDirError`, `InvalidRemotePathError`,
  `MountModeUnavailableError`, `MountReadyTimeoutError`,
  `ProviderNotConnectedError`, `ProviderNotReadyError`.

All are subclasses of `RelayfileSetupError` and re-exported from
`packages/sdk/typescript/src/index.ts`.

---

## 5. `MountedWorkspaceHandle` acceptance

```ts
interface MountedWorkspaceHandle {
  readonly workspaceId: string;
  readonly localDir: string;            // absolute path, post-resolution
  readonly remotePath: string;          // normalized
  readonly mode: "poll" | "fuse";
  readonly ready: boolean;              // mirrors last status() snapshot
  readonly expiresAt: string | null;    // from cloud response
  readonly suggestedRefreshAt: string | null;

  env(): Record<string, string>;
  status(): Promise<MountedWorkspaceStatus>;
  stop(): Promise<void>;
}

interface MountedWorkspaceStatus {
  ready: boolean;
  mode: "poll" | "fuse";
  pid?: number;
  lastHeartbeatAt?: string;
  lastReconcileAt?: string;
  lastEventAt?: string;
  expiresAt: string | null;
  suggestedRefreshAt: string | null;
  pendingWriteback?: number;
  pendingConflicts?: number;
}
```

Acceptance:

- `env()` **MUST** return a superset-compatible block with
  `WorkspaceHandle.mountEnv()` (`packages/sdk/typescript/src/setup.ts:514`):
  `RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`, `RELAYFILE_WORKSPACE`,
  `RELAYFILE_REMOTE_PATH`, `RELAYFILE_LOCAL_DIR`, `RELAYFILE_MOUNT_MODE`,
  `RELAYCAST_API_KEY`, `RELAY_API_KEY`, `RELAYCAST_BASE_URL`,
  `RELAY_BASE_URL`. Values come from the mount-session response, not the
  underlying workspace JWT — the mount session is the canonical token
  for the launched mount.
- `status()` **MUST** read `${localDir}/.relay/state.json` first
  (`docs/productized-cloud-mount-contract.md §3.2`) and fall back to a
  one-shot HTTP probe if the file is absent or stale (>2× interval).
- `stop()` **MUST** be idempotent and **MUST**:
  1. Send SIGTERM to the launcher pid (or call `launcher.stop()` for the
     deterministic harness),
  2. Await drain of in-flight syncs with a 10s deadline,
  3. Remove watchers / timers,
  4. Leave `${localDir}` and `.relay/state.json` intact (never delete user
     files; do **NOT** `rm -rf` the mirror).
- `expiresAt` and `suggestedRefreshAt` are stable properties of the
  handle. `status()` re-reads them only if a future refresh path mints a
  new token — out of scope for v1, so callers can treat them as
  read-once.

---

## 6. Provider verification semantics

`verifyProvider` flips the contract in `ensureMountedWorkspace`. The
matrix is:

| `verifyProvider` | `provider` connected? | provider `ready`? | Outcome                                       |
|------------------|-----------------------|-------------------|-----------------------------------------------|
| `true` (default) | no                    | n/a               | throw `ProviderNotConnectedError(provider)`   |
| `true`           | yes                   | no                | optionally wait `providerReadyTimeoutMs`; on timeout throw `ProviderNotReadyError({ provider, state, initialSyncState })` |
| `true`           | yes                   | yes               | proceed to mount-session, return handle       |
| `false`          | n/a                   | n/a               | proceed to mount-session, return handle (handle makes no provider claim) |

Acceptance:

- `ProviderNotConnectedError` extends `RelayfileSetupError`, carries
  `provider`, and serializes with `code = "provider_not_connected"`.
- `ProviderNotReadyError` carries `provider`, `state`, and
  `initialSyncState` from the status route, so callers can render
  meaningful UX without a second round-trip.
- The error is thrown **before** the mount-session POST runs. No token
  is minted, no launcher is started.
- `mountWorkspace` (without "ensure") **MUST NOT** verify providers; it
  is the lower-level entry point. Callers who want verification use
  `ensureMountedWorkspace`.
- The SDK **MUST NOT** silently downgrade `verifyProvider: true` to
  `false` when no `provider` is supplied — it throws
  `MountSessionInputError("provider required when verifyProvider=true")`.

---

## 7. Local launcher and readiness semantics

The launcher is the moving part the SDK invokes after the mount-session
POST succeeds. It is pluggable via `MountLauncher`; the default
implementation supervises `relayfile-mount`. Tests inject the
`mount-harness.ts` deterministic harness from
`packages/sdk/typescript/src/mount-harness.ts`.

```ts
interface MountLauncher {
  start(input: MountLauncherStart): Promise<MountLauncherInstance>;
}

interface MountLauncherStart {
  env: Record<string, string>;
  cwd?: string;
  signal?: AbortSignal;
  readyTimeoutMs: number;
  onEvent?: (event: MountLauncherEvent) => void;
}

interface MountLauncherInstance {
  pid?: number;
  ready: Promise<void>;        // resolves on first successful sync/probe
  status(): Promise<MountedWorkspaceStatus>;
  stop(): Promise<void>;
}
```

Acceptance:

- The default launcher **MUST** invoke `relayfile-mount` (binary
  resolved by `which relayfile-mount` then via the path embedded in the
  `@relayfile/sdk` package, in that order). It runs in the background
  unless `background: false`.
- Stdout/stderr **MUST** be tee'd to `${localDir}/.relay/mount.log` with
  rotation matching `docs/guides/vfs-cloud-setup.md` (10 MB × 3 files);
  the pid is written to `${localDir}/.relay/mount.pid` atomically.
- `ready` **MUST** resolve only after a usable readiness signal:
  1. `${localDir}/.relay/state.json` exists AND
  2. its `lastReconcileAt` parses as ISO AND
  3. for every entry in `providers`, `status` is one of
     `ready | syncing | unknown` (never `error`),
     OR
  4. (fallback) a one-shot HTTP `GET /v1/workspaces/{id}/fs/tree?path=…&depth=1`
     using `RELAYFILE_TOKEN` returned `200` within `readyTimeoutMs`.
- `ready` **MUST** reject with `MountReadyTimeoutError` when
  `readyTimeoutMs` elapses; `stop()` is then called automatically.
- The launcher **MUST NOT** declare ready based on process startup
  alone. "Process is alive" is not a readiness proof — `state.json` or a
  successful HTTP probe is.
- For `mode: "fuse"`: if the launcher receives `errFuseModeUnavailable`
  on stderr or exits non-zero with that signature, it **MUST** translate
  to `MountModeUnavailableError` and never fall back to `poll` silently.
- The deterministic harness (`startMountHarness`) is the test substitute.
  It honors the same readiness contract — `started` + first
  `sync.completed` event satisfies (1)–(3) without writing
  `state.json`, and tests **SHOULD** inject `launcher: harnessLauncher`
  rather than spawn a real process.

---

## 8. Test matrix

All tests **MUST** pass before the cloud and relayfile PRs merge.

### 8.1 Cloud route unit tests (`tests/sdk-setup-client-routes.test.ts` style)

Located in the cloud worktree, mirroring the existing fixture in
`tests/sdk-setup-client-routes.test.ts`. Coverage:

- 200: minimal valid request → response shape (§3.5) including
  `expiresAt`, `suggestedRefreshAt`, `relayfileBaseUrl` (no trailing
  slash), default `mode === "poll"`, default `remotePath === "/"`.
- 200: scoped request with `scopes: ["fs:read"]` → response carries the
  narrowed scopes.
- 200: anonymous workspace + valid Relayfile JWT → mints a session.
- 401: anonymous workspace + no auth header → `unauthorized`.
- 401: invalid/expired Relayfile JWT.
- 403: valid JWT but wrong workspace.
- 404: unknown workspace id.
- 400: `mode === "stream"` → `invalid_mode`.
- 400: `localDir` is `..`, `/`, contains NUL, contains `..` segment, or
  > 1024 chars → `invalid_local_dir`.
- 400: malformed `remotePath` → `invalid_remote_path`.
- 400: `scopes` exceeds caller grant → `invalid_scopes`.
- `relaycastBaseUrl` present iff `resolveConfiguredRelaycastUrl()` returns
  a value (parity with golden-path test
  `tests/agent-workspace-golden-path-routes.test.ts:136`).

### 8.2 SDK unit tests (`packages/sdk/typescript/src/setup.test.ts`)

- `mountWorkspace` with a `WorkspaceHandle`: posts the mount-session,
  spawns a fake launcher, resolves the handle once `launcher.ready`
  resolves.
- `mountWorkspace` with `workspaceId`: joins, then mounts.
- `mountWorkspace` rejects with `MountSessionInputError` when both
  `workspace` and `workspaceId` are passed, or neither.
- `mountWorkspace` with `mode: "stream"` → `InvalidMountModeError`
  before any HTTP call.
- `mountWorkspace` with `signal.abort()` after POST: launcher is
  stopped; promise rejects with `CloudAbortError`.
- `ensureMountedWorkspace` with `verifyProvider: true` and
  unconnected provider → `ProviderNotConnectedError`; no
  mount-session POST occurs.
- `ensureMountedWorkspace` with `verifyProvider: true` and
  connected-but-not-ready provider, with `providerReadyTimeoutMs: 0`
  → `ProviderNotReadyError` immediately.
- `ensureMountedWorkspace` with `verifyProvider: false` → no provider
  call, posts mount-session.
- `MountedWorkspaceHandle.env()` returns the expected superset of
  `mountEnv()` keys.
- `handle.status()` reads `state.json` when present; falls back to HTTP
  probe when absent.
- `handle.stop()` is idempotent and never deletes `${localDir}`.

### 8.3 Launcher tests (`packages/sdk/typescript/src/mount-launcher.test.ts`)

- Readiness gate: launcher does not resolve `ready` until `state.json`
  contains a valid `lastReconcileAt` AND non-error provider state.
- `readyTimeoutMs` elapses with no `state.json` → `MountReadyTimeoutError`,
  followed by `stop()`.
- `mode: "fuse"` with stub child that exits with `errFuseModeUnavailable`
  → `MountModeUnavailableError`, no silent fallback.
- pid file + log file written under `${localDir}/.relay/`.
- `stop()` after a successful start sends SIGTERM, drains, returns.

### 8.4 Packaged consumer E2E (`packages/sdk/typescript/scripts/post-auth-mount-session-e2e.mjs`)

Mirrors `agent-workspace-golden-path-e2e.mjs`. Uses the in-process mock
Cloud (`agent-workspace-mocks.mjs`) plus the in-process mock relayfile
server. Scenarios:

1. Lead agent calls `setup.mountWorkspace({ workspaceId, localDir,
   remotePath: "/notion" })` after `connectNotion` + `waitForNotion`,
   reads a seeded `/notion/research/brief.md` from `localDir`, asserts
   `handle.expiresAt` is non-null, then `handle.stop()`.
2. Invited agent calls `setup.ensureMountedWorkspace({ workspace,
   provider: "notion", verifyProvider: true, scopes: ["fs:read"],
   localDir })`, attempts a write → `MountHarnessPermissionError`
   (read-only path).
3. `verifyProvider: true` against an unconnected provider →
   `ProviderNotConnectedError`; no mount session is created (assert via
   the mock cloud's request log).
4. `mode: "stream"` rejected at the SDK layer (no HTTP traffic).

Evidence path: `docs/evidence/post-auth-mount-session-e2e.log`.

### 8.5 Regression commands

```bash
# Relayfile worktree
npm test --workspace=packages/sdk/typescript
npm run demo:agent-workspace --workspace=packages/sdk/typescript
npm run e2e:post-auth-mount-session --workspace=packages/sdk/typescript
go test ./cmd/relayfile-mount/... ./internal/mountsync/...
scripts/check-contract-surface.sh

# Cloud worktree
pnpm vitest run tests/sdk-setup-client-routes.test.ts
pnpm vitest run tests/post-auth-mount-session-route.test.ts
pnpm typecheck && pnpm build
```

CI **MUST** keep the cloud PR green on the same checks listed in
cloud#498's `statusCheckRollup` (Unit Tests, Typecheck, Next.js build,
Sage worker bundle probe, RelayFile observer package build, Validate
secret wiring, Snapshot Impact Check).

---

## 9. Agent responsibilities

The post-auth mount-session work runs through a four-agent loop. Roles
are non-fungible — each pass has a different model for adversarial
diversity.

| Phase                     | Agent                | Output                                                      |
|---------------------------|----------------------|-------------------------------------------------------------|
| 1. Contract authoring     | **Claude (lead)**    | This document; reconciles cloud#498 with relayfile reality. |
| 2. Implementation         | **Codex**            | Cloud route + SDK additions + launcher + tests.             |
| 3. Self-reflection        | **Codex**            | Diff review against §3–§7 acceptance bullets; trail entry.   |
| 4. Codex self-review      | **Codex**            | Adversarial second pass: missing edge cases, error wiring.   |
| 5. Claude peer review     | **Claude (review)**  | Final review against this contract; signs off or returns.    |

Operational expectations:

- The **Claude lead** owns this contract and any contract amendments.
  Claude does not write production code in this loop; clarifications
  land here as patch revisions, not in the implementation PR.
- **Codex** opens both PRs, runs §8 commands locally, and pastes
  evidence URLs into the PR bodies. Codex **MUST** record decisions via
  `trail decision` for each non-trivial choice (e.g. "rejected stream
  mode in v1 per contract §2"). Codex **MUST NOT** introduce features
  beyond §3–§7.
- **Codex self-reflection** is mandatory: after the first green run,
  re-read this contract end-to-end and produce a checklist
  (`docs/evidence/post-auth-mount-session-self-review.md`) marking each
  acceptance bullet PASS / FAIL with a code anchor.
- **Codex self-review** is the adversarial second pass — same agent,
  fresh framing: "find the way this breaks in production." Output goes
  in the same evidence file under a separate header.
- **Claude review** consumes the evidence file plus the diffs and
  either approves or returns with a numbered list of contract
  citations. No vibes-based feedback; every comment cites a §-number.

---

## 10. PR policy

The work spans two repos: `relayfile` (this repo) and `cloud`. Both
PRs **MUST** ship together — neither is independently useful.

1. Use `gh` for both PRs. No web-UI submissions.
2. Branch names:
   - relayfile: `feat/post-auth-mount-session`
   - cloud:     `feat/post-auth-mount-session-route`
3. Each PR body **MUST** include:
   - A link to `AgentWorkforce/cloud#498` ("Implements the contract
     proposed in #498; companion PR: <other-PR-url>").
   - The contract anchor: `docs/post-auth-mount-session-contract.md`
     (relayfile path).
   - Test evidence: paths under `docs/evidence/` and the regression
     commands from §8.5.
   - A short **Behavioral changes** section listing the new route, new
     SDK methods, and new env-block guarantees.
4. Cross-link the PRs in both directions before requesting review.
5. Do not squash-merge across repos in the wrong order. The relayfile
   SDK PR depends on the cloud route, so the cloud PR **MUST** merge
   first; the relayfile SDK PR rebases against the deployed cloud route
   and re-runs §8.4 against the staging deployment before merge.
6. After merge, run `trail compact --pr <num> --discard-sources` in the
   relayfile worktree to fold this work into the durable record per
   `CLAUDE.md`.

---

POST_AUTH_MOUNT_CONTRACT_READY
