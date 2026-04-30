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
  intervalMs?: number
  timeoutMs?: number
  signal?: AbortSignal
  onPoll?: (attempt: number, ready: boolean) => void
}
