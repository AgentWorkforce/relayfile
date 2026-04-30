# SDK Setup Client — Final Cross-Repo Review

Reviewer: non-interactive reviewer agent
Scope: relayfile + cloud current diff against `docs/sdk-setup-client.md` and
`docs/sdk-setup-client-acceptance.md`.

## Checklist verdicts

### 1. Default cloud URL `https://agentrelay.com/cloud` everywhere
PASS in the SDK and tests.
- `packages/sdk/typescript/src/setup.ts:27` — `DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"`.
- Spec aligned: `docs/sdk-setup-client.md:97`, `:111` use the new default.
- `packages/sdk/typescript/src/setup.test.ts:80–82` assert the exact default
  on both `POST /workspaces` and `POST /workspaces/:id/join` URLs.

### 2. URL joining preserves `/cloud` base path
PASS.
- `buildCloudUrl` (`setup.ts:634–640`) appends a trailing `/` to
  `URL.pathname` and then resolves the relative path, so a base of
  `https://agentrelay.com/cloud` plus `api/v1/workspaces` becomes
  `https://agentrelay.com/cloud/api/v1/workspaces`.
- The override test (`setup.test.ts:84–109`) uses
  `https://staging.agentrelay.com/base/cloud` and asserts the prefix is
  preserved on join, connect-session, GET status, and DELETE status.

### 3. Public setup surface exported without breaking existing exports
PASS.
- `packages/sdk/typescript/src/index.ts` re-exports `RelayfileSetup`,
  `WorkspaceHandle`, and every option/result/info type required by the spec
  and acceptance contract: `RelayfileSetupOptions`,
  `RelayfileSetupRetryOptions`, `CreateWorkspaceOptions`,
  `JoinWorkspaceOptions`, `WorkspaceInfo`, `ConnectIntegrationOptions`,
  `ConnectIntegrationResult`, `WaitForConnectionOptions`,
  `WorkspaceIntegrationProvider`, `WorkspacePermissions`, plus the runtime
  `WORKSPACE_INTEGRATION_PROVIDERS` const.
- Error classes exported: `RelayfileSetupError`, `CloudApiError`,
  `CloudTimeoutError`, `CloudAbortError`,
  `IntegrationConnectionTimeoutError`, `MalformedCloudResponseError`,
  `MissingConnectionIdError`, `UnknownProviderError`.
- Existing client exports (`RelayFileClient`, sync, types) are untouched.

### 4. Error classes carry the spec's fields
PASS.
- `setup-errors.ts`: `CloudApiError` exposes `httpStatus` + `httpBody`;
  `MalformedCloudResponseError` exposes `field` + `response`;
  `IntegrationConnectionTimeoutError` exposes `provider`, `connectionId`,
  `elapsedMs`, `timeoutMs`; `UnknownProviderError` exposes `provider`;
  `MissingConnectionIdError` exposes `provider`; `CloudTimeoutError` and
  `CloudAbortError` expose `operation`. All set `code` via the base class.

### 5. Required public methods are tested
PASS at the unit-test level and at the package-consumer E2E level.
- `setup.test.ts` covers `createWorkspace`, `joinWorkspace`,
  `connectIntegration` (already-connected and OAuth paths),
  `waitForConnection` (delayed success, 429 retry-after, timeout, abort,
  immediate 401), `client()` singleton + token refresh through the same
  join options, `getToken()`, and the malformed-response path.
- `disconnectIntegration` and `isConnected` are exercised in
  `setup.test.ts:84–109` (URL preservation suite) and through the E2E
  scenario `is-connected-and-disconnect`.
- E2E `scripts/setup-e2e.mjs` covers every spec scenario including
  refresh-token, delayed wait, abort, timeout, retry-after, 5xx retry, 401
  fast fail, malformed responses, and `CloudApiError` body parsing.

### 6. Cloud accepts relayfile JWT only for the same workspace
PASS in the route logic and the new test file.
- `packages/web/lib/auth/request-auth.ts` adds `tryRelayfileJwtAuth` that
  RS256-verifies via `TokenVerifier` against the relayauth JWKS, audience
  `["relayfile"]`, and issuer = relayauth base URL. The verified
  `RequestAuth` carries `source: "relayfile"` plus the JWT's `wks` as
  `workspaceId`.
