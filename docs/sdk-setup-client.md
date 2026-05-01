# SDK Setup Client

**Status:** Implemented (SDK v0.6.0)  
**Affects:** `packages/sdk/typescript`, `cloud/packages/web`

---

## Problem

Using relayfile today requires understanding at least four separate systems before writing a single line of product code: the cloud web API (workspace creation, join tokens), relayauth (JWT issuance), Nango (OAuth connect sessions), and the relayfile VFS API. None of these steps are exposed through the SDK — they live in the cloud web app, accessed through a browser dashboard or undocumented HTTP calls.

The result is that embedding relayfile in an agent sandbox requires either:
- Hand-rolling HTTP calls against `https://agentrelay.com/cloud/api/v1/...` with no type safety, or
- Copying setup code from examples that have no official contract.

The existing `RelayFileClient` assumes you already have a workspace ID and a valid JWT. It does not know how to get either.

---

## Goal

A user running an agent in any sandbox environment (Daytona, E2B, Docker, local) should be able to get from zero to a fully-mounted, integration-connected relayfile workspace in a few lines of TypeScript, with no browser interaction required for the common case (PAT/token-based auth), and a single URL to visit for OAuth-based auth.

```ts
import { RelayfileSetup } from '@relayfile/sdk'

const setup = new RelayfileSetup()

const workspace = await setup.createWorkspace({ name: 'my-agent' })

const { connectLink } = await workspace.connectIntegration('github', {
  connectionId: process.env.GITHUB_CONNECTION_ID,
})
if (connectLink) {
  console.log('Authorize GitHub:', connectLink)
  await workspace.waitForConnection('github', {
    connectionId: process.env.GITHUB_CONNECTION_ID,
  })
}

const client = workspace.client()
const tree = await client.listTree(workspace.workspaceId, { path: '/github' })
```

The one-agent workspace path should be even smaller for the most common
OAuth-backed workspace flow. An agent can create or join a workspace, hand the
human one Notion authorization URL, wait for the webhook-confirmed connection,
then pass one env block or invite payload to every other process that needs the
same relayfile filesystem plus relaycast room.

```ts
import { RelayfileSetup } from '@relayfile/sdk'

const setup = new RelayfileSetup({ accessToken: process.env.RELAY_ACCESS_TOKEN })
const workspace = await setup.createWorkspace({
  name: 'notion-research-room',
  agentName: 'lead-agent',
})

const notion = await workspace.connectNotion()
if (notion.connectLink) {
  console.log(`Connect Notion: ${notion.connectLink}`)
  await workspace.waitForNotion()
}

// Use these vars to launch relayfile-mount locally or inside a cloud sandbox.
const mountEnv = workspace.mountEnv({
  localDir: '/workspace/notion',
  remotePath: '/notion',
})

// Send this secret payload to another trusted agent so it can mount relayfile
// and join the same relaycast workspace.
const reviewerInvite = workspace.agentInvite({ agentName: 'review-agent' })
```

---

## Golden Path

The setup-client is the primitive contract. For the full agent workflow — connecting Notion, mounting the workspace, and inviting other agents — see [`docs/agent-workspace-golden-path.md`](./agent-workspace-golden-path.md). That doc composes the primitives here into a single end-to-end journey, records the v1 decisions, and links to the E2E evidence. Run `npm run demo:agent-workspace --workspace=packages/sdk/typescript` to see it locally.

---

## Scope

This spec covers:

1. A new `RelayfileSetup` class added to `@relayfile/sdk` that wraps the cloud web API
2. A `WorkspaceHandle` class returned from setup that provides both setup operations and a ready `RelayFileClient`
3. New cloud web API endpoints required to support polling for connection status without requiring full auth
4. The `waitForConnection` polling contract
5. Error types and error handling
6. Token refresh and TTL handling
7. What is explicitly out of scope

This spec does **not** cover: self-hosted server setup, the Go server, Python SDK changes, or changes to any existing `RelayFileClient` methods.

---

## Architecture

### Layers

```
┌──────────────────────────────────────────────────────┐
│  User code                                           │
│  new RelayfileSetup() → workspace.client()           │
└────────────────────┬─────────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────────┐
│  @relayfile/sdk                                      │
│  RelayfileSetup         — cloud web API calls        │
│  WorkspaceHandle        — workspace state + client   │
│  RelayFileClient        — VFS API calls (unchanged)  │
└────────────────────┬─────────────────────────────────┘
                     │
          ┌──────────┴──────────┐
          │                     │
┌─────────▼──────────┐  ┌──────▼────────────────────┐
│  cloud web API      │  │  relayfile VFS (CF Worker) │
│  agentrelay.com      │  │  relayfile.agent-relay.com │
│  /cloud/api/v1/...   │  │  /v1/workspaces/{id}/fs/…  │
└─────────────────────┘  └───────────────────────────┘
          │
          │  (internally calls)
          │
┌─────────▼────────────────────────────────────────────┐
│  relayauth  ·  Nango  ·  Relaycast  ·  Postgres      │
└──────────────────────────────────────────────────────┘
```

