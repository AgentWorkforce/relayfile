import type { WorkspaceIntegrationProvider } from "./setup-types.js"

export class RelayfileSetupError extends Error {
  readonly code: string

  constructor(message: string, code: string) {
    super(message)
    this.name = new.target.name
    this.code = code
  }
}

export class CloudApiError extends RelayfileSetupError {
  readonly httpStatus: number
  readonly httpBody: unknown

  constructor(httpStatus: number, httpBody: unknown, message?: string) {
    super(
      message ??
        readCloudErrorMessage(httpBody) ??
        `Cloud API request failed with status ${httpStatus}.`,
      "cloud_api_error"
    )
    this.httpStatus = httpStatus
    this.httpBody = httpBody
  }
}

export class MalformedCloudResponseError extends RelayfileSetupError {
  readonly field: string
  readonly response: unknown

  constructor(field: string, response: unknown) {
    super(
      `Cloud API response is missing required field "${field}".`,
      "malformed_cloud_response"
    )
    this.field = field
    this.response = response
  }
}

export class CloudTimeoutError extends RelayfileSetupError {
  readonly operation: string
  readonly timeoutMs: number

  constructor(operation: string, timeoutMs: number) {
    super(
      `Timed out while waiting for ${operation} after ${timeoutMs}ms.`,
      "cloud_timeout_error"
    )
    this.operation = operation
    this.timeoutMs = timeoutMs
  }
}

export class IntegrationConnectionTimeoutError extends RelayfileSetupError {
  readonly provider: WorkspaceIntegrationProvider
  readonly connectionId: string
  readonly elapsedMs: number
  readonly timeoutMs: number

  constructor(input: {
    provider: WorkspaceIntegrationProvider
    connectionId: string
    elapsedMs: number
    timeoutMs: number
  }) {
    super(
      `Timed out waiting for ${input.provider} connection "${input.connectionId}" after ${input.elapsedMs}ms.`,
      "integration_connection_timeout"
    )
    this.provider = input.provider
    this.connectionId = input.connectionId
    this.elapsedMs = input.elapsedMs
    this.timeoutMs = input.timeoutMs
  }
}

export class CloudAbortError extends RelayfileSetupError {
  readonly operation: string

  constructor(operation: string) {
    super(`Aborted while waiting for ${operation}.`, "cloud_abort_error")
    this.operation = operation
  }
}

export class UnknownProviderError extends RelayfileSetupError {
  readonly provider: string

  constructor(provider: string) {
    super(`Unknown workspace integration provider "${provider}".`, "unknown_provider")
    this.provider = provider
  }
}

export class MissingConnectionIdError extends RelayfileSetupError {
  readonly provider: WorkspaceIntegrationProvider

  constructor(provider: WorkspaceIntegrationProvider) {
    super(
      `No connectionId is available for provider "${provider}".`,
      "missing_connection_id"
    )
    this.provider = provider
  }
}

export class MountSessionInputError extends RelayfileSetupError {
  constructor(message: string) {
    super(message, "mount_session_input_error")
  }
}

export class InvalidMountModeError extends RelayfileSetupError {
  readonly mode: string

  constructor(mode: string) {
    super(`Invalid mount mode "${mode}". Expected "poll" or "fuse".`, "invalid_mount_mode")
    this.mode = mode
  }
}

export class InvalidLocalDirError extends RelayfileSetupError {
  readonly localDir: string

  constructor(localDir: string, message?: string) {
    super(
      message ?? `Invalid localDir "${localDir}" for mount session.`,
      "invalid_local_dir"
    )
    this.localDir = localDir
  }
}

export class InvalidRemotePathError extends RelayfileSetupError {
  readonly remotePath: string

  constructor(remotePath: string, message?: string) {
    super(
      message ?? `Invalid remotePath "${remotePath}" for mount session.`,
      "invalid_remote_path"
    )
    this.remotePath = remotePath
  }
}

export class MountModeUnavailableError extends RelayfileSetupError {
  readonly mode: string

  constructor(mode: string, message?: string) {
    super(
      message ?? `Mount mode "${mode}" is unavailable in this environment.`,
      "mount_mode_unavailable"
    )
    this.mode = mode
  }
}

export class MountReadyTimeoutError extends RelayfileSetupError {
  readonly localDir: string
  readonly timeoutMs: number

  constructor(localDir: string, timeoutMs: number) {
    super(
      `Timed out waiting for the mount at ${localDir} to become ready after ${timeoutMs}ms.`,
      "mount_ready_timeout"
    )
    this.localDir = localDir
    this.timeoutMs = timeoutMs
  }
}

export class ProviderNotConnectedError extends RelayfileSetupError {
  readonly provider: WorkspaceIntegrationProvider

  constructor(provider: WorkspaceIntegrationProvider) {
    super(
      `Provider "${provider}" is not connected for this workspace.`,
      "provider_not_connected"
    )
    this.provider = provider
  }
}

export class ProviderNotReadyError extends RelayfileSetupError {
  readonly provider: WorkspaceIntegrationProvider
  readonly state?: string
  readonly initialSyncState?: string

  constructor(input: {
    provider: WorkspaceIntegrationProvider
    state?: string
    initialSyncState?: string
  }) {
    const details = [input.state, input.initialSyncState]
      .filter((value): value is string => typeof value === "string" && value.trim() !== "")
      .join(", ")
    super(
      details
        ? `Provider "${input.provider}" is connected but not ready (${details}).`
        : `Provider "${input.provider}" is connected but not ready.`,
      "provider_not_ready"
    )
    this.provider = input.provider
    this.state = input.state
    this.initialSyncState = input.initialSyncState
  }
}

function readCloudErrorMessage(httpBody: unknown): string | undefined {
  if (!httpBody || typeof httpBody !== "object" || Array.isArray(httpBody)) {
    return undefined
  }
  const data = httpBody as Record<string, unknown>
  const message = data.message ?? data.error
  return typeof message === "string" && message.trim() !== "" ? message : undefined
}
