# Post-Auth Mount-Session Peer Review (Claude) — Final

**Date:** 2026-05-09
**Reviewer:** Claude (final peer review pass after fixes)
**Inputs:**
- Contract: `docs/post-auth-mount-session-contract.md`
- Self-review: `docs/post-auth-mount-session-self-review.md`
- Reflection: `docs/post-auth-mount-session-reflection.md`
- Evidence: `.workflow-worktrees/post-auth-mount-session/relayfile/docs/evidence/post-auth-mount-session-e2e.log`
- Diffs: `.workflow-worktrees/post-auth-mount-session/relayfile` and `.workflow-worktrees/post-auth-mount-session/cloud`

---

## Verdict

**Approved.** All four MUST blockers from the prior peer review are resolved
in the shipping artifacts (the impl worktrees). One SHOULD remains and is
recorded as a non-blocking follow-up.

---

## Blocker resolution status

### Finding 1 — Workspace-existence leak via 403 on cross-workspace JWT (MUST) — RESOLVED

`packages/web/app/api/v1/workspaces/[workspaceId]/relayfile/mount-session/route.ts:80-85`
now returns `404 workspace_not_found` for non-anonymous workspaces when the
caller has neither owner access nor a workspace-bound JWT, regardless of
`auth.source`. The `403 forbidden` branch is retained only for the
anonymous-workspace + foreign relayfile JWT case (where workspace existence
is already discoverable via the join route per contract §3.1). Tests at
`tests/relayfile-mount-session-route.test.ts:417-449` assert both behaviors,
including the new "returns 404 for workspace-scoped auth bound to a
different non-anonymous workspace" case.

### Finding 2 — `ProviderNotReadyError` documented surface mismatch (MUST) — RESOLVED

Contract §6 (`docs/post-auth-mount-session-contract.md:392, 400-401, 521`)
and the guide (`docs/guides/post-auth-mount-session.md:198, 261, 267, 461`)
now uniformly describe the error as `ProviderNotReadyError({ provider,
state, initialSyncState })` and tell consumers to read `.state` /
`.initialSyncState`. No remaining `lastState` references. Implementation at
`packages/sdk/typescript/src/setup-errors.ts:187-210` and thrower at
`packages/sdk/typescript/src/setup.ts:1511-1515` already exported these
fields, so contract and impl agree.

### Finding 3 — Cloud route §8.1 test matrix incomplete (MUST) — RESOLVED

`tests/relayfile-mount-session-route.test.ts` is now 615 LOC across 17
cases. New coverage explicitly maps onto contract §8.1:

- 200 narrowed scopes for same-workspace relayfile caller (line 291).
- 200 anonymous workspace + valid Relayfile JWT (line 360).
- 401 anonymous workspace + missing auth (line 402).
- 401 invalid relayfile bearer token (line 388).
- 404 wrong-workspace JWT (line 417, see Finding 1).
- 404 unknown workspace id (line 451).
- 400 dangerous `localDir` enumeration via `it.each` over `/`, `..`,
  `/tmp/../escape`, `bad\0path`, `"a".repeat(1025)` (lines 481-505).
- 400 malformed `remotePath` (line 528).
- 200 + `relaycastBaseUrl` omission when env unset (line 580).
- 400 `invalid_scopes` when caller grant is exceeded (line 596).

Together with the pre-existing happy-path, workspace API token,
same-workspace JWT, missing auth, mode rejection, and provider-secret
absence cases, the §8.1 matrix is complete.

### Finding 4 — Workflow-worktree E2E does not exercise the product path (MUST) — RESOLVED for shipping artifacts

In the impl worktree
(`.workflow-worktrees/post-auth-mount-session/relayfile`):

- `packages/sdk/typescript/scripts/post-auth-mount-session-e2e.mjs` is the
  substantive 1145-LOC consumer test (no longer a wrapper, no longer
  named `post-auth-mount-e2e.mjs`).
- `packages/sdk/typescript/package.json` registers both
  `post-auth-mount:e2e` and `e2e:post-auth-mount-session` pointing at
  that script (lines 16-17).