The SDK calls the cloud web API for all setup operations. The cloud web API is the single surface that owns workspace lifecycle, token issuance, and Nango session management. The SDK never calls relayauth, Nango, or Relaycast directly.

### Cloud web API base URL

`RelayfileSetup` defaults to `https://agentrelay.com/cloud`. This is overridable via constructor option for staging/dev environments.

---

## New SDK Exports

### `RelayfileSetup`

The entry point. Stateless — creates workspaces and returns handles.

```ts
export interface RelayfileSetupOptions {
  /**
   * Base URL for the cloud web API.
   * @default "https://agentrelay.com/cloud"
   */
  cloudApiUrl?: string

  /**
   * Cloud API access token (cld_at_... bearer token).
   * When omitted, workspace creation and join calls proceed anonymously.
   * Anonymous workspaces are publicly joinable — use only for ephemeral sandboxes.
   */
  accessToken?: string | (() => string | Promise<string>)

  /**
   * Timeout in milliseconds for each HTTP request to the cloud web API.
   * @default 30_000
   */
  requestTimeoutMs?: number

  /**
   * Retry configuration for cloud web API calls.
   * @default { maxRetries: 3, baseDelayMs: 500 }
   */
  retry?: {
    maxRetries: number
    baseDelayMs: number
  }
}

export class RelayfileSetup {
  constructor(options?: RelayfileSetupOptions)

  /**
   * Create a new workspace and return a WorkspaceHandle.
   * This calls POST /api/v1/workspaces, then POST /api/v1/workspaces/:id/join
   * to obtain a relayfile JWT. Both calls happen in a single method so
   * the caller gets a fully ready handle.
   */
  createWorkspace(options?: CreateWorkspaceOptions): Promise<WorkspaceHandle>

  /**
   * Rejoin an existing workspace. Fetches a fresh relayfile JWT.
   * Use this when you already have a workspaceId from a previous session.
   */
  joinWorkspace(
    workspaceId: string,
    options?: JoinWorkspaceOptions,
  ): Promise<WorkspaceHandle>
}
```

---

### `CreateWorkspaceOptions`

```ts
export interface CreateWorkspaceOptions {
  /**
   * Human-readable name for the workspace, shown in the dashboard.
   * If omitted, the cloud assigns a generated name.
   */
  name?: string

  /**
   * ACL permissions to apply to the workspace.
   * Mirrors the WorkspacePermissions shape from the cloud web API.
   */
  permissions?: {
    /** Glob patterns for paths that are read-only for all agents. */
    readonly?: string[]
    /** Glob patterns for paths that are invisible to all agents. */
    ignored?: string[]
  }

  /**
   * Agent name to use when joining the workspace immediately after creation.
   * @default "sdk-agent"
   */
  agentName?: string

  /**
   * Relayfile JWT scopes to request for the agent token.
   * @default ["fs:read", "fs:write"]
   */
  scopes?: string[]
}
```

---

### `JoinWorkspaceOptions`

```ts
export interface JoinWorkspaceOptions {
  /**
   * Agent name for this join session.
   * @default "sdk-agent"
   */
  agentName?: string

  /**
   * Relayfile JWT scopes to request.
   * @default ["fs:read", "fs:write"]
   */
  scopes?: string[]

  /**
   * ACL permission overrides for this agent.
   * Merged with the workspace-level permissions.
   */
  permissions?: {
    readonly?: string[]
    ignored?: string[]
  }
}
```

---

### `WorkspaceHandle`

Returned by `createWorkspace` and `joinWorkspace`. Holds workspace state and exposes all integration setup operations.

