import {
  RelayFileClient,
  type AccessTokenProvider
} from "./client.js"
import {
  createRelayfileCloudAccessTokenProvider,
  type RelayfileCloudTokenSet,
  type RelayfileCloudTokenSetupOptions
} from "./cloud-token-provider.js"
import type { RelayfileCloudLoginOptions } from "./cloud-login.js"
import {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  InvalidLocalDirError,
  InvalidMountModeError,
  InvalidRemotePathError,
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError,
  MissingConnectionIdError,
  MountSessionInputError,
  ProviderNotConnectedError,
  ProviderNotReadyError,
  RelayfileSetupError,
  UnknownProviderError
} from "./setup-errors.js"
import {
  type EnsureMountedWorkspaceInput,
  WORKSPACE_INTEGRATION_PROVIDERS,
  type AgentWorkspaceInvite,
  type AgentWorkspaceInviteOptions,
  type AgentWorkspaceScopedInviteOptions,
  type ConnectIntegrationOptions,
  type ConnectIntegrationResult,
  type CreateWorkspaceOptions,
  type JoinWorkspaceOptions,
  type MountLauncher,
  type MountLauncherInstance,
  type MountMode,
  type MountSessionRequest,
  type MountSessionResponse,
  type MountSessionResult,
  type MountedWorkspaceHandle,
  type MountedWorkspaceStatus,
  type MountWorkspaceInput,
  type ReadMountedWorkspaceStatusInput,
  type RelayfileSetupOptions,
  type WaitForConnectionOptions,
  type WorkspaceMountEnv,
  type WorkspaceMountEnvOptions,
  type WorkspaceInfo,
  type WorkspaceIntegrationProvider,
  type WorkspacePermissions
} from "./setup-types.js"
import { RELAYFILE_SDK_VERSION } from "./version.js"

export { RELAYFILE_SDK_VERSION } from "./version.js"

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"
const DEFAULT_RELAYCAST_BASE_URL = "https://api.relaycast.dev"
const DEFAULT_REQUEST_TIMEOUT_MS = 30_000
const DEFAULT_RETRY_BASE_DELAY_MS = 500
const DEFAULT_RETRY_MAX_DELAY_MS = 5_000
const DEFAULT_RETRY_MAX_RETRIES = 3
const DEFAULT_AGENT_NAME = "sdk-agent"
const DEFAULT_SCOPES = ["fs:read", "fs:write"]
const DEFAULT_WAIT_INTERVAL_MS = 2_000
const DEFAULT_WAIT_TIMEOUT_MS = 300_000
const DEFAULT_MOUNT_READY_TIMEOUT_MS = 60_000
const DEFAULT_MOUNT_AGENT_NAME = "relayfile-mount"
const TOKEN_REFRESH_AGE_MS = 55 * 60 * 1000

const nodeOnlyMountLauncher: MountLauncher = {
  async start(): Promise<MountLauncherInstance> {
    throw new RelayfileSetupError(
      "The default relayfile-mount launcher is only available from @relayfile/sdk/cli. Import RelayfileSetup from @relayfile/sdk/cli or pass a custom launcher to mountWorkspace().",
      "node_only_sdk_feature"
    )
  }
}

interface CreateWorkspaceResponse {
  workspaceId?: string
  relayfileUrl?: string
  relaycastApiKey?: string
  createdAt?: string
  name?: string
}

interface JoinWorkspaceResponse {
  workspaceId?: string
  token?: string
  relayfileUrl?: string
  wsUrl?: string
  relaycastApiKey?: string
  relaycastBaseUrl?: string
}

type ValidatedJoinWorkspaceResponse = Required<
  Pick<
    JoinWorkspaceResponse,
    "workspaceId" | "token" | "relayfileUrl" | "wsUrl" | "relaycastApiKey"
  >
> &
  Pick<JoinWorkspaceResponse, "relaycastBaseUrl">

interface ConnectSessionResponse {
  token?: string
  expiresAt?: string
  connectLink?: string
  connectionId?: string
}

interface IntegrationStatusResponse {
  ready?: boolean
  state?: string
  initialSync?: {
    state?: string
  }
}

interface NormalizedRetryOptions {
  maxRetries: number
  baseDelayMs: number
  maxDelayMs: number
}

interface NormalizedJoinWorkspaceOptions {
  agentName: string
  scopes: string[]
  permissions?: WorkspacePermissions
}

interface NormalizedMountWorkspaceInput {
  workspace?: WorkspaceHandle
  workspaceId?: string
  localDir: string
  remotePath: string
  mode: MountMode
  background: boolean
  agentName?: string
  scopes?: string[]
  signal?: AbortSignal
  launcher?: MountLauncher
  readyTimeoutMs: number
}

interface NormalizedEnsureMountedWorkspaceInput extends NormalizedMountWorkspaceInput {
  provider?: WorkspaceIntegrationProvider
  verifyProvider: boolean
  providerReadyTimeoutMs: number
}

interface ValidatedIntegrationStatusResponse {
  ready: boolean
  state?: string
  initialSyncState?: string
}

interface CloudRequestOptions {
  operation: string
  method: string
  path: string
  body?: unknown
  signal?: AbortSignal
  timeoutMs?: number
  tokenProvider?: AccessTokenProvider
}

interface WorkspaceHandleOptions {
  setup: RelayfileSetup
  info: WorkspaceInfo
  token: string
  joinOptions: NormalizedJoinWorkspaceOptions
}

export class RelayfileSetup {
  private readonly cloudApiUrl: string
  private readonly accessToken?: AccessTokenProvider
  private readonly requestTimeoutMs: number
  private readonly retryOptions: NormalizedRetryOptions

  static async login(
    options: RelayfileCloudLoginOptions = {}
  ): Promise<RelayfileSetup> {
    void options
    throw new RelayfileSetupError(
      "RelayfileSetup.login() starts a local HTTP callback server and is only available from @relayfile/sdk/cli. Import RelayfileSetup from @relayfile/sdk/cli for interactive Cloud login.",
      "node_only_sdk_feature"
    )
  }

  static fromCloudTokens(
    tokens: RelayfileCloudTokenSet,
    options: RelayfileCloudTokenSetupOptions = {}
  ): RelayfileSetup {
    const cloudApiUrl = options.cloudApiUrl ?? tokens.apiUrl ?? DEFAULT_CLOUD_API_URL
    return new RelayfileSetup({
      ...options,
      cloudApiUrl,
      accessToken: createRelayfileCloudAccessTokenProvider(
        {
          ...tokens,
          apiUrl: tokens.apiUrl ?? cloudApiUrl
        },
        {
          ...options,
          cloudApiUrl
        }
      )
    })
  }

  constructor(options: RelayfileSetupOptions = {}) {
    this.cloudApiUrl = options.cloudApiUrl ?? DEFAULT_CLOUD_API_URL
    this.accessToken = options.accessToken
    this.requestTimeoutMs = Math.max(
      1,
      Math.floor(options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS)
    )
    this.retryOptions = normalizeRetryOptions(options.retry)
  }

