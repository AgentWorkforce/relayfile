export type FileNodeType = "file" | "dir";

export interface TreeEntry {
  path: string;
  type: FileNodeType;
  revision: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
}

export interface TreeResponse {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

export interface FileReadResponse {
  path: string;
  revision: string;
  contentType: string;
  content: string;
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
}

export interface FileWriteRequest {
  contentType?: string;
  content: string;
}

export type WritebackState = "pending" | "succeeded" | "failed" | "dead_lettered";

export interface WriteQueuedResponse {
  opId: string;
  status: "queued" | "pending";
  targetRevision: string;
  writeback?: {
    provider?: string;
    state?: WritebackState;
  };
}

export type FilesystemEventType =
  | "file.created"
  | "file.updated"
  | "file.deleted"
  | "dir.created"
  | "dir.deleted"
  | "sync.error"
  | "sync.ignored"
  | "sync.suppressed"
  | "sync.stale"
  | "writeback.failed"
  | "writeback.succeeded";

export type EventOrigin = "provider_sync" | "agent_write" | "system";

export interface FilesystemEvent {
  eventId: string;
  type: FilesystemEventType;
  path: string;
  revision: string;
  origin: EventOrigin;
  correlationId: string;
  timestamp: string;
}

export interface EventFeedResponse {
  events: FilesystemEvent[];
  nextCursor: string | null;
}

export type OperationStatus =
  | "pending"
  | "running"
  | "succeeded"
  | "failed"
  | "dead_lettered"
  | "canceled";

export type WritebackActionType = "file_upsert" | "file_delete";

export interface OperationStatusResponse {
  opId: string;
  path?: string;
  revision?: string;
  action?: WritebackActionType;
  provider?: string;
  status: OperationStatus;
  attemptCount: number;
  nextAttemptAt?: string | null;
  lastError?: string | null;
  providerResult?: Record<string, unknown>;
  correlationId?: string;
}

export interface GetOperationsOptions {
  status?: OperationStatus;
  action?: WritebackActionType;
  provider?: string;
  cursor?: string;
  limit?: number;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface OperationFeedResponse {
  items: OperationStatusResponse[];
  nextCursor: string | null;
}

export interface SyncRefreshRequest {
  provider: string;
  reason?: string;
}

export type SyncProviderStatusState = "healthy" | "lagging" | "error" | "paused";

export interface SyncProviderStatus {
  provider: string;
  status: SyncProviderStatusState;
  cursor?: string | null;
  watermarkTs?: string | null;
  lagSeconds?: number;
  lastError?: string | null;
  failureCodes?: Record<string, number>;
}

export interface SyncStatusResponse {
  workspaceId: string;
  providers: SyncProviderStatus[];
}

export interface QueuedResponse {
  status: "queued";
  id: string;
  correlationId?: string;
}

export interface AckResponse {
  status: "acknowledged";
  id: string;
  correlationId?: string;
}

export interface ErrorResponse {
  code: string;
  message: string;
  correlationId: string;
  details?: Record<string, unknown>;
}

export interface ConflictErrorResponse extends ErrorResponse {
  expectedRevision: string;
  currentRevision: string;
  currentContentPreview?: string;
}

export interface ListTreeOptions {
  path?: string;
  depth?: number;
  cursor?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetEventsOptions {
  cursor?: string;
  limit?: number;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetSyncStatusOptions {
  provider?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetSyncIngressStatusOptions {
  correlationId?: string;
  signal?: AbortSignal;
}

export interface SyncIngressStatusResponse {
  workspaceId: string;
  queueDepth: number;
  queueCapacity: number;
  queueUtilization: number;
  pendingTotal: number;
  oldestPendingAgeSeconds: number;
  deadLetterTotal: number;
  deadLetterByProvider: Record<string, number>;
  acceptedTotal: number;
  droppedTotal: number;
  dedupedTotal: number;
  coalescedTotal: number;
  dedupeRate: number;
  coalesceRate: number;
  suppressedTotal: number;
  staleTotal: number;
  ingressByProvider: Record<string, {
    acceptedTotal: number;
    droppedTotal: number;
    dedupedTotal: number;
    coalescedTotal: number;
    pendingTotal: number;
    oldestPendingAgeSeconds: number;
    suppressedTotal: number;
    staleTotal: number;
    dedupeRate: number;
    coalesceRate: number;
  }>;
}

export interface GetSyncDeadLettersOptions {
  provider?: string;
  cursor?: string;
  limit?: number;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface DeadLetterItem {
  envelopeId: string;
  workspaceId: string;
  provider: string;
  deliveryId: string;
  correlationId?: string;
  failedAt: string;
  attemptCount: number;
  lastError: string;
}

export interface DeadLetterFeedResponse {
  items: DeadLetterItem[];
  nextCursor: string | null;
}

export interface WriteFileInput {
  workspaceId: string;
  path: string;
  baseRevision: string;
  content: string;
  contentType?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface DeleteFileInput {
  workspaceId: string;
  path: string;
  baseRevision: string;
  correlationId?: string;
  signal?: AbortSignal;
}