```ts
export interface WorkspaceInfo {
  workspaceId: string
  relayfileUrl: string
  relaycastApiKey: string
  /** ISO 8601 creation timestamp */
  createdAt: string
  name?: string
}

export class WorkspaceHandle {
  /** Resolved workspace metadata */
  readonly info: WorkspaceInfo

  /** Shorthand for info.workspaceId */
  readonly workspaceId: string

  /**
   * Get a RelayFileClient scoped to this workspace.
   * The client's token is managed internally and refreshed automatically
   * when the underlying join token approaches expiry (TTL < 5 minutes remaining).
   */
  client(): RelayFileClient

  /**
   * Request a Nango OAuth connect session for an integration.
   *
   * If the integration is already connected (a Nango connection exists for
   * the given connectionId), returns { connectLink: null, alreadyConnected: true }.
   *
   * Otherwise, creates a Nango connect session and returns the connectLink URL
   * that the user must visit to authorize the integration.
   *
   * The connectionId is stored internally on the handle and used by
   * waitForConnection().
   */
  connectIntegration(
    provider: WorkspaceIntegrationProvider,
    options?: ConnectIntegrationOptions,
  ): Promise<ConnectIntegrationResult>

  /**
   * Convenience wrapper for connectIntegration('notion') that returns the
   * single URL a human should open to connect Notion.
   */
  connectNotion(
    options?: Omit<ConnectIntegrationOptions, 'allowedIntegrations'>,
  ): Promise<ConnectIntegrationResult>

  /**
   * Poll until a previously requested integration connection is confirmed active,
   * or until the timeout is reached.
   *
   * Internally polls GET /api/v1/workspaces/:id/integrations/:provider/status
   * with the connectionId from the most recent connectIntegration() call for
   * this provider (or the connectionId provided in options).
   *
   * Resolves when { ready: true } is returned.
   * Rejects with IntegrationConnectionTimeoutError if timeout is reached.
   */
  waitForConnection(
    provider: WorkspaceIntegrationProvider,
    options?: WaitForConnectionOptions,
  ): Promise<void>

  /** Convenience wrapper for waitForConnection('notion'). */
  waitForNotion(options?: WaitForConnectionOptions): Promise<void>

  /**
   * Check whether an integration is currently connected.
   * Returns true if the cloud reports ready: true for the given connectionId.
   */
  isConnected(
    provider: WorkspaceIntegrationProvider,
    connectionId: string,
  ): Promise<boolean>

  /**
   * Remove an integration connection.
   * Calls DELETE /api/v1/workspaces/:id/integrations/:provider/status
   */
  disconnectIntegration(
    provider: WorkspaceIntegrationProvider,
    connectionId: string,
  ): Promise<void>

  /**
   * Get the raw relayfile JWT currently held by this handle.
   * Useful for passing to other processes that need to call the VFS API.
   * The token is refreshed automatically by client() but this method
   * always returns the current token synchronously.
   */
  getToken(): string

  /**
   * Build the environment needed by relayfile-mount and relaycast-aware agents.
   * Values include RELAYFILE_BASE_URL, RELAYFILE_TOKEN, RELAYFILE_WORKSPACE,
   * RELAYFILE_REMOTE_PATH, RELAY_API_KEY, and RELAY_BASE_URL.
   */
  mountEnv(options?: WorkspaceMountEnvOptions): WorkspaceMountEnv

  /**
   * Build a serializable, secret invite for another trusted agent. The invite
   * includes relayfile workspace details and relaycast credentials; by default
   * it also includes the current relayfile JWT so the receiving agent can mount
   * the workspace immediately.
   */
  agentInvite(options?: AgentWorkspaceInviteOptions): AgentWorkspaceInvite

  /**
   * Refresh the relayfile JWT by re-joining the workspace.
   * Called automatically by client() when token is near expiry,
   * but can be called manually if needed.
   */
  refreshToken(): Promise<void>
}
```

---

### `ConnectIntegrationOptions`

```ts
export interface ConnectIntegrationOptions {
  /**
   * The Nango connection ID to use for this provider.
   *
   * Nango connection IDs are opaque strings scoped to a Nango account.
   * For relayfile.dev, the cloud manages Nango and assigns connection IDs
   * on the user's behalf. You do not need to know the Nango account details.
   *
   * If you pass a connectionId here, it is used as the identifier to check
   * status against. If omitted, the cloud assigns one from the Nango session.
   *
   * In practice, most users will omit this field. It exists for cases where
   * a connection was previously established and you want to verify it or
   * reuse it without going through the OAuth flow again.
   */
  connectionId?: string

  /**
   * Restrict the Nango connect session to this list of integrations.
   * If omitted, all integrations supported for this provider are allowed.
   */
  allowedIntegrations?: string[]
}

export interface ConnectIntegrationResult {
  /**
   * The Nango-hosted OAuth connect URL. The user must visit this URL to
   * authorize the integration. It is a one-time session URL that expires
   * (typically within 10 minutes).
   *
   * Null if alreadyConnected is true.
   */
  connectLink: string | null

  /**
   * The Nango connect session token (the short-lived token that identifies
   * the session on the Nango side). Not needed for most use cases.
   */
  sessionToken: string | null

  /**
   * ISO 8601 expiry timestamp for the connect session.
   * Null if alreadyConnected is true.
   */
  expiresAt: string | null

  /**
   * True if the integration was already connected for this workspace
   * and no new OAuth flow is needed.
   */
  alreadyConnected: boolean

  /**
   * The connectionId that will be used when polling for connection status.
   * Store this if you need to call waitForConnection() from a different
   * process or later in execution.
   */
  connectionId: string
}
```

---

