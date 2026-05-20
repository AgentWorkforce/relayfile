import type { WorkspaceHandle } from "./setup.js"
import type { AccessTokenProvider } from "./client.js"

export const WORKSPACE_INTEGRATION_PROVIDERS = [
  "github",
  "slack-sage",
  "slack-my-senior-dev",
  "slack-nightcto",
  "notion",
  "linear"
] as const

export type WorkspaceIntegrationProvider =
  (typeof WORKSPACE_INTEGRATION_PROVIDERS)[number]

export interface WorkspacePermissions {
  readonly?: string[]
  ignored?: string[]
}

export interface RelayfileSetupRetryOptions {
  maxRetries: number
  baseDelayMs: number
}

export interface RelayfileSetupOptions {
  cloudApiUrl?: string
  accessToken?: AccessTokenProvider
  requestTimeoutMs?: number
  retry?: RelayfileSetupRetryOptions
}

export interface CreateWorkspaceOptions {
  name?: string
  permissions?: WorkspacePermissions
  agentName?: string
  scopes?: string[]
}

export interface JoinWorkspaceOptions {
  agentName?: string
  scopes?: string[]
  permissions?: WorkspacePermissions
}

export interface WorkspaceInfo {
  workspaceId: string
  relayfileUrl: string
  relaycastApiKey: string
  relaycastBaseUrl?: string
  createdAt?: string
  name?: string
  wsUrl?: string
}

export interface ConnectIntegrationOptions {
  connectionId?: string
  allowedIntegrations?: string[]
}

export interface ConnectIntegrationResult {
  connectLink: string | null
  sessionToken: string | null
  expiresAt: string | null
  alreadyConnected: boolean
  connectionId: string
}

export interface WaitForConnectionOptions {
  connectionId?: string
  pollIntervalMs?: number
  /**
   * @deprecated Use pollIntervalMs. This alias will be removed in a future
   * minor release.
   */
  intervalMs?: number
  timeoutMs?: number
  signal?: AbortSignal
  onPoll?: (elapsed: number) => void
}

export interface WorkspaceMountEnvOptions {
  localDir?: string
  remotePath?: string
  mode?: "poll" | "fuse"
  relaycastBaseUrl?: string
}

export type WorkspaceMountEnv = Record<string, string>

export type MountMode = "poll" | "fuse"

export interface MountSessionRequest {
  localDir: string
  remotePath?: string
  mode?: MountMode
  agentName?: string
  scopes?: string[]
  provider?: WorkspaceIntegrationProvider
}

export interface MountSessionResponse {
  workspaceId: string
  relayfileBaseUrl: string
  relayfileToken: string
  wsUrl: string
  remotePath: string
  localDir: string
  mode: MountMode
  scopes: string[]
  tokenIssuedAt: string | null
  expiresAt: string | null
  suggestedRefreshAt: string | null
  relaycastApiKey: string
  relaycastBaseUrl?: string
}

export interface MountSessionResult {
  workspaceId: string
  relayfileBaseUrl: string
  relayfileToken: string
  wsUrl: string
  remotePath: string
  localDir: string
  mode: MountMode
  scopes: string[]
  tokenIssuedAt: string | null
  expiresAt: string | null
  suggestedRefreshAt: string | null
  relaycastApiKey: string
  relaycastBaseUrl?: string
}

export interface MountedWorkspaceStatus {
  ready: boolean
  mode: MountMode
  pid?: number
  lastHeartbeatAt?: string
  lastReconcileAt?: string
  lastEventAt?: string
  expiresAt: string | null
  suggestedRefreshAt: string | null
  pendingWriteback?: number
  pendingConflicts?: number
}

export interface ReadMountedWorkspaceStatusInput {
  localDir: string
  workspaceId: string
  remotePath: string
  mode: MountMode
  relayfileBaseUrl: string
  relayfileToken: string
  expiresAt: string | null
  suggestedRefreshAt: string | null
  pid?: number
}

export interface MountedWorkspaceHandle {
  readonly workspaceId: string
  readonly localDir: string
  readonly remotePath: string
  readonly mode: MountMode
  readonly ready: boolean
  readonly expiresAt: string | null
  readonly suggestedRefreshAt: string | null

  env(): Record<string, string>
  status(): Promise<MountedWorkspaceStatus>
  stop(): Promise<void>
}

export interface MountLauncherEvent {
  type: string
  [key: string]: unknown
}

export interface MountLauncherStart {
  env: Record<string, string>
  cwd?: string
  signal?: AbortSignal
  readyTimeoutMs: number
  onEvent?: (event: MountLauncherEvent) => void
  background?: boolean
}

export interface MountLauncherInstance {
  pid?: number
  ready: Promise<void>
  status(): Promise<MountedWorkspaceStatus>
  stop(): Promise<void>
}

export interface MountLauncher {
  start(input: MountLauncherStart): Promise<MountLauncherInstance>
}

export interface MountWorkspaceInput {
  workspace?: WorkspaceHandle
  workspaceId?: string
  localDir: string
  remotePath?: string
  mode?: MountMode
  background?: boolean
  agentName?: string
  scopes?: string[]
  signal?: AbortSignal
  launcher?: MountLauncher
  readyTimeoutMs?: number
}

export interface EnsureMountedWorkspaceInput extends MountWorkspaceInput {
  provider?: WorkspaceIntegrationProvider
  verifyProvider?: boolean
  providerReadyTimeoutMs?: number
}

export interface AgentWorkspaceInviteOptions {
  agentName?: string
  relaycastBaseUrl?: string
  includeRelayfileToken?: boolean
}

export interface AgentWorkspaceScopedInviteOptions {
  /**
   * Scopes to grant on the minted JWT. Must be a subset of the calling
   * workspace token's grant; the cloud API rejects requests that exceed it.
   * If omitted, falls back to the join-time scopes (effectively the same as
   * the sync `agentInvite()`).
   */
  scopes?: string[]
  agentName?: string
  permissions?: WorkspacePermissions
  relaycastBaseUrl?: string
  /**
   * Set false to omit `relayfileToken` from the returned invite — useful
   * when the receiving agent already has a token and only needs the
   * connection metadata.
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