  async createWorkspace(
    options: CreateWorkspaceOptions = {}
  ): Promise<WorkspaceHandle> {
    const createResponse = validateCreateWorkspaceResponse(
      await this.requestJson({
        operation: "createWorkspace",
        method: "POST",
        path: "api/v1/workspaces",
        body: compactObject({
          name: options.name,
          permissions: clonePermissions(options.permissions)
        })
      })
    )
    const joinOptions = normalizeJoinWorkspaceOptions({
      agentName: options.agentName,
      scopes: options.scopes,
      permissions: options.permissions
    })
    const joinResponse = await this.joinWorkspaceResponse(
      createResponse.workspaceId,
      joinOptions
    )

    return new WorkspaceHandle({
      setup: this,
      info: {
        workspaceId: createResponse.workspaceId,
        relayfileUrl: joinResponse.relayfileUrl,
        relaycastApiKey: joinResponse.relaycastApiKey,
        relaycastBaseUrl: joinResponse.relaycastBaseUrl,
        createdAt: createResponse.createdAt,
        name: createResponse.name,
        wsUrl: joinResponse.wsUrl
      },
      token: joinResponse.token,
      joinOptions
    })
  }

  async joinWorkspace(
    workspaceId: string,
    options: JoinWorkspaceOptions = {}
  ): Promise<WorkspaceHandle> {
    const joinOptions = normalizeJoinWorkspaceOptions(options)
    const joinResponse = await this.joinWorkspaceResponse(workspaceId, joinOptions)

    return new WorkspaceHandle({
      setup: this,
      info: {
        workspaceId: joinResponse.workspaceId,
        relayfileUrl: joinResponse.relayfileUrl,
        relaycastApiKey: joinResponse.relaycastApiKey,
        relaycastBaseUrl: joinResponse.relaycastBaseUrl,
        wsUrl: joinResponse.wsUrl
      },
      token: joinResponse.token,
      joinOptions
    })
  }

  async mountWorkspace(
    input: MountWorkspaceInput
  ): Promise<MountedWorkspaceHandle> {
    const normalized = normalizeMountWorkspaceInput(input)
    const workspace = await this.resolveWorkspaceForMount(normalized)
    const mountSession = await this.createMountSession(workspace, normalized)
    const launcher = normalized.launcher ?? this.getDefaultMountLauncher()
    const launcherInstance = await launcher.start({
      env: buildMountLauncherEnv(mountSession),
      signal: normalized.signal,
      readyTimeoutMs: normalized.readyTimeoutMs,
      background: normalized.background
    })

    try {
      await waitForMountReady(launcherInstance.ready, normalized.signal)
    } catch (error) {
      // If the abort signal fired before waitForMountReady got a chance to
      // attach a Promise.race handler, ensureLauncher.ready will reject
      // later. Attach a no-op rejection handler so Node 18+ does not surface
      // an unhandledRejection (which terminates the process by default).
      launcherInstance.ready.catch(() => {})
      await safeStopLauncher(launcherInstance)
      throw error
    }

    return new MountedWorkspaceHandleImpl({
      mountSession,
      launcherInstance: normalized.background ? launcherInstance : undefined,
      probeOnly: !normalized.background,
      readStatus: (statusInput) => this.readMountedWorkspaceStatus(statusInput)
    })
  }

  async ensureMountedWorkspace(
    input: EnsureMountedWorkspaceInput
  ): Promise<MountedWorkspaceHandle> {
    const normalized = normalizeEnsureMountedWorkspaceInput(input)
    const workspace = await this.resolveWorkspaceForMount(normalized)

    if (normalized.verifyProvider) {
      if (!normalized.provider) {
        throw new MountSessionInputError(
          "provider required when verifyProvider=true"
        )
      }
      await verifyWorkspaceProviderReady(
        workspace,
        normalized.provider,
        normalized.providerReadyTimeoutMs,
        normalized.signal
      )
    }

    return this.mountWorkspace({
      workspace,
      localDir: normalized.localDir,
      remotePath: normalized.remotePath,
      mode: normalized.mode,
      background: normalized.background,
      agentName: normalized.agentName,
      scopes: normalized.scopes,
      signal: normalized.signal,
      launcher: normalized.launcher,
      readyTimeoutMs: normalized.readyTimeoutMs
    })
  }

  async joinWorkspaceResponse(
    workspaceId: string,
    options: NormalizedJoinWorkspaceOptions,
    overrides: { tokenProvider?: AccessTokenProvider } = {}
  ): Promise<ValidatedJoinWorkspaceResponse> {
    return validateJoinWorkspaceResponse(
      await this.requestJson({
        operation: "joinWorkspace",
        method: "POST",
        path: `api/v1/workspaces/${encodeURIComponent(workspaceId)}/join`,
        body: compactObject({
          agentName: options.agentName,
          scopes: [...options.scopes],
          permissions: clonePermissions(options.permissions)
        }),
        tokenProvider: overrides.tokenProvider
      })
    )
  }

  async requestJson(options: CloudRequestOptions): Promise<unknown> {
    const url = buildCloudUrl(this.cloudApiUrl, options.path)
    const body =
      options.body === undefined ? undefined : JSON.stringify(options.body)

    let retries = 0
    for (;;) {
      const token = await resolveToken(options.tokenProvider ?? this.accessToken)
      const headers: Record<string, string> = {
        "X-Relayfile-SDK-Version": RELAYFILE_SDK_VERSION
      }
      if (token) {
        headers.Authorization = `Bearer ${token}`
      }
      if (body !== undefined) {
        headers["Content-Type"] = "application/json"
      }

      let response: Response
      try {
        response = await fetchWithTimeout(url, {
          method: options.method,
          headers,
          body,
          signal: options.signal
        }, options.timeoutMs ?? this.requestTimeoutMs, options.operation)
      } catch (error) {
        if (
          error instanceof CloudAbortError ||
          error instanceof CloudTimeoutError
        ) {
          throw error
        }
        if (!shouldRetryError(error, retries, this.retryOptions.maxRetries, options.signal)) {
          throw error
        }
        retries += 1
        await sleep(
          computeRetryDelayMs(this.retryOptions, retries, null),
          options.signal,
          options.operation
        )
        continue
      }

      const payload = await readResponseBody(response)
      if (response.ok) {
        return payload
      }

      if (
        shouldRetryStatus(response.status, retries, this.retryOptions.maxRetries, options.signal)
      ) {
        retries += 1
        await sleep(
          computeRetryDelayMs(
            this.retryOptions,
            retries,
            response.headers.get("retry-after")
          ),
          options.signal,
          options.operation
        )
        continue
      }

      throw new CloudApiError(response.status, payload)
    }
  }

  getCloudApiUrl(): string {
    return this.cloudApiUrl
  }

  protected getDefaultMountLauncher(): MountLauncher {
    return nodeOnlyMountLauncher
  }

  protected async readMountedWorkspaceStatus(
    input: ReadMountedWorkspaceStatusInput
  ): Promise<MountedWorkspaceStatus> {
    return {
      ready: true,
      mode: input.mode,
      pid: input.pid,
      expiresAt: input.expiresAt,
      suggestedRefreshAt: input.suggestedRefreshAt
    }
  }