### `WaitForConnectionOptions`

```ts
export interface WaitForConnectionOptions {
  /**
   * The connection ID to poll for. Defaults to the most recently returned
   * connectionId from connectIntegration() for this provider.
   */
  connectionId?: string

  /**
   * How often to poll the status endpoint, in milliseconds.
   * @default 2_000
   */
  pollIntervalMs?: number

  /**
   * Maximum total time to wait for the connection to become active.
   * @default 300_000 (5 minutes)
   */
  timeoutMs?: number

  /**
   * AbortSignal for cancellation.
   */
  signal?: AbortSignal

  /**
   * Called on each poll iteration. Useful for progress indicators.
   */
  onPoll?: (elapsed: number) => void
}
```

---

### `WorkspaceMountEnvOptions`

```ts
export interface WorkspaceMountEnvOptions {
  /** Local directory for relayfile-mount, for example /workspace/notion. */
  localDir?: string

  /** Remote relayfile path to mount. Defaults to /. */
  remotePath?: string

  /** relayfile-mount mode. */
  mode?: 'poll' | 'fuse'

  /** Override for non-production relaycast deployments. */
  relaycastBaseUrl?: string
}

export type WorkspaceMountEnv = Record<string, string>
```

`mountEnv()` returns `RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`,
`RELAYFILE_WORKSPACE`, `RELAYFILE_REMOTE_PATH`, `RELAY_API_KEY`,
`RELAYCAST_API_KEY`, `RELAY_BASE_URL`, and `RELAYCAST_BASE_URL`, plus optional
mount fields when provided. The result is intentionally shaped as environment
variables so a host agent can pass it directly to a local process, a cloud
sandbox, or `relayfile-mount`.

---

### `AgentWorkspaceInviteOptions`

```ts
export interface AgentWorkspaceInviteOptions {
  agentName?: string
  scopes?: string[]
  relaycastBaseUrl?: string

  /**
   * Include the current relayfile JWT. Defaults to true for the easiest
   * trusted-agent handoff. Set false when an agent should join separately.
   */
  includeRelayfileToken?: boolean
}

export interface AgentWorkspaceInvite {
  workspaceId: string
  cloudApiUrl: string
  relayfileUrl: string
  relaycastApiKey: string
  relaycastBaseUrl: string
  agentName: string
  scopes: string[]
  relayfileToken?: string
  createdAt?: string
  name?: string
}
```

The invite is secret credential material. It is meant for trusted agents under
the same user or workflow so they can both join the relaycast workspace and work
against the same relayfile filesystem.

---

### `WorkspaceIntegrationProvider` type

```ts
export type WorkspaceIntegrationProvider =
  | 'github'
  | 'notion'
  | 'linear'
  | 'slack'
  | 'slack-sage'
  | 'slack-my-senior-dev'
  | 'slack-nightcto'
```

This mirrors the `WORKSPACE_INTEGRATION_PROVIDERS` constant in the cloud web app. The SDK should export this type and a corresponding const array for runtime validation.

---

## Error Types

All errors extend a base `RelayfileSetupError`:

```ts
export class RelayfileSetupError extends Error {
  readonly code: string
}

/**
 * The cloud web API returned a non-2xx response.
 * HTTP 4xx and 5xx responses from /api/v1/workspaces/... or
 * /api/v1/workspaces/:id/integrations/... land here.
 */
export class CloudApiError extends RelayfileSetupError {
  readonly code = 'cloud_api_error'
  readonly httpStatus: number
  readonly httpBody: unknown
}

/**
 * Workspace creation or join succeeded but the response was missing
 * a required field (workspaceId, token, relayfileUrl, etc.).
 * Should not happen in production — indicates a cloud API contract violation.
 */
export class MalformedCloudResponseError extends RelayfileSetupError {
  readonly code = 'malformed_cloud_response'
  readonly field: string
  readonly response: unknown
}

/**
 * waitForConnection() reached its timeout without seeing { ready: true }.
 * The OAuth flow may not have been completed, or the Nango webhook may
 * be delayed.
 */
export class IntegrationConnectionTimeoutError extends RelayfileSetupError {
  readonly code = 'integration_connection_timeout'
  readonly provider: WorkspaceIntegrationProvider
  readonly connectionId: string
  readonly elapsedMs: number
  readonly timeoutMs: number
}

/**
 * An unknown or unsupported provider was passed to connectIntegration()
 * or waitForConnection().
 */
export class UnknownProviderError extends RelayfileSetupError {
  readonly code = 'unknown_provider'
  readonly provider: string
}

/**
 * waitForConnection() or isConnected() was called without a connectionId
 * and no prior connectIntegration() call was made for this provider on
 * this handle.
 */
export class MissingConnectionIdError extends RelayfileSetupError {
  readonly code = 'missing_connection_id'
  readonly provider: WorkspaceIntegrationProvider
}
```

