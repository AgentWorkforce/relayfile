# SDK Setup Client — Cross-Repo Acceptance Contract

This document is the binding acceptance contract for the `RelayfileSetup` SDK
work. It spans the relayfile repo (this repo) and the cloud repo
(`../cloud`). Implementation that does not satisfy every clause in this
contract is not considered complete.

## 1. URL correction

`RelayfileSetup` defaults its `cloudApiUrl` to **`https://agentrelay.com/cloud`**,
**not** `https://app.agentrelay.com`. The `/cloud` base path is mandatory: the
cloud Next.js app is mounted under `/cloud` by the SST router, and any URL
that drops the `/cloud` prefix lands on the legacy fallback proxy and 404s.

Source files (local) that establish this as the source of truth:

- `../cloud/infra/web-routing.ts` — defines `appUrl` as `https://${routerDomain}/cloud`
  in production (and `http://localhost:3000/cloud` in dev), and documents that
  the router only forwards `/cloud*` to the Next.js origin.
- `../cloud/packages/cli/src/cli/constants.ts` — defines
  `DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"` for the CLI, which
  is the canonical default for any cloud-API client we ship.

The relayfile spec (`docs/sdk-setup-client.md`) already documents this URL;
this contract simply ratifies that value as binding and forbids any drift to
`https://app.agentrelay.com` or any URL that omits `/cloud`.

## 2. File ownership

### Relayfile SDK files (this repo)

- `packages/sdk/typescript/src/setup.ts` — `RelayfileSetup` and
  `WorkspaceHandle` implementation.
- `packages/sdk/typescript/src/setup-types.ts` — public option / result /
  info types and `WORKSPACE_INTEGRATION_PROVIDERS` constant.
- `packages/sdk/typescript/src/setup-errors.ts` — `CloudApiError`,
  `CloudTimeoutError`, `CloudAbortError`, and any other setup-specific error
  classes.
- `packages/sdk/typescript/src/setup.test.ts` — vitest unit tests for the
  setup client.
- `packages/sdk/typescript/src/index.ts` — re-export `RelayfileSetup`,
  `WorkspaceHandle`, all setup option/result types, the integration provider
  constant, and the new error classes.
- `packages/sdk/typescript/package.json` — modify **only** if a new test or
  build dependency is genuinely required. No speculative deps.
- Optional E2E harness files under `packages/sdk/typescript/scripts/` or
  top-level `scripts/` (e.g. `scripts/sdk-setup-e2e.ts`) — only if the
  package-consumer E2E in §6 requires standalone harness code, and only with
  a one-line justification in the PR.

### Cloud files (`../cloud`)

- `../cloud/packages/web/lib/auth/request-auth.ts` — extend
  `resolveRequestAuth` so that a verified relayfile JWT is accepted as a
  valid bearer token for workspace-scoped operations on the workspace named
  in its `wks` claim. Must keep existing session, service-token, and cloud
  API-token branches intact.
- `../cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts`
  — accept relayfile JWTs, return either the real Nango `connectionId` (if
  available at session creation) or the workspace ID as the polling key,
  alongside the existing `token`, `expiresAt`, and `connectLink` fields.
- `../cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts`
  — accept relayfile JWTs, treat omitted or `connectionId === workspaceId`
  as "ready when a `workspace_integrations` row exists for this provider",
  and add a `DELETE` handler that removes the integration row for the same
  auth scope.
- `../cloud/packages/web/lib/integrations/providers.ts` — modify **only** if
  cloud provider keys drift from what the SDK ships in
  `WORKSPACE_INTEGRATION_PROVIDERS`. Otherwise leave untouched.
- `../cloud/tests/sdk-setup-client-routes.test.ts` (new, or an
  equivalently-targeted route test name) — covers the new auth and status
  semantics; see §5.
- `../cloud/tests/cli/default-api-url.test.ts` — modify **only** if the URL
  assertion needs to be extended to cover the SDK default. The CLI test must
  remain green either way.

No other cloud file should be touched. Anything else in `../cloud` is out of
scope for this work.

## 3. Endpoint contract to prove