  private async resolveWorkspaceForMount(
    input: NormalizedMountWorkspaceInput
  ): Promise<WorkspaceHandle> {
    if (input.workspace) {
      return input.workspace
    }
    return this.joinWorkspace(input.workspaceId!, {
      agentName: input.agentName,
      scopes: input.scopes
    })
  }

  private async createMountSession(
    workspace: WorkspaceHandle,
    input: NormalizedMountWorkspaceInput
  ): Promise<MountSessionResult> {
    const request: MountSessionRequest = compactObject({
      localDir: input.localDir,
      remotePath: input.remotePath,
      mode: input.mode,
      agentName: normalizeNonEmptyString(input.agentName) ?? DEFAULT_MOUNT_AGENT_NAME,
      scopes:
        input.scopes && input.scopes.length > 0
          ? [...input.scopes]
          : undefined
    })

    try {
      return validateMountSessionResponse(
        await workspace.requestJson({
          operation: "mountWorkspace",
          method: "POST",
          path: `api/v1/workspaces/${encodeURIComponent(workspace.workspaceId)}/relayfile/mount-session`,
          body: request,
          signal: input.signal
        }),
        input.localDir
      )
    } catch (error) {
      throw mapMountSessionError(error, request)
    }
  }
}

export class WorkspaceHandle {
  readonly info: WorkspaceInfo
  readonly workspaceId: string

  private readonly _setup: RelayfileSetup
  private readonly _joinOptions: NormalizedJoinWorkspaceOptions
  private readonly _pendingConnections = new Map<
    WorkspaceIntegrationProvider,
    string
  >()

  private _token: string
  private _tokenIssuedAt: number
  private _client?: RelayFileClient
  private _refreshPromise?: Promise<void>

  constructor(options: WorkspaceHandleOptions) {
    this.info = options.info
    this.workspaceId = options.info.workspaceId
    this._setup = options.setup
    this._joinOptions = {
      agentName: options.joinOptions.agentName,
      scopes: [...options.joinOptions.scopes],
      permissions: clonePermissions(options.joinOptions.permissions)
    }
    this._token = options.token
    this._tokenIssuedAt = Date.now()
  }

  client(): RelayFileClient {
    if (!this._client) {
      this._client = new RelayFileClient({
        baseUrl: this.info.relayfileUrl,
        token: async () => this.getOrRefreshToken()
      })
    }
    return this._client
  }

  async connectIntegration(
    provider: WorkspaceIntegrationProvider,
    options: ConnectIntegrationOptions = {}
  ): Promise<ConnectIntegrationResult> {
    assertProvider(provider)
    const requestedConnectionId = normalizeConnectionId(options.connectionId)

    if (requestedConnectionId) {
      const alreadyConnected = await this.isConnected(provider, requestedConnectionId)
      if (alreadyConnected) {
        this._pendingConnections.set(provider, requestedConnectionId)
        return {
          alreadyConnected: true,
          connectLink: null,
          sessionToken: null,
          expiresAt: null,
          connectionId: requestedConnectionId
        }
      }
    }

    const response = validateConnectSessionResponse(
      await this._setup.requestJson({
        operation: "connectIntegration",
        method: "POST",
        path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/connect-session`,
        body: {
          allowedIntegrations:
            options.allowedIntegrations && options.allowedIntegrations.length > 0
              ? [...options.allowedIntegrations]
              : [provider]
        },
        tokenProvider: async () => this.getOrRefreshToken()
      })
    )

    const connectionId = normalizeConnectionId(response.connectionId) ?? this.workspaceId
    this._pendingConnections.set(provider, connectionId)

    return {
      alreadyConnected: false,
      connectLink: response.connectLink,
      sessionToken: response.token,
      expiresAt: response.expiresAt,
      connectionId
    }
  }

  async connectNotion(
    options: Omit<ConnectIntegrationOptions, "allowedIntegrations"> = {}
  ): Promise<ConnectIntegrationResult> {
    return this.connectIntegration("notion", {
      ...options,
      allowedIntegrations: ["notion"]
    })
  }

  async waitForConnection(
    provider: WorkspaceIntegrationProvider,
    options: WaitForConnectionOptions = {}
  ): Promise<void> {
    assertProvider(provider)
    const connectionId = this.resolveConnectionId(provider, options.connectionId)
    const pollIntervalMs = Math.max(
      0,
      Math.floor(
        options.pollIntervalMs ?? options.intervalMs ?? DEFAULT_WAIT_INTERVAL_MS
      )
    )
    const timeoutMs = Math.max(
      1,
      Math.floor(options.timeoutMs ?? DEFAULT_WAIT_TIMEOUT_MS)
    )
    const startedAt = Date.now()

    for (;;) {
      throwIfAborted(options.signal, "waitForConnection")

      const elapsedMs = Date.now() - startedAt
      options.onPoll?.(elapsedMs)
      if (elapsedMs >= timeoutMs) {
        throw new IntegrationConnectionTimeoutError({
          provider,
          connectionId,
          elapsedMs,
          timeoutMs
        })
      }

      const remainingMs = timeoutMs - elapsedMs
      let ready: boolean
      try {
        const status = await this.getConnectionStatus(provider, connectionId, {
          signal: options.signal,
          timeoutMs: remainingMs
        })
        ready = status.ready
      } catch (error) {
        if (error instanceof CloudTimeoutError) {
          throw new IntegrationConnectionTimeoutError({
            provider,
            connectionId,
            elapsedMs: Date.now() - startedAt,
            timeoutMs
          })
        }
        throw error
      }
      if (ready) {
        return
      }

      const sleepMs = Math.min(pollIntervalMs, Math.max(0, timeoutMs - (Date.now() - startedAt)))
      await sleep(sleepMs, options.signal, "waitForConnection")
    }
  }

  async waitForNotion(
    options: WaitForConnectionOptions = {}
  ): Promise<void> {
    return this.waitForConnection("notion", options)
  }

  async isConnected(
    provider: WorkspaceIntegrationProvider,
    connectionId: string
  ): Promise<boolean> {
    assertProvider(provider)
    const status = await this.getConnectionStatus(
      provider,
      this.resolveConnectionId(provider, connectionId)
    )
    return status.ready
  }

  async disconnectIntegration(
    provider: WorkspaceIntegrationProvider,
    _connectionId?: string
  ): Promise<void> {
    assertProvider(provider)
    await this._setup.requestJson({
      operation: "disconnectIntegration",
      method: "DELETE",
      path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(provider)}/status`,
      tokenProvider: async () => this.getOrRefreshToken()
    })
    this._pendingConnections.delete(provider)
  }

  /**
   * Bind an existing Nango connection to this workspace + provider slot
   * without going through the OAuth re-mint flow. Use this when an operator
   * has already minted the connection out-of-band (Nango UI, third-party
   * setup) and just wants Cloud to start routing sync webhooks for it.
   *
   * The Cloud-side adopt route validates that the Nango connection exists
   * upstream and that its end-user/workspace tag matches this workspace.
   * On success returns the bound `connectionId` and, when a stale prior
   * row was atomically replaced, a `replacedConnectionId` so callers can
   * surface that a migration happened.
   *
   * Failure modes (HTTP body carries `code`):
   *   - `connection_not_found` (404): Nango doesn't know this connectionId
   *   - `workspace_mismatch` (409): connection belongs to a different
   *     workspace; the body includes `pathWorkspaceId` and
   *     `connectionWorkspaceId`
   *   - `existing_connection_live_or_unknown` (409): a different
   *     connection is already bound here and is either still live
   *     upstream or has indeterminate state; operator must disconnect
   *     first
   */
  async adoptIntegration(
    provider: WorkspaceIntegrationProvider,
    connectionId: string,
    options: { providerConfigKey?: string } = {}
  ): Promise<{ connectionId: string; replacedConnectionId?: string }> {
    assertProvider(provider)
    const trimmedConnectionId = connectionId?.trim()
    if (!trimmedConnectionId) {
      throw new Error("connectionId is required to adopt an integration")
    }
    const body: Record<string, unknown> = { connectionId: trimmedConnectionId }
    const providerConfigKey = options.providerConfigKey?.trim()
    if (providerConfigKey) {
      body.providerConfigKey = providerConfigKey
    }
    const response = (await this._setup.requestJson({
      operation: "adoptIntegration",
      method: "POST",
      path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(provider)}/adopt`,
      body,
      tokenProvider: async () => this.getOrRefreshToken()
    })) as {
      connectionId?: unknown
      replacedConnectionId?: unknown
    }
    const boundConnectionId =
      typeof response.connectionId === "string" && response.connectionId.trim()
        ? response.connectionId.trim()
        : trimmedConnectionId
    const replacedConnectionId =
      typeof response.replacedConnectionId === "string" &&
      response.replacedConnectionId.trim()
        ? response.replacedConnectionId.trim()
        : undefined
    this._pendingConnections.delete(provider)
    return replacedConnectionId
      ? { connectionId: boundConnectionId, replacedConnectionId }
      : { connectionId: boundConnectionId }
  }

  /**
   * List the upstream resources the current connection's OAuth grant
   * covers. Today only Atlassian-family providers (`jira`, `confluence`)
   * have meaningful entries here — every Atlassian OAuth grant can cover
   * multiple sites (cloudIds), and the operator needs to bind one of them
   * to this workspace via {@link setIntegrationMetadata} before sync can
   * run.
   *
   * Cloud returns 400 `provider_has_no_accessible_resources` for providers
   * that don't model this concept (currently everything non-Atlassian);
   * that error surfaces here as a `CloudApiError` so callers can handle
   * it explicitly rather than treating an empty list as ambiguous.
   *
   * @example
   * ```ts
   * const sites = await workspace.listAccessibleResources("jira")
   * if (sites.length > 1) {
   *   const choice = await promptOperator(sites)
   *   await workspace.setIntegrationMetadata("jira", {
   *     cloudId: choice.id,
   *     baseUrl: choice.url
   *   })
   * }
   * ```
   */
  async listAccessibleResources(
    provider: WorkspaceIntegrationProvider | string
  ): Promise<
    Array<{
      id: string
      url: string
      name?: string
      scopes?: string[]
      avatarUrl?: string
    }>
  > {
    const normalized = normalizeProviderId(provider)
    const response = (await this._setup.requestJson({
      operation: "listAccessibleResources",
      method: "GET",
      path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(normalized)}/accessible-resources`,
      tokenProvider: async () => this.getOrRefreshToken()
    })) as { resources?: unknown }
    if (!response || !Array.isArray(response.resources)) {
      return []
    }
    return response.resources
      .filter(
        (
          entry: unknown
        ): entry is {
          id: string
          url: string
          name?: string
          scopes?: string[]
          avatarUrl?: string
        } => {
          if (!entry || typeof entry !== "object") return false
          const record = entry as Record<string, unknown>
          return (
            typeof record.id === "string" &&
            record.id.length > 0 &&
            typeof record.url === "string" &&
            record.url.length > 0
          )
        }
      )
      .map((entry) => ({ ...entry }))
  }