---

## Internal Implementation

### `RelayfileSetup.createWorkspace()`

Sequence:

1. `POST {cloudApiUrl}/api/v1/workspaces` with body `{ name?, permissions? }`  
   — Auth header: `Authorization: Bearer {accessToken}` if provided, else anonymous  
   — Returns: `{ workspaceId, relaycastApiKey, relayfileUrl, relayauthUrl, createdAt, name? }`

2. `POST {cloudApiUrl}/api/v1/workspaces/{workspaceId}/join` with body `{ agentName, scopes?, permissions? }`  
   — Auth header: same as step 1  
   — Returns: `{ workspaceId, token, relayfileUrl, wsUrl, relaycastApiKey }`

3. Construct and return a `WorkspaceHandle` with:
   - `info`: merged from steps 1 and 2
   - `_token`: the JWT from step 2
   - `_tokenIssuedAt`: `Date.now()`
   - `_setup`: reference to the `RelayfileSetup` instance (for token refresh)

Token TTL: the relayfile JWT issued by the join endpoint has a 1-hour TTL (set by relayauth). The handle tracks issuance time and triggers a re-join when fewer than 5 minutes remain.

### `RelayfileSetup.joinWorkspace()`

Sequence:

1. `POST {cloudApiUrl}/api/v1/workspaces/{workspaceId}/join` with body `{ agentName, scopes?, permissions? }`

2. Construct and return a `WorkspaceHandle`.

Note: `joinWorkspace` does not call `GET /api/v1/workspaces/{workspaceId}` to fetch workspace metadata by default — it trusts the join response, which includes `relayfileUrl` and `relaycastApiKey`. If the caller needs `createdAt` or `name`, they should call `GET /api/v1/workspaces/{workspaceId}` separately (this is not wrapped by the SDK in v1 — see Future Work).

### `WorkspaceHandle.client()`

Returns a `RelayFileClient` instance constructed with:

```ts
new RelayFileClient({
  baseUrl: this.info.relayfileUrl,
  token: () => this._getOrRefreshToken(),
})
```

`_getOrRefreshToken()` is an internal async method that:
1. Checks if `Date.now() - _tokenIssuedAt > (60 * 60 * 1000) - (5 * 60 * 1000)` (55 minutes elapsed)
2. If yes: calls `refreshToken()` to re-join and update `_token` and `_tokenIssuedAt`
3. Returns `_token`

The `RelayFileClient` already accepts `token: () => Promise<string>`, so this integrates cleanly with the existing client without modification.

`client()` is synchronous and returns the same `RelayFileClient` instance on repeated calls (singleton per handle). The token factory is what provides freshness.

### `WorkspaceHandle.connectIntegration()`

Sequence:

1. If `options.connectionId` is provided, call `isConnected(provider, connectionId)` first. If true, return `{ connectLink: null, alreadyConnected: true, connectionId, sessionToken: null, expiresAt: null }`.

2. `POST {cloudApiUrl}/api/v1/workspaces/{workspaceId}/integrations/connect-session`  
   — Body: `{ allowedIntegrations?: string[] }` (mapped from provider to Nango config keys)  
   — Auth: `Authorization: Bearer {relayfileJwt}` from the workspace handle. If an `accessToken` was supplied to `RelayfileSetup`, it is used for workspace create/join calls, but setup operations on an existing handle use the handle's workspace-scoped relayfile JWT so anonymous SDK-created workspaces can also connect integrations.  
   — Returns: `{ token, expiresAt, connectLink, connectionId? }`. When the cloud cannot know the final Nango connection ID at session creation time, the SDK uses `workspaceId` as the polling key and the cloud status endpoint treats an omitted `connectionId` or `connectionId=workspaceId` as "ready when a provider row exists".

3. Store `{ connectionId, provider }` on the handle in a `_pendingConnections: Map<provider, string>` for use by `waitForConnection()`.

4. Return `ConnectIntegrationResult`.

**Provider → Nango config key mapping** in the SDK:

The SDK needs to know which Nango integration names to pass as `allowedIntegrations`. This mapping is the same as `DEFAULT_PROVIDER_CONFIG_KEYS` in the cloud, and must be kept in sync:

```ts
const PROVIDER_CONFIG_KEYS: Record<WorkspaceIntegrationProvider, string> = {
  'github': 'github-sage',
  'notion': 'notion-sage',
  'linear': 'linear-sage',
  'slack': 'slack-sage',
  'slack-sage': 'slack-sage',
  'slack-my-senior-dev': 'slack-my-senior-dev',
  'slack-nightcto': 'slack-nightcto',
}
```

This is a private implementation detail of the SDK and is not exported. If the cloud changes config keys via env var overrides, the SDK's defaults may be wrong — see Cloud API Changes section for how to address this.

