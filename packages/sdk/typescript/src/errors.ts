import type { ConflictErrorResponse, ErrorResponse, MergeConflictDetail } from "./types.js";

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

export class ParentMovedError extends RelayFileApiError {
  public readonly currentRevision: string;

  constructor(status: number, payload: Partial<ErrorResponse> & { currentRevision: string }) {
    super(status, payload);
    this.name = "ParentMovedError";
    this.currentRevision = payload.currentRevision;
  }
}

export class MergeConflictError extends RelayFileApiError {
  public readonly currentRevision: string;
  public readonly conflicts: MergeConflictDetail[];

  constructor(
    status: number,
    payload: Partial<ErrorResponse> & { currentRevision: string; conflicts: MergeConflictDetail[] }
  ) {
    super(status, payload);
    this.name = "MergeConflictError";
    this.currentRevision = payload.currentRevision;
    this.conflicts = payload.conflicts;
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