The implementation MUST prove these endpoint behaviors with both unit tests
(cloud route tests) and E2E (§6):

1. **`POST /api/v1/workspaces`** — creates a workspace. Optional bearer auth:
   anonymous callers get a workspace owned by the nil UUID; authenticated
   callers (`session` or `cli:auth`-scoped token) get a workspace owned by
   their user. Invalid credentials → 401.

2. **`POST /api/v1/workspaces/:workspaceId/join`** — returns
   `{ workspaceId, token, relayfileUrl, wsUrl, relaycastApiKey }`. The
   `token` is a relayfile JWT carrying `wks = workspaceId`. Anonymous
   workspaces (nil UUID owner) are joinable by anyone with the workspace ID;
   authenticated workspaces still require owner access.

3. **`POST /api/v1/workspaces/:workspaceId/integrations/connect-session`** —
   accepts a relayfile JWT whose `wks` claim equals the path
   `:workspaceId`. Returns `{ token, expiresAt, connectLink, connectionId }`.
   - If the cloud can know the final Nango `connectionId` at session
     creation, return it.
   - Otherwise return `connectionId = workspaceId` and document that the
     status route treats `connectionId` missing or equal to `workspaceId` as
     "ready when a `workspace_integrations` row exists for this provider".
   - JWT for a different workspace → 403. No auth → 401.

4. **`GET /api/v1/workspaces/:workspaceId/integrations/:provider/status`** —
   accepts relayfile JWT auth, accepts an omitted `connectionId` query
   parameter (defaults to `workspaceId`), and resolves `ready` from the real
   `workspace_integrations` row for `(workspaceId, provider)`. Returns
   `{ ready: true }` once the row exists; `{ ready: false }` before the
   Nango webhook upserts it. Unknown provider → 404. Mismatched workspace →
   403.

5. **`DELETE /api/v1/workspaces/:workspaceId/integrations/:provider/status`**
   — accepts relayfile JWT auth and removes the `workspace_integrations` row
   for `(workspaceId, provider)`. Powers `WorkspaceHandle.disconnectIntegration()`.
   Idempotent: deleting a non-existent integration returns 200/204, not 404.
   JWT for a different workspace → 403.

## 4. SDK public contract

The following constitute the public contract of `@relayfile/sdk` for this
work and MUST be exported from `packages/sdk/typescript/src/index.ts`:

- `RelayfileSetup` class
- `WorkspaceHandle` class (or interface + concrete impl)
- All setup option / result / info types: `RelayfileSetupOptions`,
  `CreateWorkspaceOptions`, `JoinWorkspaceOptions`, `WorkspaceInfo`,
  `ConnectIntegrationOptions`, `ConnectIntegrationResult`,
  `WaitForConnectionOptions`, and any other types named in
  `docs/sdk-setup-client.md`.
- `WORKSPACE_INTEGRATION_PROVIDERS` constant (and its TypeScript union type),
  matching the cloud `WorkspaceIntegrationProvider` set
  (`github`, `slack-sage`, `slack-my-senior-dev`, `slack-nightcto`, `notion`,
  `linear`).
- Setup error classes: `CloudApiError`, `CloudTimeoutError`,
  `CloudAbortError` (and any other error type referenced by the spec).

Behavioral guarantees:

- **Default URL.** `cloudApiUrl` defaults to `https://agentrelay.com/cloud`.
  Constructor option overrides only for staging/dev. URL building for every
  cloud call MUST preserve the `/cloud` base path (e.g. join `${cloudApiUrl}/api/v1/...`,
  never replace the path).
- **Access token.** `accessToken` accepts a `string` or an async factory
  `() => string | Promise<string>`. When present, the SDK sends
  `Authorization: Bearer <token>` on cloud calls. When absent, cloud calls go
  out unauthenticated (anonymous workspace path).
- **`createWorkspace`** calls `POST /api/v1/workspaces` then
  `POST /api/v1/workspaces/:id/join` and returns a `WorkspaceHandle`.
- **`joinWorkspace`** calls only `POST /api/v1/workspaces/:id/join` and
  returns a `WorkspaceHandle`. It does not call `GET /api/v1/workspaces/:id`
  to refresh metadata; it trusts the join response.