  /**
   * Set the operator-controlled metadata namespace on the workspace's
   * connection for `provider`. The cloud route forwards the payload to
   * Nango's `setMetadata` (full-replacement, not merge), so the value
   * passed here becomes the connection's metadata wholesale. Callers that
   * want merge semantics should read existing metadata first.
   *
   * This is the second half of the post-OAuth picker flow for Jira /
   * Confluence (see {@link listAccessibleResources}), but is intentionally
   * general-purpose — any provider whose sync requires operator-supplied
   * connection-level config (e.g. a non-default API host) can use this.
   *
   * The cloud side rejects top-level keys that look like Nango plumbing
   * (`_*`, `connection_*`, `auth_*`, `provider_config_key`, `connection_id`)
   * with `code: "invalid_metadata"` so a typo can't clobber the connection.
   *
   * @example
   * ```ts
   * await workspace.setIntegrationMetadata("jira", {
   *   cloudId: "abc-123",
   *   baseUrl: "https://foo.atlassian.net"
   * })
   * ```
   */
  async setIntegrationMetadata(
    provider: WorkspaceIntegrationProvider | string,
    metadata: Record<string, unknown>
  ): Promise<Record<string, unknown>> {
    const normalized = normalizeProviderId(provider)
    if (!isPlainRecord(metadata)) {
      throw new Error("metadata must be a plain object")
    }
    const response = (await this._setup.requestJson({
      operation: "setIntegrationMetadata",
      method: "PUT",
      path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(normalized)}/metadata`,
      body: { metadata },
      tokenProvider: async () => this.getOrRefreshToken()
    })) as { metadata?: unknown }
    if (isPlainRecord(response?.metadata)) {
      return { ...response.metadata }
    }
    throw new Error("invalid cloud response: expected metadata to be a plain object")
  }

  getToken(): string {
    return this._token
  }

  async requestJson(
    options: Omit<CloudRequestOptions, "tokenProvider">
  ): Promise<unknown> {
    return this._setup.requestJson({
      ...options,
      tokenProvider: async () => this.getOrRefreshToken()
    })
  }

  mountEnv(options: WorkspaceMountEnvOptions = {}): WorkspaceMountEnv {
    const relaycastBaseUrl = this.resolveRelaycastBaseUrl(
      options.relaycastBaseUrl
    )

    return compactStringRecord({
      RELAYFILE_BASE_URL: this.info.relayfileUrl,
      RELAYFILE_TOKEN: this.getToken(),
      RELAYFILE_WORKSPACE: this.workspaceId,
      RELAYFILE_REMOTE_PATH: options.remotePath ?? "/",
      RELAYFILE_LOCAL_DIR: options.localDir,
      RELAYFILE_MOUNT_MODE: options.mode,
      RELAYCAST_API_KEY: this.info.relaycastApiKey,
      RELAY_API_KEY: this.info.relaycastApiKey,
      RELAYCAST_BASE_URL: relaycastBaseUrl,
      RELAY_BASE_URL: relaycastBaseUrl
    })
  }

  /**
   * Build an invite that hands a peer agent the calling workspace's existing
   * JWT and metadata. The invite carries the SAME token this handle holds,
   * with the SAME scopes — there is no per-invite downscoping. Use
   * {@link agentInviteScoped} when the receiver should have a strictly
   * narrower set of scopes than this workspace.
   */
  agentInvite(options: AgentWorkspaceInviteOptions = {}): AgentWorkspaceInvite {
    const relaycastBaseUrl = this.resolveRelaycastBaseUrl(
      options.relaycastBaseUrl
    )

    return compactObject({
      workspaceId: this.workspaceId,
      cloudApiUrl: this._setup.getCloudApiUrl(),
      relayfileUrl: this.info.relayfileUrl,
      relaycastApiKey: this.info.relaycastApiKey,
      relaycastBaseUrl,
      agentName: options.agentName ?? this._joinOptions.agentName,
      scopes: [...this._joinOptions.scopes],
      relayfileToken:
        options.includeRelayfileToken === false ? undefined : this.getToken(),
      createdAt: this.info.createdAt,
      name: this.info.name
    })
  }

  /**
   * Mint a fresh, downscoped relayfile JWT for a peer agent and return an
   * invite carrying that token. Round-trips through the cloud join endpoint
   * (`POST /api/v1/workspaces/{id}/join`), which signs a new JWT whose
   * `scopes` claim is the requested subset of this workspace's grant. The
   * cloud API rejects requests that exceed the calling workspace's scopes.
   *
   * Prefer this over {@link agentInvite} whenever the receiver should not
   * inherit the full workspace token's reach (e.g. one agent that only needs
   * `relayfile:fs:read:/notion/pages/*`). The caller's token is unaffected;
   * a separate JWT is issued for the invitee.
   *
   * @example
   * const invite = await workspace.agentInviteScoped({
   *   agentName: 'notion-summarizer',
   *   scopes: ['relayfile:fs:read:/notion/pages/*'],
   * })
   * // invite.relayfileToken is a JWT with scopes=['relayfile:fs:read:/notion/pages/*']
   */
  async agentInviteScoped(
    options: AgentWorkspaceScopedInviteOptions = {}
  ): Promise<AgentWorkspaceInvite> {
    const requestedScopes =
      options.scopes && options.scopes.length > 0
        ? [...options.scopes]
        : [...this._joinOptions.scopes]
    const agentName = options.agentName ?? this._joinOptions.agentName
    const permissions = options.permissions ?? this._joinOptions.permissions

    // Authenticate the join with this handle's workspace JWT. Without it,
    // the cloud cannot verify that requestedScopes ⊆ caller's grant, and
    // anonymous (or permissive) endpoints would silently mint a wide token —
    // exactly the failure this method is designed to prevent. The setup-
    // level accessToken (used for createWorkspace/joinWorkspace at the
    // workspace-bootstrap layer) is the wrong identity here; we want the
    // workspace JWT itself to be the parent the cloud downscopes from.
    const joinResponse = await this._setup.joinWorkspaceResponse(
      this.workspaceId,
      {
        agentName,
        scopes: requestedScopes,
        permissions
      },
      { tokenProvider: async () => this.getOrRefreshToken() }
    )

    // Prefer a relaycastBaseUrl returned by the cloud over the cached one —
    // the cloud is authoritative if it shipped a fresher value with the
    // join response.
    const relaycastBaseUrl = this.resolveRelaycastBaseUrl(
      options.relaycastBaseUrl ?? joinResponse.relaycastBaseUrl
    )

    return compactObject({
      workspaceId: this.workspaceId,
      cloudApiUrl: this._setup.getCloudApiUrl(),
      relayfileUrl: joinResponse.relayfileUrl ?? this.info.relayfileUrl,
      relaycastApiKey:
        joinResponse.relaycastApiKey ?? this.info.relaycastApiKey,
      relaycastBaseUrl,
      agentName,
      scopes: requestedScopes,
      relayfileToken:
        options.includeRelayfileToken === false
          ? undefined
          : joinResponse.token,
      createdAt: this.info.createdAt,
      name: this.info.name
    })
  }

  async refreshToken(): Promise<void> {
    if (!this._refreshPromise) {
      this._refreshPromise = this.performRefreshToken()
    }

    try {
      await this._refreshPromise
    } finally {
      this._refreshPromise = undefined
    }
  }

  private async performRefreshToken(): Promise<void> {
    const response = await this._setup.joinWorkspaceResponse(
      this.workspaceId,
      this._joinOptions
    )
    this._token = response.token
    this._tokenIssuedAt = Date.now()
  }

  private async getOrRefreshToken(): Promise<string> {
    if (Date.now() - this._tokenIssuedAt >= TOKEN_REFRESH_AGE_MS) {
      await this.refreshToken()
    }
    return this._token
  }

  private resolveConnectionId(
    provider: WorkspaceIntegrationProvider,
    connectionId?: string
  ): string {
    const resolved =
      normalizeConnectionId(connectionId) ?? this._pendingConnections.get(provider)
    if (!resolved) {
      throw new MissingConnectionIdError(provider)
    }
    return resolved
  }

  async getConnectionStatus(
    provider: WorkspaceIntegrationProvider,
    connectionId: string,
    options: { signal?: AbortSignal; timeoutMs?: number } = {}
  ): Promise<ValidatedIntegrationStatusResponse> {
    const query = new URLSearchParams({ connectionId })
    return validateIntegrationStatusResponse(
      await this.requestJson({
        operation: "getIntegrationStatus",
        method: "GET",
        path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(provider)}/status?${query.toString()}`,
        signal: options.signal,
        timeoutMs: options.timeoutMs
      })
    )
  }

  private resolveRelaycastBaseUrl(override?: string): string {
    return (
      normalizeNonEmptyString(override) ??
      normalizeNonEmptyString(this.info.relaycastBaseUrl) ??
      DEFAULT_RELAYCAST_BASE_URL
    )
  }
}

class MountedWorkspaceHandleImpl implements MountedWorkspaceHandle {
  readonly workspaceId: string
  readonly localDir: string
  readonly remotePath: string
  readonly mode: MountMode
  readonly expiresAt: string | null
  readonly suggestedRefreshAt: string | null

  private readonly mountSession: MountSessionResult
  private readonly launcherInstance?: Awaited<ReturnType<MountLauncher["start"]>>
  private readonly probeOnly: boolean
  private readonly readStatus: (
    input: ReadMountedWorkspaceStatusInput
  ) => Promise<MountedWorkspaceStatus>

  private readySnapshot = true
  private stopPromise?: Promise<void>

  constructor(input: {
    mountSession: MountSessionResult
    launcherInstance?: Awaited<ReturnType<MountLauncher["start"]>>
    probeOnly: boolean
    readStatus: (
      input: ReadMountedWorkspaceStatusInput
    ) => Promise<MountedWorkspaceStatus>
  }) {
    this.mountSession = input.mountSession
    this.workspaceId = input.mountSession.workspaceId
    this.localDir = input.mountSession.localDir
    this.remotePath = input.mountSession.remotePath
    this.mode = input.mountSession.mode
    this.expiresAt = input.mountSession.expiresAt
    this.suggestedRefreshAt = input.mountSession.suggestedRefreshAt
    this.launcherInstance = input.launcherInstance
    this.probeOnly = input.probeOnly
    this.readStatus = input.readStatus
  }

  get ready(): boolean {
    return this.readySnapshot
  }

  env(): Record<string, string> {
    return buildMountedWorkspaceEnv(this.mountSession)
  }

  async status(): Promise<MountedWorkspaceStatus> {
    if (this.launcherInstance) {
      const status = await this.launcherInstance.status()
      const mergedStatus = {
        ...status,
        expiresAt: this.expiresAt,
        suggestedRefreshAt: this.suggestedRefreshAt
      }
      this.readySnapshot = mergedStatus.ready
      return mergedStatus
    }

    const status = await this.readStatus({
      localDir: this.localDir,
      workspaceId: this.workspaceId,
      remotePath: this.remotePath,
      mode: this.mode,
      relayfileBaseUrl: this.mountSession.relayfileBaseUrl,
      relayfileToken: this.mountSession.relayfileToken,
      expiresAt: this.expiresAt,
      suggestedRefreshAt: this.suggestedRefreshAt,
      pid: this.launcherInstance?.pid
    })
    this.readySnapshot = status.ready
    return status
  }

  async stop(): Promise<void> {
    if (!this.stopPromise) {
      this.stopPromise = this.performStop()
    }
    await this.stopPromise
  }

  private async performStop(): Promise<void> {
    if (this.probeOnly || !this.launcherInstance) {
      return
    }
    await safeStopLauncher(this.launcherInstance)
  }
}

// The metadata + accessible-resources verbs accept `WorkspaceIntegrationProvider | string`
// so operators can target dynamically-discovered providers (Composio toolkit
// slugs, future Atlassian-family additions) without an SDK release. We still
// strip trailing whitespace and lower-case so consumers don't have to.
function normalizeProviderId(
  provider: WorkspaceIntegrationProvider | string
): string {
  const trimmed = typeof provider === "string" ? provider.trim() : ""
  if (!trimmed) {
    throw new Error("provider is required")
  }
  return trimmed.toLowerCase()
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false
  }
  const proto = Object.getPrototypeOf(value)
  return proto === Object.prototype || proto === null
}

function assertProvider(provider: string): asserts provider is WorkspaceIntegrationProvider {
  if (
    !(WORKSPACE_INTEGRATION_PROVIDERS as readonly string[]).includes(provider)
  ) {
    throw new UnknownProviderError(provider)
  }
}

function normalizeJoinWorkspaceOptions(
  options: JoinWorkspaceOptions = {}
): NormalizedJoinWorkspaceOptions {
  return {
    agentName: normalizeNonEmptyString(options.agentName) ?? DEFAULT_AGENT_NAME,
    scopes:
      options.scopes && options.scopes.length > 0
        ? [...options.scopes]
        : [...DEFAULT_SCOPES],
    permissions: clonePermissions(options.permissions)
  }
}

function normalizeRetryOptions(
  options: RelayfileSetupOptions["retry"]
): NormalizedRetryOptions {
  const maxRetries = Math.max(
    0,
    Math.floor(options?.maxRetries ?? DEFAULT_RETRY_MAX_RETRIES)
  )
  const baseDelayMs = Math.max(
    1,
    Math.floor(options?.baseDelayMs ?? DEFAULT_RETRY_BASE_DELAY_MS)
  )

  return {
    maxRetries,
    baseDelayMs,
    maxDelayMs: DEFAULT_RETRY_MAX_DELAY_MS
  }
}

function validateCreateWorkspaceResponse(
  payload: unknown
): Required<Pick<CreateWorkspaceResponse, "workspaceId" | "relayfileUrl" | "relaycastApiKey" | "createdAt">> &
  Pick<CreateWorkspaceResponse, "name"> {
  return {
    workspaceId: requireStringField(payload, "workspaceId"),
    relayfileUrl: requireStringField(payload, "relayfileUrl"),
    relaycastApiKey: requireStringField(payload, "relaycastApiKey"),
    createdAt: requireStringField(payload, "createdAt"),
    name: readOptionalStringField(payload, "name")
  }
}

function validateJoinWorkspaceResponse(
  payload: unknown
): ValidatedJoinWorkspaceResponse {
  return {
    workspaceId: requireStringField(payload, "workspaceId"),
    token: requireStringField(payload, "token"),
    relayfileUrl: requireStringField(payload, "relayfileUrl"),
    wsUrl: requireStringField(payload, "wsUrl"),
    relaycastApiKey: requireStringField(payload, "relaycastApiKey"),
    relaycastBaseUrl: readOptionalStringField(payload, "relaycastBaseUrl")
  }
}

function validateConnectSessionResponse(
  payload: unknown
): Required<Pick<ConnectSessionResponse, "token" | "expiresAt" | "connectLink">> &
  Pick<ConnectSessionResponse, "connectionId"> {
  return {
    token: requireStringField(payload, "token"),
    expiresAt: requireStringField(payload, "expiresAt"),
    connectLink: requireStringField(payload, "connectLink"),
    connectionId: readOptionalStringField(payload, "connectionId")
  }
}

function validateIntegrationStatusResponse(
  payload: unknown
): ValidatedIntegrationStatusResponse {
  return {
    ready: requireBooleanField(payload, "ready"),
    state: readOptionalStringField(payload, "state"),
    initialSyncState: readOptionalNestedStringField(payload, ["initialSync", "state"])
  }
}

function validateMountSessionResponse(
  payload: unknown,
  localDir: string
): MountSessionResult {
  return {
    workspaceId: requireStringField(payload, "workspaceId"),
    relayfileBaseUrl: stripTrailingSlash(requireStringField(payload, "relayfileBaseUrl")),
    relayfileToken: requireStringField(payload, "relayfileToken"),
    wsUrl: requireStringField(payload, "wsUrl"),
    remotePath: requireStringField(payload, "remotePath"),
    localDir,
    mode: requireMountModeField(payload, "mode"),
    scopes: requireStringArrayField(payload, "scopes"),
    tokenIssuedAt: readNullableStringField(payload, "tokenIssuedAt"),
    expiresAt: readNullableStringField(payload, "expiresAt"),
    suggestedRefreshAt: readNullableStringField(payload, "suggestedRefreshAt"),
    relaycastApiKey: requireStringField(payload, "relaycastApiKey"),
    relaycastBaseUrl: readOptionalStringField(payload, "relaycastBaseUrl")
  }
}

function requireStringField(payload: unknown, field: string): string {
  const value = readField(payload, field)
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function readOptionalStringField(payload: unknown, field: string): string | undefined {
  const value = readField(payload, field)
  if (value === undefined) {
    return undefined
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function readNullableStringField(payload: unknown, field: string): string | null {
  const value = readField(payload, field)
  if (value === null || value === undefined) {
    return null
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function readOptionalNestedStringField(
  payload: unknown,
  pathSegments: [string, string]
): string | undefined {
  const parent = readField(payload, pathSegments[0])
  if (!parent || typeof parent !== "object" || Array.isArray(parent)) {
    return undefined
  }
  const value = (parent as Record<string, unknown>)[pathSegments[1]]
  if (value === undefined) {
    return undefined
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(pathSegments.join("."), payload)
  }
  return value
}

function requireBooleanField(payload: unknown, field: string): boolean {
  const value = readField(payload, field)
  if (typeof value !== "boolean") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function requireMountModeField(payload: unknown, field: string): MountMode {
  const value = requireStringField(payload, field)
  if (value !== "poll" && value !== "fuse") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function requireStringArrayField(payload: unknown, field: string): string[] {
  const value = readField(payload, field)
  if (!Array.isArray(value) || value.some((entry) => typeof entry !== "string")) {
    throw new MalformedCloudResponseError(field, payload)
  }
  return [...value]
}

function readField(payload: unknown, field: string): unknown {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    return undefined
  }
  return (payload as Record<string, unknown>)[field]
}

function normalizeConnectionId(connectionId?: string): string | undefined {
  return normalizeNonEmptyString(connectionId)
}

function normalizeMountWorkspaceInput(
  input: MountWorkspaceInput
): NormalizedMountWorkspaceInput {
  if (!!input.workspace === !!input.workspaceId) {
    throw new MountSessionInputError(
      "Exactly one of workspace or workspaceId must be provided."
    )
  }

  const localDir = normalizeNonEmptyString(input.localDir)
  if (!localDir) {
    throw new InvalidLocalDirError(String(input.localDir ?? ""))
  }

  return {
    workspace: input.workspace,
    workspaceId: normalizeNonEmptyString(input.workspaceId),
    localDir: resolveLocalDir(localDir),
    remotePath: normalizeMountRemotePath(input.remotePath),
    mode: normalizeMountModeInput(input.mode),
    background: input.background !== false,
    agentName: normalizeNonEmptyString(input.agentName),
    scopes:
      input.scopes && input.scopes.length > 0 ? [...input.scopes] : undefined,
    signal: input.signal,
    launcher: input.launcher,
    readyTimeoutMs: Math.max(
      1,
      Math.floor(input.readyTimeoutMs ?? DEFAULT_MOUNT_READY_TIMEOUT_MS)
    )
  }
}

function normalizeEnsureMountedWorkspaceInput(
  input: EnsureMountedWorkspaceInput
): NormalizedEnsureMountedWorkspaceInput {
  const normalized = normalizeMountWorkspaceInput(input)
  return {
    ...normalized,
    provider: input.provider,
    verifyProvider: input.verifyProvider !== false,
    providerReadyTimeoutMs: Math.max(
      0,
      Math.floor(input.providerReadyTimeoutMs ?? 0)
    )
  }
}

function normalizeMountModeInput(mode?: string): MountMode {
  const normalized = normalizeNonEmptyString(mode) ?? "poll"
  if (normalized !== "poll" && normalized !== "fuse") {
    throw new InvalidMountModeError(normalized)
  }
  return normalized
}

function normalizeMountRemotePath(remotePath?: string): string {
  const normalized = normalizeNonEmptyString(remotePath) ?? "/"
  if (normalized.includes("\u0000")) {
    throw new InvalidRemotePathError(normalized)
  }
  const segments = normalized.replace(/\\/g, "/").split("/")
  if (segments.some((segment) => segment === "..")) {
    throw new InvalidRemotePathError(normalized)
  }
  const resolved = normalizePosixPath(
    normalized.startsWith("/") ? normalized : `/${normalized}`
  )
  if (!resolved.startsWith("/")) {
    throw new InvalidRemotePathError(normalized)
  }
  return resolved === "/" ? "/" : resolved.replace(/\/+$/, "")
}

function resolveLocalDir(localDir: string): string {
  const normalized = localDir.replace(/\\/g, "/")
  if (isAbsolutePathLike(normalized)) {
    return normalizePosixPath(normalized)
  }
  const cwd = readProcessCwd()
  if (!cwd) {
    return normalized
  }
  return normalizePosixPath(`${cwd.replace(/\\/g, "/")}/${normalized}`)
}

function isAbsolutePathLike(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:\//.test(value)
}

function readProcessCwd(): string | undefined {
  const maybeProcess = (globalThis as {
    process?: { cwd?: () => string }
  }).process
  try {
    return maybeProcess?.cwd?.()
  } catch {
    return undefined
  }
}

function normalizePosixPath(value: string): string {
  const isDrivePath = /^[A-Za-z]:\//.test(value)
  const prefix = isDrivePath
    ? value.slice(0, 3)
    : value.startsWith("/")
      ? "/"
      : ""
  const rest = isDrivePath ? value.slice(3) : value
  const segments: string[] = []
  for (const segment of rest.split("/")) {
    if (!segment || segment === ".") {
      continue
    }
    if (segment === "..") {
      segments.pop()
      continue
    }
    segments.push(segment)
  }
  const joined = segments.join("/")
  if (prefix === "/") {
    return joined ? `/${joined}` : "/"
  }
  if (prefix) {
    return joined ? `${prefix}${joined}` : prefix
  }
  return joined || "."
}

function normalizeNonEmptyString(value?: string): string | undefined {
  const normalized = value?.trim()
  return normalized ? normalized : undefined
}

function clonePermissions(
  permissions?: WorkspacePermissions
): WorkspacePermissions | undefined {
  if (!permissions) {
    return undefined
  }
  return compactObject({
    readonly: permissions.readonly ? [...permissions.readonly] : undefined,
    ignored: permissions.ignored ? [...permissions.ignored] : undefined
  })
}

function compactObject<T extends Record<string, unknown>>(value: T): T {
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => entry !== undefined)
  ) as T
}

function compactStringRecord(
  value: Record<string, string | undefined>
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(value).filter((entry): entry is [string, string] => {
      const [, entryValue] = entry
      return entryValue !== undefined
    })
  )
}

function buildMountedWorkspaceEnv(
  mountSession: MountSessionResult
): Record<string, string> {
  const relaycastBaseUrl =
    normalizeNonEmptyString(mountSession.relaycastBaseUrl) ??
    DEFAULT_RELAYCAST_BASE_URL

  return compactStringRecord({
    RELAYFILE_BASE_URL: mountSession.relayfileBaseUrl,
    RELAYFILE_TOKEN: mountSession.relayfileToken,
    RELAYFILE_WORKSPACE: mountSession.workspaceId,
    RELAYFILE_REMOTE_PATH: mountSession.remotePath,
    RELAYFILE_LOCAL_DIR: mountSession.localDir,
    RELAYFILE_MOUNT_MODE: mountSession.mode,
    RELAYCAST_API_KEY: mountSession.relaycastApiKey,
    RELAY_API_KEY: mountSession.relaycastApiKey,
    RELAYCAST_BASE_URL: relaycastBaseUrl,
    RELAY_BASE_URL: relaycastBaseUrl
  })
}

function buildMountLauncherEnv(
  mountSession: MountSessionResult
): Record<string, string> {
  return compactStringRecord({
    ...buildMountedWorkspaceEnv(mountSession),
    RELAYFILE_MOUNT_SCOPES: mountSession.scopes.join(" ")
  })
}

function stripTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "")
}

function buildCloudUrl(baseUrl: string, path: string): string {
  const base = new URL(baseUrl)
  if (!base.pathname.endsWith("/")) {
    base.pathname = `${base.pathname}/`
  }
  return new URL(path.replace(/^\/+/, ""), base).toString()
}

async function resolveToken(
  provider?: AccessTokenProvider
): Promise<string | undefined> {
  if (!provider) {
    return undefined
  }
  return typeof provider === "function" ? provider() : provider
}

async function fetchWithTimeout(
  url: string,
  init: RequestInit,
  timeoutMs: number,
  operation: string
): Promise<Response> {
  if (init.signal?.aborted) {
    throw new CloudAbortError(operation)
  }

  let didTimeout = false
  let didAbort = false
  const controller = new AbortController()
  const onAbort = () => {
    didAbort = true
    controller.abort()
  }
  const timer = setTimeout(() => {
    didTimeout = true
    controller.abort()
  }, timeoutMs)

  init.signal?.addEventListener("abort", onAbort, { once: true })

  try {
    return await fetch(url, { ...init, signal: controller.signal })
  } catch (error) {
    if (didTimeout) {
      throw new CloudTimeoutError(operation, timeoutMs)
    }
    if (didAbort || init.signal?.aborted) {
      throw new CloudAbortError(operation)
    }
    throw error
  } finally {
    clearTimeout(timer)
    init.signal?.removeEventListener("abort", onAbort)
  }
}

async function readResponseBody(response: Response): Promise<unknown> {
  const text = await response.text()
  if (text === "") {
    return null
  }

  const contentType = response.headers.get("content-type") ?? ""
  if (contentType.includes("application/json")) {
    try {
      return JSON.parse(text)
    } catch {
      return text
    }
  }

  return text
}

function shouldRetryStatus(
  status: number,
  retries: number,
  maxRetries: number,
  signal?: AbortSignal
): boolean {
  if (signal?.aborted || retries >= maxRetries) {
    return false
  }
  return status === 429 || (status >= 500 && status <= 599)
}

function shouldRetryError(
  error: unknown,
  retries: number,
  maxRetries: number,
  signal?: AbortSignal
): boolean {
  if (signal?.aborted || retries >= maxRetries) {
    return false
  }
  return !(error instanceof Error && error.name === "AbortError")
}

function computeRetryDelayMs(
  options: NormalizedRetryOptions,
  retryAttempt: number,
  retryAfterHeader: string | null
): number {
  const retryAfterMs = parseRetryAfterMs(retryAfterHeader)
  if (retryAfterMs !== null) {
    return Math.min(options.maxDelayMs, retryAfterMs)
  }

  const backoff = options.baseDelayMs * Math.pow(2, Math.max(0, retryAttempt - 1))
  return Math.min(options.maxDelayMs, backoff)
}

function parseRetryAfterMs(retryAfterHeader: string | null): number | null {
  if (!retryAfterHeader) {
    return null
  }

  const seconds = Number.parseInt(retryAfterHeader, 10)
  if (Number.isFinite(seconds) && seconds >= 0) {
    return seconds * 1000
  }

  const timestamp = Date.parse(retryAfterHeader)
  if (!Number.isNaN(timestamp)) {
    return Math.max(0, timestamp - Date.now())
  }

  return null
}

async function sleep(
  delayMs: number,
  signal: AbortSignal | undefined,
  operation: string
): Promise<void> {
  if (delayMs <= 0) {
    throwIfAborted(signal, operation)
    return
  }

  throwIfAborted(signal, operation)
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort)
      resolve()
    }, delayMs)
    const onAbort = () => {
      clearTimeout(timer)
      signal?.removeEventListener("abort", onAbort)
      reject(new CloudAbortError(operation))
    }
    signal?.addEventListener("abort", onAbort, { once: true })
  })
}

function throwIfAborted(
  signal: AbortSignal | undefined,
  operation: string
): void {
  if (signal?.aborted) {
    throw new CloudAbortError(operation)
  }
}

async function waitForMountReady(
  ready: Promise<void>,
  signal?: AbortSignal
): Promise<void> {
  if (!signal) {
    await ready
    return
  }
  if (signal.aborted) {
    throw new CloudAbortError("mountWorkspace")
  }
  let onAbort: (() => void) | undefined
  try {
    await Promise.race([
      ready,
      new Promise<never>((_, reject) => {
        onAbort = () => {
          signal.removeEventListener("abort", onAbort!)
          reject(new CloudAbortError("mountWorkspace"))
        }
        signal.addEventListener("abort", onAbort, { once: true })
      })
    ])
  } finally {
    if (onAbort) {
      signal.removeEventListener("abort", onAbort)
    }
  }
}

async function safeStopLauncher(
  launcherInstance: Awaited<ReturnType<MountLauncher["start"]>>
): Promise<void> {
  try {
    await launcherInstance.stop()
  } catch {
    // stop is best-effort during cleanup
  }
}

async function verifyWorkspaceProviderReady(
  workspace: WorkspaceHandle,
  provider: WorkspaceIntegrationProvider,
  providerReadyTimeoutMs: number,
  signal?: AbortSignal
): Promise<void> {
  const startedAt = Date.now()
  const connectionId = workspace.workspaceId

  for (;;) {
    throwIfAborted(signal, "ensureMountedWorkspace")

    const elapsedMs = Date.now() - startedAt
    const remainingMs =
      providerReadyTimeoutMs <= 0
        ? undefined
        : Math.max(1, providerReadyTimeoutMs - elapsedMs)

    const status = await workspace.getConnectionStatus(provider, connectionId, {
      signal,
      timeoutMs: remainingMs
    })

    if (status.ready) {
      return
    }
    // Integration status uses state="not_connected" to signal a provider that
    // has never been connected. Other non-ready states still indicate a
    // connected provider and map to ProviderNotReadyError.
    if (status.state === "not_connected") {
      throw new ProviderNotConnectedError(provider)
    }

    if (providerReadyTimeoutMs <= 0 || elapsedMs >= providerReadyTimeoutMs) {
      throw new ProviderNotReadyError({
        provider,
        state: status.state,
        initialSyncState: status.initialSyncState
      })
    }

    const sleepMs = Math.min(
      DEFAULT_WAIT_INTERVAL_MS,
      Math.max(1, providerReadyTimeoutMs - elapsedMs)
    )
    await sleep(sleepMs, signal, "ensureMountedWorkspace")
  }
}

function mapMountSessionError(
  error: unknown,
  request: MountSessionRequest
): unknown {
  if (!(error instanceof CloudApiError) || error.httpStatus !== 400) {
    return error
  }
  const code = readCloudErrorCode(error.httpBody)
  switch (code) {
    case "invalid_mode":
      return new InvalidMountModeError(request.mode ?? "poll")
    case "invalid_local_dir":
      return new InvalidLocalDirError(request.localDir)
    case "invalid_remote_path":
      return new InvalidRemotePathError(request.remotePath ?? "/")
    default:
      return error
  }
}

function readCloudErrorCode(httpBody: unknown): string | undefined {
  if (!httpBody || typeof httpBody !== "object" || Array.isArray(httpBody)) {
    return undefined
  }
  const payload = httpBody as Record<string, unknown>
  for (const field of ["code", "error"] as const) {
    const value = payload[field]
    if (typeof value === "string" && value.trim() !== "") {
      return value
    }
  }
  return undefined
}