- `connect-session/route.ts:35` and the status route's GET/DELETE both run
  through `hasWorkspaceAccess`, which for non-session sources requires
  `auth.workspaceId === workspaceId`. Mismatch → 403. Missing auth → 401.
- `tests/sdk-setup-client-routes.test.ts` proves all three: accept matching
  JWT (200), unsigned JWT (401), wrong-workspace JWT (403), missing auth
  (401), and a session user without workspace access (403).

### 7. Status GET handles omitted `connectionId` and explicit IDs
PASS.
- `status/route.ts:62–73` no longer 400s on empty `connectionId`. It treats
  empty or `connectionId === workspaceId` as "ready iff a
  `workspace_integrations` row exists for `(workspaceId, provider)`",
  falling back to strict equality otherwise.
- Tests cover all three branches: omitted (ready false then ready true),
  workspace-id sentinel (ready true), and explicit matching connectionId
  (ready true).

### 8. DELETE implements `disconnectIntegration` per spec
PASS.
- New `DELETE` handler in `status/route.ts:81–120` performs the same auth
  + workspace gate, calls `deleteWorkspaceIntegration(workspaceId, provider)`,
  and returns `{ success: true }` with status 200. The handler does not 404
  on a missing row — the test `disconnects integrations through DELETE and
  remains idempotent` confirms two DELETEs both return 200/`success: true`.

### 9. Package-consumer E2E imports the packed tarball, not source
PASS.
- `packages/sdk/typescript/scripts/setup-e2e.mjs:732–772` builds, packs the
  built `@relayfile/sdk` (and `@relayfile/core`), creates a fresh consumer
  dir under `os.tmpdir()`, `npm install`s the tarball, and writes a
  `consumer.mjs` that imports from `"@relayfile/sdk"` only — never from
  `packages/sdk/typescript/src/...`.
- The script asserts the actual mock-server paths for every spec endpoint:
  `POST /api/v1/workspaces`, `POST /api/v1/workspaces/:id/join`,
  `POST /api/v1/workspaces/:id/integrations/connect-session`,
  `GET /api/v1/workspaces/:id/integrations/:provider/status`,
  `DELETE /api/v1/workspaces/:id/integrations/:provider/status`, and the
  relayfile `GET /v1/workspaces/:id/fs/tree`.

### 10. No unrelated refactor or broad churn
PASS after remediation.

The earlier review found unrelated cloud churn in
`packages/core/src/bootstrap/script-generator.ts`,
`tests/orchestrator/script-generator.test.ts`, and
`workflows/github-clone-durable-queue.ts`. Those substantive out-of-scope
changes are no longer present in the current cloud diff. The remaining cloud
code changes are limited to the SDK setup contract files:

- `packages/web/lib/auth/request-auth.ts`
- `packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts`
- `packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts`
- `tests/sdk-setup-client-routes.test.ts`

The cloud `.trajectories/index.json` diff is local agent state and is excluded
from the workflow's explicit cloud `git add` command, so it will not enter the
SDK setup commit.

## Other observations (non-blocking)

- `WorkspaceHandle.disconnectIntegration` accepts an unused
  `_connectionId` parameter to match the spec signature; the cloud DELETE
  ignores `connectionId` because the row is keyed on
  `(workspaceId, provider)`. This is consistent with the spec but worth
  flagging because the SDK type allows passing a value that the server
  cannot use to scope the delete.
- The connect-session route falls back to `workspaceId` when Nango does not
  return a `connectionId` — matches Option A in the spec. The SDK mirrors
  this fallback in `connectIntegration` (`setup.ts:331`).
- The relayfile evidence file `docs/evidence/sdk-setup-client-final-evidence.md`
  shows only 4 modified relayfile files because the new files
  (`setup.ts`, `setup-types.ts`, `setup-errors.ts`, `setup.test.ts`,
  `scripts/setup-e2e.mjs`) are still untracked. They must be staged
  individually per acceptance §8 ("git add explicit paths") before the
  commit; otherwise the relayfile change is incomplete. (This is a commit
  hygiene gate rather than a code-review blocker, but worth catching here
  so the final commit doesn't drop the implementation.)

## Required fixes before merge

No code-scope blockers remain. The final commit commands must still use the
explicit path lists from the workflow so local trajectory files and unrelated
untracked cloud workflow files are not staged.

REVIEW_APPROVED