- **`connectIntegration`** on a handle:
  1. If `connectionId` is supplied, calls `isConnected({ connectionId })`
     first and returns `{ alreadyConnected: true }` when ready.
  2. Maps the SDK provider name to the cloud config key (using
     `WORKSPACE_INTEGRATION_PROVIDERS` as the canonical list — the cloud
     `getProviderConfigKey` mapping is consulted server-side).
  3. Calls `POST /api/v1/workspaces/:id/integrations/connect-session`.
  4. Stores the `connectionId` returned by the cloud (or infers
     `workspaceId` if omitted) on the handle for later polling.
  5. Returns
     `{ alreadyConnected, connectLink, sessionToken, expiresAt, connectionId }`.
- **`waitForConnection`** polls `GET /api/v1/workspaces/:id/integrations/:provider/status`:
  - Honors `timeoutMs`, `pollIntervalMs`, `AbortSignal`, and `onPoll(elapsed)`.
  - Keeps `intervalMs` as a deprecated alias for already-shipped callers.
  - On HTTP 429, respects the `Retry-After` header (seconds or HTTP date).
  - Performs bounded retry on 5xx (e.g. up to 3 retries with capped
    exponential backoff) before raising `CloudApiError`.
  - Raises `CloudApiError` immediately on 401, 403, 404 — these are not
    retried.
  - Raises `CloudTimeoutError` when `timeoutMs` elapses.
  - Raises `CloudAbortError` when the `AbortSignal` fires.
- **`client()`** returns a **singleton** `RelayFileClient` per
  `WorkspaceHandle`, constructed with `baseUrl = info.relayfileUrl` and an
  async token factory that refreshes the relayfile JWT when its age exceeds
  55 minutes (the JWT TTL is 1h; 55m gives a 5m safety window).
- **`getToken()`** is **synchronous** and returns the currently-cached
  relayfile JWT.
- **`refreshToken()`** is async and re-calls
  `POST /api/v1/workspaces/:workspaceId/join` with the *same options* used
  during the original join (agent name, scopes, permissions), then updates
  the cached `_token` and `_tokenIssuedAt`.

## 5. Required unit tests

The unit-test surface includes every test enumerated in
`docs/sdk-setup-client.md`, plus the following additions.

### Relayfile SDK (`packages/sdk/typescript/src/setup.test.ts`)

- Default cloud URL is exactly `https://agentrelay.com/cloud`.
- Generated requests for `createWorkspace`, `joinWorkspace`,
  `connectIntegration`, `waitForConnection`, `isConnected`, and
  `disconnectIntegration` all hit URLs prefixed with
  `https://agentrelay.com/cloud/api/v1`.
- Constructor override of `cloudApiUrl` to a staging URL with a base path
  preserves that base path on every generated cloud URL.
- Cloud setup requests include `X-Relayfile-SDK-Version`.
- `connectNotion()` is covered as the single-link Notion connect helper.
- `mountEnv()` and `agentInvite()` return relayfile mount values plus
  relaycast credentials for trusted agent handoff.

### Cloud routes (`../cloud/tests/sdk-setup-client-routes.test.ts`)

- `POST .../connect-session` accepts a verified relayfile JWT whose `wks`
  claim matches the path `:workspaceId` and returns
  `{ token, expiresAt, connectLink, connectionId }`.
- `GET .../integrations/:provider/status` returns `ready: true` when the
  `workspace_integrations` row exists, with `connectionId` query parameter
  omitted entirely.
- `GET .../integrations/:provider/status?connectionId=:workspaceId` returns
  `ready: true` when the row exists.
- `GET .../integrations/:provider/status` returns `ready: false` before the
  Nango webhook has upserted the row.
- `DELETE .../integrations/:provider/status` removes the row and is
  idempotent.
- Auth negative cases:
  - No bearer → 401 on `connect-session`, status, and DELETE.
  - Relayfile JWT whose `wks` does not match path `:workspaceId` → 403 on
    all three.
  - Cloud session for a user without access to the workspace → 403.