### `WorkspaceHandle.waitForConnection()`

Polling loop:

```
start = Date.now()
connectionId = options.connectionId ?? _pendingConnections.get(provider) ?? throw MissingConnectionIdError
loop:
  elapsed = Date.now() - start
  if elapsed >= timeoutMs: throw IntegrationConnectionTimeoutError
  if signal?.aborted: throw AbortError
  options.onPoll?.(elapsed)
  result = GET /api/v1/workspaces/{id}/integrations/{provider}/status?connectionId={connectionId}
  if result.ready: return
  await sleep(pollIntervalMs)
```

HTTP errors during polling:
- **401 / 403**: rethrow as `CloudApiError` immediately (do not retry — likely a session expiry problem)
- **404**: rethrow as `CloudApiError` immediately (workspace or provider not found)
- **429**: honor `Retry-After` header, sleep, then retry
- **5xx**: retry up to 3 times with backoff, then rethrow

### `WorkspaceHandle.getToken()` and `WorkspaceHandle.refreshToken()`

`getToken()` returns `_token` synchronously. It does not trigger a refresh.

`refreshToken()` re-calls `POST /api/v1/workspaces/{workspaceId}/join` with the same options that were used during the original join, then updates `_token` and `_tokenIssuedAt`.

The join options (`agentName`, `scopes`, `permissions`) are stored on the handle at construction time for this purpose.

---

## Cloud API Changes Required

The current cloud web API has one gap that blocks this SDK from working fully.

### 1. Auth requirement on `/integrations/connect-session`

The current `POST /api/v1/workspaces/:workspaceId/integrations/connect-session` endpoint requires full authenticated session or workspace-scoped token access (checked via `resolveRequestAuth`). Anonymous workspace handles — which are the default for SDK-created workspaces without an `accessToken` — cannot call this endpoint.

**Required change:** The connect-session endpoint should accept the relayfile JWT (the `token` returned by the join endpoint) as a valid bearer token for workspace-scoped operations on that workspace. The relayfile JWT already contains `wks` (workspace ID) as a claim. The cloud web app's `resolveRequestAuth` should be extended to recognize and validate relayfile JWTs in addition to cloud `cld_at_` tokens and session cookies.

Alternatively, the workspace join token itself (a `cld_at_` scoped to that workspace) could be used. The join endpoint already returns a relayfile JWT; the cloud could return a short-lived cloud-scoped workspace token alongside it. This is a lighter change but adds a field to the join response.

**Recommended approach:** Extend `resolveRequestAuth` to accept relayfile JWTs. The cloud already has the relayauth JWKS URL configured — validating a relayfile JWT is the same RS256 verification it already performs for workspace-level relayfile ACL operations.

### 2. Connection ID in connect-session response

The current `POST /api/v1/workspaces/:workspaceId/integrations/connect-session` response returns `{ token, expiresAt, connectLink }` from Nango. It does not return a `connectionId` that the client can use to poll status.

The Nango connect session token identifies the session, but the actual Nango `connectionId` is determined when the user completes OAuth. At session creation time, the `connectionId` is not yet known.

**Two options:**

**Option A — workspace-scoped connection ID (recommended):** Use `workspaceId` as the Nango `end_user.id` (already the case in `nango-service.ts`). The Nango connection for the workspace is identified by the combination of `workspaceId` + `providerConfigKey`. After OAuth completes, the cloud's Nango webhook handler stores the `connectionId` in the `workspace_integrations` DB table. The SDK can poll `GET /integrations/:provider/status?connectionId=<workspaceId>` using the workspace ID as the connection ID.

This requires the cloud to ensure that `getWorkspaceIntegration()` uses `workspaceId` as the expected connection ID.

**Option B — return expected connection ID in session response:** The cloud generates the `connectionId` upfront (e.g., `${workspaceId}-${provider}`) and passes it to Nango as the expected connection ID. The session response then includes `connectionId`. The SDK stores this and uses it for polling.

Option A requires the least new code. Option B is more explicit. This spec recommends Option A for now and Option B as a future improvement once Nango's connect session API supports pre-specifying connection IDs.

### 3. Status endpoint — no `connectionId` required when workspaceId is the connectionId

If Option A above is adopted, the `GET /integrations/:provider/status` endpoint's `connectionId` query parameter should also accept the workspace ID as a valid value (or become optional, defaulting to the workspace ID). Currently the endpoint returns 400 if `connectionId` is empty.

**Required change:** Make `connectionId` optional on the status endpoint. When omitted, use `workspaceId` as the expected connection ID.

---

## Token Handling Summary