- `docs/evidence/post-auth-mount-session-e2e.log` captures all four
  contract §8.4 scenarios plus the SDK-layer `mode: "stream"` rejection
  with substantive per-scenario verification lines:
  - "Verified mountWorkspace request path, body, authorization, env
    mapping, seeded /notion/research/brief.md sync, expiresAt, status(),
    env(), and stop()"
  - "Verified ensureMountedWorkspace verifyProvider=true success path"
  - "Verified ensureMountedWorkspace invited read-only path and
    MountHarnessPermissionError"
  - "Verified ensureMountedWorkspace verifyProvider=true failure path"
  - "Verified mode=\"stream\" is rejected by the SDK before any HTTP
    request"
  - terminating `POST_AUTH_MOUNT_E2E_OK`.

The workflow E2E gate (`workflows/065-post-auth-mount-session.ts:618-624`)
runs in `RELAYFILE_ROOT` (the impl worktree), so this is the canonical
evidence log for the workflow gate.

Note (non-blocking): the workflow cwd
(`relayfile-post-auth-mount-session-worktree`) still carries a 113-line
wrapper `post-auth-mount-session-e2e.mjs` plus a now-modified
`agent-workspace-golden-path-e2e.mjs` that imports
`MountHarnessPermissionError`. Because the workflow cwd's SDK source does
not include `mountWorkspace`, a manual run there fails with
`setup.mountWorkspace is not a function`. This artifact is not consumed by
the workflow gate and does not affect the product PRs (the SDK changes
ship from the impl worktree). Recommend cleaning up the workflow cwd
modifications before commit, but this is not a blocker.

### Finding 5 — `verifyWorkspaceProviderReady` couples to literal `"not_connected"` (SHOULD) — UNCHANGED

`packages/sdk/typescript/src/setup.ts:1500-1515` still dispatches on
`status.state === "not_connected"` rather than calling
`workspace.isConnected(provider, connectionId)` as contract §6 specifies.
This was raised as a SHOULD in the prior review and remains unaddressed.
It is not a blocker for v1 because the integration status route does
return that exact state literal today. Recorded as a follow-up: switch
the dispatch to `isConnected()` so the SDK does not silently demote a
not-connected provider to `ProviderNotReadyError` if the cloud status
route later renames the state field.

---

## Other checks (re-verified)

- Mode reconciliation (`poll | fuse`, no `stream`): clean at both
  layers (SDK `normalizeMountModeInput` and cloud
  `validateMountSessionBody`).
- No provider secrets, Cloud session tokens, refresh tokens, or PII in
  the response. Test asserts absence of `accessToken`, `refreshToken`,
  `providerSecret`, `relayAuthApiKey`.
- `relaycastBaseUrl` conditionally included only when configured;
  omission case explicitly tested.
- `MountedWorkspaceHandle` exposes `expiresAt` and `suggestedRefreshAt`
  as readonly handle properties and on `status()`. Cloud route maps them
  from `createWorkspaceJoinAccess`, never invents them.
- Launcher cannot resolve `ready` on process startup alone; it gates on
  `state.json` (or HTTP fallback) and translates
  `errFuseModeUnavailable` to `MountModeUnavailableError` without silent
  fallback.
- `stop()` is idempotent and never deletes `${localDir}` or its
  `.relay/state.json`.
- The cloud and relayfile diffs remain compositionally coherent: SDK
  request shape matches cloud response shape, error-code mapping is
  bidirectional on `invalid_mode | invalid_local_dir |
  invalid_remote_path`, and contract §10 cloud-first merge order still
  holds.

---

## Required actions before approval

None. All MUST blockers from the prior peer review are resolved.

## Recommended follow-up (non-blocking)

1. (SHOULD) Refactor `verifyWorkspaceProviderReady` to dispatch on
   `await workspace.isConnected(provider, connectionId)` instead of the
   literal `status.state === "not_connected"` compare, per contract §6.
2. (Hygiene) Revert the unused
   `packages/sdk/typescript/scripts/agent-workspace-golden-path-e2e.mjs`
   and `package.json` modifications in the workflow cwd
   (`relayfile-post-auth-mount-session-worktree`) so the workflow cwd
   does not carry a broken evidence log alongside the impl worktree's
   passing one.

---

PEER_REVIEW_APPROVED
