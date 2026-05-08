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