| Token type | Where issued | Used for | TTL | How SDK gets it |
|---|---|---|---|---|
| `cld_at_...` cloud token | `/api/v1/cli/login` OAuth flow | Authenticating to cloud web API | 24h | Passed by user as `accessToken` option |
| Relayfile JWT | `/api/v1/workspaces/:id/join` | VFS API calls | 1h | Returned by `createWorkspace` / `joinWorkspace` |

The SDK does not issue or refresh `cld_at_` tokens. If the user provides an `accessToken` and it expires, cloud API calls will return 401 and the SDK will throw `CloudApiError`. The user is responsible for refreshing their cloud token.

The SDK automatically refreshes the relayfile JWT by re-joining (no `cld_at_` token required — the join endpoint accepts anonymous requests for anonymous workspaces, and workspace-owner requests for authenticated workspaces).

For authenticated workspaces (`accessToken` provided), the re-join call uses the same `accessToken`. If the `accessToken` has expired by the time re-join is attempted, the SDK throws `CloudApiError` with status 401, which the user should handle by providing a fresh token.

---

## Usage Examples

### Ephemeral sandbox — anonymous, no OAuth (file-based state)

```ts
import { RelayfileSetup } from '@relayfile/sdk'
import { writeFileSync, readFileSync } from 'node:fs'

const setup = new RelayfileSetup()

let workspaceId: string | undefined
try {
  workspaceId = readFileSync('/tmp/.relayfile-workspace-id', 'utf8').trim()
} catch {}

const workspace = workspaceId
  ? await setup.joinWorkspace(workspaceId, { agentName: 'my-agent' })
  : await setup.createWorkspace({ name: 'my-agent', agentName: 'my-agent' })

if (!workspaceId) {
  writeFileSync('/tmp/.relayfile-workspace-id', workspace.workspaceId)
}

const client = workspace.client()
```

### Connect GitHub (OAuth flow)

```ts
import { RelayfileSetup, IntegrationConnectionTimeoutError } from '@relayfile/sdk'

const setup = new RelayfileSetup({ accessToken: process.env.RELAY_ACCESS_TOKEN })
const workspace = await setup.createWorkspace({ name: 'github-agent' })

const result = await workspace.connectIntegration('github')

if (!result.alreadyConnected) {
  console.log(`\nAuthorize GitHub access:\n\n  ${result.connectLink}\n`)
  try {
    await workspace.waitForConnection('github', {
      timeoutMs: 5 * 60 * 1000,
      onPoll: (elapsed) => {
        process.stdout.write(`\rWaiting for GitHub authorization... ${Math.round(elapsed / 1000)}s`)
      },
    })
    console.log('\nConnected.')
  } catch (err) {
    if (err instanceof IntegrationConnectionTimeoutError) {
      console.error('Timed out waiting for GitHub authorization.')
      process.exit(1)
    }
    throw err
  }
}

const client = workspace.client()
const tree = await client.listTree(workspace.workspaceId, { path: '/github' })
```

### Multiple integrations

```ts
const workspace = await setup.createWorkspace({ name: 'multi-agent' })

const [githubResult, notionResult] = await Promise.all([
  workspace.connectIntegration('github'),
  workspace.connectIntegration('notion'),
])

const pendingLinks = [githubResult, notionResult]
  .filter(r => !r.alreadyConnected)
  .map(r => r.connectLink)

if (pendingLinks.length > 0) {
  console.log('Authorize the following integrations:')
  pendingLinks.forEach(link => console.log(' ', link))

  await Promise.all([
    workspace.waitForConnection('github'),
    workspace.waitForConnection('notion'),
  ])
}

const client = workspace.client()
```

### Long-running process with token refresh

```ts
const workspace = await setup.createWorkspace({ name: 'long-runner' })
const client = workspace.client()

// client() uses a token factory that auto-refreshes the relayfile JWT
// when < 5 minutes remain. No manual refresh needed in normal operation.
// The underlying RelayFileClient handles this transparently.

setInterval(async () => {
  const tree = await client.listTree(workspace.workspaceId, { path: '/' })
  console.log('Files:', tree.entries.length)
}, 60_000)
```

### Staging environment

```ts
const setup = new RelayfileSetup({
  cloudApiUrl: 'https://staging.agentrelay.com',
  accessToken: process.env.STAGING_ACCESS_TOKEN,
})
const workspace = await setup.createWorkspace({ name: 'staging-test' })
```

---

## File Location in `@relayfile/sdk`

```
packages/sdk/typescript/src/
├── setup.ts           ← RelayfileSetup, WorkspaceHandle (new)
├── setup-types.ts     ← All setup-related types/interfaces (new)
├── setup-errors.ts    ← RelayfileSetupError and subclasses (new)
├── client.ts          ← RelayFileClient (unchanged)
├── types.ts           ← VFS types (unchanged)
├── index.ts           ← Add exports from setup.ts, setup-types.ts, setup-errors.ts
└── ...
```

