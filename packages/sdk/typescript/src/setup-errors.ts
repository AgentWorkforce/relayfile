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

function readCloudErrorMessage(httpBody: unknown): string | undefined {
  if (!httpBody || typeof httpBody !== "object" || Array.isArray(httpBody)) {
    return undefined
  }
  const data = httpBody as Record<string, unknown>
  const message = data.message ?? data.error
  return typeof message === "string" && message.trim() !== "" ? message : undefined
}
