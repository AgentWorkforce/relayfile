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
  provider?: string;
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
  deadLetteredEnvelopes?: number;
  deadLetteredOps?: number;
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

export interface BackendStatusResponse {
  backendProfile: string;
  stateBackend: string;
  envelopeQueue: string;
  envelopeQueueDepth: number;
  envelopeQueueCapacity: number;
  writebackQueue: string;
  writebackQueueDepth: number;
  writebackQueueCapacity: number;
}

export type AdminIngressAlertType = "dead_letters" | "pending_backlog" | "drop_rate" | "stale_events";
export type AdminIngressAlertSeverity = "warning" | "critical";
export type AdminIngressAlertProfile = "strict" | "balanced" | "relaxed";
export type AdminIngressEffectiveAlertProfile = AdminIngressAlertProfile | "custom";

export interface AdminIngressAlert {
  workspaceId: string;
  type: AdminIngressAlertType;
  severity: AdminIngressAlertSeverity;
  value: number;
  threshold: number;
  message: string;
}

export interface AdminIngressAlertThresholds {
  pending: number;
  deadLetter: number;
  stale: number;
  dropRate: number;
}

export interface AdminIngressAlertTotals {
  total: number;
  critical: number;
  warning: number;
  byType: Record<string, number>;
}

export interface AdminIngressStatusResponse {
  generatedAt: string;
  alertProfile: AdminIngressAlertProfile;
  effectiveAlertProfile: AdminIngressEffectiveAlertProfile;
  workspaceCount: number;
  returnedWorkspaceCount: number;
  workspaceIds: string[];
  nextCursor: string | null;
  pendingTotal: number;
  deadLetterTotal: number;
  acceptedTotal: number;
  droppedTotal: number;
  dedupedTotal: number;
  coalescedTotal: number;
  suppressedTotal: number;
  staleTotal: number;
  thresholds: AdminIngressAlertThresholds;
  alertTotals: AdminIngressAlertTotals;
  alertsTruncated: boolean;
  alerts: AdminIngressAlert[];
  workspaces: Record<string, SyncIngressStatusResponse>;
}

export interface AdminSyncStatusResponse {
  generatedAt: string;
  workspaceCount: number;
  workspaceIds: string[];
  providerStatusCount: number;
  healthyCount: number;
  laggingCount: number;
  errorCount: number;
  pausedCount: number;
  deadLetteredEnvelopesTotal: number;
  deadLetteredOpsTotal: number;
  failureCodes: Record<string, number>;
  workspaces: Record<string, SyncStatusResponse>;
}

export interface ListTreeOptions {
  path?: string;
  depth?: number;
  cursor?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetEventsOptions {
  provider?: string;
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
  provider?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetAdminIngressStatusOptions {
  workspaceId?: string;
  provider?: string;
  alertProfile?: AdminIngressAlertProfile;
  pendingThreshold?: number;
  deadLetterThreshold?: number;
  staleThreshold?: number;
  dropRateThreshold?: number;
  nonZeroOnly?: boolean;
  maxAlerts?: number;
  cursor?: string;
  limit?: number;
  includeWorkspaces?: boolean;
  includeAlerts?: boolean;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface GetAdminSyncStatusOptions {
  workspaceId?: string;
  provider?: string;
  nonZeroOnly?: boolean;
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