New exports added to `index.ts`:

```ts
export { RelayfileSetup, WorkspaceHandle } from './setup.js'
export { WORKSPACE_INTEGRATION_PROVIDERS } from './setup-types.js'
export type {
  RelayfileSetupOptions,
  CreateWorkspaceOptions,
  JoinWorkspaceOptions,
  ConnectIntegrationOptions,
  ConnectIntegrationResult,
  WaitForConnectionOptions,
  WorkspaceInfo,
  WorkspaceIntegrationProvider,
} from './setup-types.js'
export {
  RelayfileSetupError,
  CloudApiError,
  MalformedCloudResponseError,
  IntegrationConnectionTimeoutError,
  UnknownProviderError,
  MissingConnectionIdError,
} from './setup-errors.js'
```

---

## Testing

### Unit tests (`setup.test.ts`)

Use `msw` (or inline `fetch` mocking with `vi.stubGlobal`) to mock the cloud web API. Test cases:

- `createWorkspace()` — happy path: calls POST workspaces then POST join; returns WorkspaceHandle with correct info
- `createWorkspace()` — cloud API 500: throws `CloudApiError` with httpStatus 500
- `createWorkspace()` — malformed response (missing workspaceId): throws `MalformedCloudResponseError`
- `joinWorkspace()` — happy path: calls only POST join
- `connectIntegration()` — already connected: returns `alreadyConnected: true`, no connect-session call
- `connectIntegration()` — not connected: calls connect-session, returns connectLink
- `waitForConnection()` — resolves on first poll that returns `{ ready: true }`
- `waitForConnection()` — resolves after multiple polls
- `waitForConnection()` — throws `IntegrationConnectionTimeoutError` after timeout
- `waitForConnection()` — throws on abort signal
- `waitForConnection()` — throws `MissingConnectionIdError` when no prior connectIntegration call
- `waitForConnection()` — retries on 429 with Retry-After
- `waitForConnection()` — throws immediately on 401
- `client()` — returns same RelayFileClient instance on repeated calls
- `getToken()` / `refreshToken()` — token refresh logic
- Token auto-refresh — `client()` token factory calls refreshToken when token is near expiry

### Integration tests

Not in scope for the initial implementation. The setup client depends on the cloud web API which is not available in the relayfile repo's test environment.

---

## HTTP Request Details

All requests from `RelayfileSetup` and `WorkspaceHandle` to the cloud web API:

- Use `fetch` (Node.js 18+ native)
- Set `Content-Type: application/json` on POST requests
- Set `Authorization: Bearer {accessToken}` for setup-level calls when `accessToken` is provided
- Set `Authorization: Bearer {relayfileJwt}` for workspace-handle setup calls such as `connectIntegration`, `waitForConnection`, `isConnected`, and `disconnectIntegration`
- Set `X-Relayfile-SDK-Version: {version}` header from the SDK version constant, kept in sync with `package.json`
- Apply exponential backoff with jitter on 429 and 5xx responses, up to `retry.maxRetries` attempts
- Honor `Retry-After` header on 429 responses
- Apply `requestTimeoutMs` via `AbortSignal.timeout()`

---

## Future Work

The following are explicitly deferred from this spec:

1. **`getWorkspaceInfo()`** — A method on `WorkspaceHandle` that calls `GET /api/v1/workspaces/:id` to refresh metadata. Not needed for the initial use case since `createWorkspace` already returns the necessary fields.

2. **Cloud token issuance in the SDK** — Wrapping the CLI login OAuth flow (`GET /api/v1/cli/login`) to let SDK users authenticate with a browser redirect. Out of scope; users should obtain their `cld_at_` token through the CLI or dashboard.

3. **Python SDK** — Mirror `RelayfileSetup` as `RelayfileSetup` (sync) and `AsyncRelayfileSetup` in `packages/sdk/python`. This should follow the Python SDK implementation tracked in `sdk-improvements.md`.

4. **Workspace listing** — `RelayfileSetup.listWorkspaces()` to enumerate workspaces the current user owns. Requires the cloud `GET /api/v1/workspaces` endpoint to support non-Slack-integration queries.

5. **Connection ID pre-assignment** — Option B from the Cloud API Changes section: having the cloud return a stable, pre-assigned `connectionId` in the connect-session response. Requires Nango API support.

6. **`watchForConnection()` via WebSocket** — Replace the polling loop in `waitForConnection()` with a WebSocket or SSE subscription to the workspace event stream, triggering on the Nango auth webhook event. Reduces latency from 2s to near-instant.

7. **SDK-level Nango webhook ingest** — Once a connection is established, a convenience method to register a webhook listener URL with Nango through the cloud API, so the full sync loop can be configured from the SDK without touching the dashboard.
