import {
  RelayFileClient,
  type AccessTokenProvider
} from "./client.js"
import {
  createRelayfileCloudAccessTokenProvider,
  runRelayfileCloudLogin,
  type RelayfileCloudLoginOptions,
  type RelayfileCloudTokenSet,
  type RelayfileCloudTokenSetupOptions
} from "./cloud-login.js"
import {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError,
  MissingConnectionIdError,
  UnknownProviderError
} from "./setup-errors.js"
import {
  WORKSPACE_INTEGRATION_PROVIDERS,
  type AgentWorkspaceInvite,
  type AgentWorkspaceInviteOptions,
  type ConnectIntegrationOptions,
  type ConnectIntegrationResult,
  type CreateWorkspaceOptions,
  type JoinWorkspaceOptions,
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
const TOKEN_REFRESH_AGE_MS = 55 * 60 * 1000

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
    const cloudApiUrl = options.cloudApiUrl ?? DEFAULT_CLOUD_API_URL
    const tokens = await runRelayfileCloudLogin({
      ...options,
      cloudApiUrl
    })
    await options.onTokens?.({ ...tokens })
    return RelayfileSetup.fromCloudTokens(tokens, {
      ...options,
      cloudApiUrl: tokens.apiUrl ?? cloudApiUrl
    })
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

  async joinWorkspaceResponse(
    workspaceId: string,
    options: NormalizedJoinWorkspaceOptions
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
        })
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
        ready = await this.getConnectionStatus(provider, connectionId, {
          signal: options.signal,
          timeoutMs: remainingMs
        })
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
    return this.getConnectionStatus(provider, this.resolveConnectionId(provider, connectionId))
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

  getToken(): string {
    return this._token
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
      scopes:
        options.scopes && options.scopes.length > 0
          ? [...options.scopes]
          : [...this._joinOptions.scopes],
      relayfileToken:
        options.includeRelayfileToken === false ? undefined : this.getToken(),
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

  private async getConnectionStatus(
    provider: WorkspaceIntegrationProvider,
    connectionId: string,
    options: { signal?: AbortSignal; timeoutMs?: number } = {}
  ): Promise<boolean> {
    const query = new URLSearchParams({ connectionId })
    const response = validateIntegrationStatusResponse(
      await this._setup.requestJson({
        operation: "getIntegrationStatus",
        method: "GET",
        path: `api/v1/workspaces/${encodeURIComponent(this.workspaceId)}/integrations/${encodeURIComponent(provider)}/status?${query.toString()}`,
        signal: options.signal,
        timeoutMs: options.timeoutMs,
        tokenProvider: async () => this.getOrRefreshToken()
      })
    )
    return response.ready
  }

  private resolveRelaycastBaseUrl(override?: string): string {
    return (
      normalizeNonEmptyString(override) ??
      normalizeNonEmptyString(this.info.relaycastBaseUrl) ??
      DEFAULT_RELAYCAST_BASE_URL
    )
  }
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
): Required<IntegrationStatusResponse> {
  return {
    ready: requireBooleanField(payload, "ready")
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

function requireBooleanField(payload: unknown, field: string): boolean {
  const value = readField(payload, field)
  if (typeof value !== "boolean") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
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
