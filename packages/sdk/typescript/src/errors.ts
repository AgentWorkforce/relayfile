import type { ConflictErrorResponse, ErrorResponse } from "./types.js";

export type PluginContractType =
  | "connection-provider"
  | "integration-adapter"
  | "registration";

export interface InvalidPluginContractErrorOptions {
  code?: string;
  field: string;
  pluginType: PluginContractType;
}

export class RelayFileApiError extends Error {
  public readonly status: number;
  public readonly code: string;
  public readonly correlationId?: string;
  public readonly details?: Record<string, unknown>;

  constructor(status: number, payload: Partial<ErrorResponse> = {}) {
    super(payload.message ?? `RelayFile API error: ${status}`);
    this.name = "RelayFileApiError";
    this.status = status;
    this.code = payload.code ?? "unknown_error";
    this.correlationId = payload.correlationId;
    this.details = payload.details;
  }
}

export class RevisionConflictError extends RelayFileApiError {
  public readonly expectedRevision: string;
  public readonly currentRevision: string;
  public readonly currentContentPreview?: string;

  constructor(status: number, payload: ConflictErrorResponse) {
    super(status, payload);
    this.name = "RevisionConflictError";
    this.expectedRevision = payload.expectedRevision;
    this.currentRevision = payload.currentRevision;
    this.currentContentPreview = payload.currentContentPreview;
  }
}

export class QueueFullError extends RelayFileApiError {
  public readonly retryAfterSeconds?: number;

  constructor(status: number, payload: Partial<ErrorResponse> = {}, retryAfterSeconds?: number) {
    super(status, payload);
    this.name = "QueueFullError";
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export class InvalidStateError extends RelayFileApiError {
  constructor(status: number, payload: Partial<ErrorResponse> = {}) {
    super(status, payload);
    this.name = "InvalidStateError";
  }
}

export class PayloadTooLargeError extends RelayFileApiError {
  constructor(status: number, payload: Partial<ErrorResponse> = {}) {
    super(status, payload);
    this.name = "PayloadTooLargeError";
  }
}

export class InvalidPluginContractError extends TypeError {
  readonly code: string;
  readonly field: string;
  readonly pluginType: PluginContractType;

  constructor(message: string, options: InvalidPluginContractErrorOptions) {
    super(message);
    this.name = "InvalidPluginContractError";
    this.code = options.code ?? "invalid_plugin_contract";
    this.field = options.field;
    this.pluginType = options.pluginType;
  }
}

export class PluginValidationError extends InvalidPluginContractError {
  constructor(message: string, options: InvalidPluginContractErrorOptions) {
    super(message, options);
    this.name = "PluginValidationError";
  }
}

export class DuplicateAdapterError extends Error {
  readonly adapterName: string;
  readonly code = "duplicate_adapter_name";

  constructor(adapterName: string) {
    super(`Adapter already registered: ${adapterName}`);
    this.name = "DuplicateAdapterError";
    this.adapterName = adapterName;
  }
}

export class DuplicateAdapterNameError extends DuplicateAdapterError {
  constructor(adapterName: string) {
    super(adapterName);
    this.name = "DuplicateAdapterNameError";
  }
}

export class MissingAdapterError extends Error {
  readonly provider: string;
  readonly code = "missing_adapter";

  constructor(provider: string) {
    super(`No adapter registered for provider: ${provider}`);
    this.name = "MissingAdapterError";
    this.provider = provider;
  }
}