The pre-existing `tests/app-path.test.ts` and
`tests/cli/default-api-url.test.ts` remain green; the latter is extended
only if needed to cover the SDK default URL.

## 6. Required E2E proof

The E2E harness MUST exercise the SDK as a *consumer*, not against source.

Required steps:

1. `npm run build --workspace=packages/sdk/typescript`.
2. `npm pack` the built `@relayfile/sdk` artifact.
3. Create a temporary consumer project (e.g. under `os.tmpdir()`),
   `npm install` the packed tarball into it, and write a Node script that
   imports `RelayfileSetup` from the installed package — never from
   `packages/sdk/typescript/src/...`.
4. Stand up a mock cloud server and a mock relayfile server in the same
   process (or as child processes); the SDK is configured to point at them.
5. Run the script and assert end-to-end behavior.

The script MUST exercise:

- `createWorkspace()` happy path.
- `joinWorkspace()` happy path.
- `handle.client().listTree()` against the mock relayfile server.
- `connectIntegration()` already-connected path
  (`{ alreadyConnected: true }`).
- `connectIntegration()` OAuth-required path (returns
  `connectLink` + `connectionId`).
- `connectNotion()` returns a Notion-only connect link.
- `mountEnv()` exposes relayfile mount env vars and relaycast env vars.
- `agentInvite()` serializes relayfile and relaycast credentials for a
  second trusted agent.
- `waitForConnection()` delayed success (mock cloud flips `ready` from
  `false` to `true` after N polls).
- `isConnected()` returning `false` then `true`.
- `disconnectIntegration()` calling `DELETE` on the status route.
- `waitForConnection()` timeout → `CloudTimeoutError`.
- `waitForConnection()` aborted via `AbortSignal` → `CloudAbortError`.
- Malformed cloud response (e.g. non-JSON body, missing required fields) →
  `CloudApiError` with a useful message.
- Cloud HTTP 500 on connect-session → `CloudApiError`.
- Token refresh: force the handle's cached token age past the 55-minute
  threshold, trigger a relayfile call, assert that the SDK rejoined and
  used the new token.

The script MUST assert the actual HTTP paths the mock servers received:

- `POST /api/v1/workspaces`
- `POST /api/v1/workspaces/:workspaceId/join`
- `POST /api/v1/workspaces/:workspaceId/integrations/connect-session`
- `GET /api/v1/workspaces/:workspaceId/integrations/:provider/status`
- `DELETE /api/v1/workspaces/:workspaceId/integrations/:provider/status`
- `GET /v1/workspaces/:workspaceId/fs/tree` (relayfile server)

Path assertions are non-negotiable: they prove the SDK is preserving the
`/cloud` base path and the v1 API surface.

## 7. Regression gates

All gates below must pass before the work is considered complete. If a
single gate is skipped, the workflow MUST record a concrete pre-existing
unrelated failure with a link to the failing job; otherwise treat any
failure as blocking.

### Relayfile

- `npm run typecheck --workspace=packages/sdk/typescript`
- `npm run test --workspace=packages/sdk/typescript`
- `npm run build --workspace=packages/sdk/typescript`
- The new package-consumer E2E command (e.g.
  `npm run e2e:setup --workspace=packages/sdk/typescript` or an explicit
  `node packages/sdk/typescript/scripts/sdk-setup-e2e.mjs`).

### Cloud

- `npx tsx --test tests/sdk-setup-client-routes.test.ts tests/app-path.test.ts tests/cli/default-api-url.test.ts`
- `npm run typecheck`
- `npm test` — unless the workflow records a concrete pre-existing
  unrelated failure (with stable failure signature and ticket reference);
  otherwise this gate is blocking.

## 8. Commit policy

- Relayfile and cloud changes are committed **separately**, in their
  respective repos.
- Each commit lists explicit file paths in `git add` (no `git add -A`,
  no `git add .`).
- Do not stage unrelated dirty files. The repo may already contain unstaged
  changes from prior work (`.trajectories/`, `workflows/`, etc.); these
  must not be swept into the SDK or cloud commits.
- Each commit message states the scope and references this acceptance
  contract.

ACCEPTANCE_CONTRACT_READY
