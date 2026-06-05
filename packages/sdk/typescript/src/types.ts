export type FileNodeType = "file" | "dir";

/**
 * JWT claims expected by the Relayfile Go API when your token provider mints
 * bearer tokens for SDK requests.
 *
 * Example minting payload:
 * `const claims: RelayFileJwtClaims = { workspace_id: "ws_123", agent_name: "review-bot", aud: ["relayfile"] };`
 */
export interface RelayFileJwtClaims {
  /** Workspace id the token is scoped to, for example `ws_123`. */
  workspace_id: string;
  /** Stable agent name presented to the Relayfile server, for example `review-bot`. */
  agent_name: string;
  /** Audience must contain `relayfile`. */
  aud: "relayfile" | string[];
  exp?: number;
  iat?: number;
  nbf?: number;
  iss?: string;
  sub?: string;
  [claim: string]: unknown;
}

export interface TreeEntry {
  path: string;
  type: FileNodeType;
  revision: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
  propertyCount?: number;
  relationCount?: number;
  permissionCount?: number;
  commentCount?: number;
}

export interface TreeResponse {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

export interface FileSemantics {
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

/**
 * Stable identity for an idempotent write, used by the server to coalesce
 * duplicate deliveries (webhook retries, cross-product fan-in). Two writes
 * with equal `(kind, key)` in the same workspace within the dedup window
 * resolve to the same op. Callers typically derive `key` from the upstream
 * event id, e.g. `github:push:<sha>`.
 */
export interface ContentIdentity {
  kind: string;
  key: string;
}

export interface FileReadResponse {
  path: string;
  revision: string;
  contentType: string;
  content: string;
  encoding?: "utf-8" | "base64";
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  semantics?: FileSemantics;
}

export interface FileWriteRequest {
  contentType?: string;
  content: string;
  encoding?: "utf-8" | "base64";
  semantics?: FileSemantics;
  contentIdentity?: ContentIdentity;
}

export interface BulkWriteFile {
  path: string;
  contentType?: string;
  content: string;
  encoding?: "utf-8" | "base64";
  contentIdentity?: ContentIdentity;
}

export interface BulkWriteInput {
  workspaceId: string;
  files: BulkWriteFile[];
  forkId?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface BulkWriteResponse {
  written: number;
  errorCount: number;
  errors: Array<{
    path: string;
    code: string;
    message: string;
  }>;
  correlationId: string;
}

export interface FileQueryItem {
  path: string;
  revision: string;
  contentType: string;
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  size: number;
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

export interface FileQueryResponse {
  items: FileQueryItem[];
  nextCursor: string | null;
}

export type WritebackState = "pending" | "succeeded" | "failed" | "dead_lettered";
export type WritebackListState = WritebackState | "dead";

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
  origin?: EventOrigin;
  provider?: string;
  correlationId?: string;
  timestamp: string;
}

export interface ChangeEventResource {
  path: string;
  kind: string;
  id: string;
  provider: string;
}

export interface ChangeEventActor {
  id: string;
  displayName?: string;
}

export interface ChangeEventSummary {
  title?: string;
  status?: string;
  priority?: string;
  labels?: string[];
  actor?: ChangeEventActor;
  fieldsChanged?: string[];
  /** Optional compact tag list. Producers must cap this at 8 entries. */
  tags?: string[];
}

/**
 * Canonical lightweight event summary shape exported for proactive-runtime
 * adapters. `buildSummary(payload)` should return this exact structure.
 */
export type EventSummary = ChangeEventSummary;

export type ExpansionLevel = "summary" | "full" | "diff" | "thread";

export interface SummaryExpansion {
  level: "summary";
  path: string;
  summary: ChangeEventSummary;
}

export interface FullExpansion<TData = unknown> {
  level: "full";
  path: string;
  data: TData;
}

export interface DiffExpansion {
  level: "diff";
  path: string;
  diff: Record<string, unknown>;
}

export interface ThreadExpansion {
  level: "thread";
  path: string;
  thread: Record<string, unknown>;
}

export type Expansion<L extends ExpansionLevel = ExpansionLevel> =
  L extends "summary" ? SummaryExpansion
    : L extends "full" ? FullExpansion
      : L extends "diff" ? DiffExpansion
        : ThreadExpansion;

/**
 * Proactive runtime relayfile notification envelope.
 *
 * This differs from the lower-level FilesystemEvent feed above. The proactive
 * runtime consumes a small, stable notification that points at the canonical
 * payload in VFS instead of inlining provider payloads directly.
 *
 * M1 exposes the type so downstream packages can compile against the final
 * shape before M2 wires live delivery.
 */
export interface ChangeEvent {
  id: string;
  workspace: string;
  agentId?: string;
  type: "relayfile.changed";
  occurredAt: string;
  resource: ChangeEventResource;
  summary: ChangeEventSummary;
  expand<L extends ExpansionLevel = "summary">(level?: L): Promise<Expansion<L>>;
  digest?: string;
}

export interface DigestWindow {
  /** ISO 8601, inclusive. */
  readonly from: string;
  /** ISO 8601, exclusive. */
  readonly to: string;
}

export interface DigestBullet {
  /** One-line past-tense description, e.g. "AGE-16 moved to in-review". */
  readonly text: string;
  /** Mount-relative canonical path the bullet points at. */
  readonly canonicalPath: string;
}

export interface DigestSection {
  readonly provider: string;
  readonly bullets: readonly DigestBullet[];
}

export interface DigestContext {
  readonly provider: string;
  readonly window: DigestWindow;
  changeEvents(filter?: {
    providers?: string[];
    paths?: string[];
  }): Promise<readonly ChangeEvent[]>;
}

export type DigestHandler = (ctx: DigestContext) => Promise<DigestSection | null>;

export interface WritebackSchemaRef {
  readonly provider: string;
  readonly resource: string;
  /** Mount-relative path to the served `.schema.json` virtual file. */
  readonly path: string;
}

export interface LayoutManifestAlias {
  /** e.g. "by-title", "by-id", "by-edited". */
  readonly segment: string;
  readonly description?: string;
}

export interface LayoutManifestResource {
  readonly name: string;
  readonly canonicalFilename: string;
  readonly writebackActions?: readonly string[];
  readonly writebackSchemas?: readonly WritebackSchemaRef[];
  readonly aliases?: readonly LayoutManifestAlias[];
}

export interface LayoutManifest {
  readonly provider: string;
  readonly materialization: "eager" | "lazy";
  readonly resources: readonly LayoutManifestResource[];
}

export interface SubscribeOptions {
  coalesce?: "none" | "fire-once";
  coalesceMs?: number;
  pathScope?: string[];
  from?: "now" | "legacy";
  cursor?: string;
  aclToken?: string;
  drainMs?: number;
}

export interface Subscription {
  unsubscribe(): Promise<void>;
}

export type ReplayOptions =
  | { replayOnStart?: "none" }
  | { replayOnStart: `since:${string}` }
  | { replayOnStart: `last:${number}` };

export type ChangeStreamConnectionOptions = ReplayOptions & {
  workspaceId: string;
  aclToken?: string;
  from?: "now" | "legacy";
  cursor?: string;
};

export interface ChangeStreamConnection extends Subscription {
  readonly ready: Promise<void>;
}

export interface ResourceAtEventResult {
  path: string;
  data: unknown;
  digest: string;
}

export interface ChangeLogQueryResult {
  events: ChangeEvent[];
}

export interface EventFeedResponse {
  events: FilesystemEvent[];
  nextCursor: string | null;
}

export type ExportFormat = "tar" | "json" | "patch";

export interface ExportOptions {
  workspaceId: string;
  format?: ExportFormat;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface ExportJsonResponse {
  files: FileReadResponse[];
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
  createdAt?: string;
  updatedAt?: string;
  completedAt?: string | null;
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
  // Productized cloud-mount contract §7.4: cloud SHOULD set this to
  // false when webhook delivery is degraded so consumers can render the
  // "falling back to periodic sync" warning. Optional for backward
  // compatibility with deployments that do not yet emit it.
  webhookHealthy?: boolean;
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
export type AdminSyncAlertType = "status_error" | "lag_seconds" | "dead_lettered_envelopes" | "dead_lettered_ops";
export type AdminSyncAlertSeverity = "warning" | "critical";

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

export interface AdminSyncAlert {
  workspaceId: string;
  provider: string;
  type: AdminSyncAlertType;
  severity: AdminSyncAlertSeverity;
  value: number;
  threshold: number;
  message: string;
}

export interface AdminSyncAlertThresholds {
  statusError: number;
  lagSeconds: number;
  deadLetteredEnvelopes: number;
  deadLetteredOps: number;
}

export interface AdminSyncAlertTotals {
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
  returnedWorkspaceCount: number;
  workspaceIds: string[];
  nextCursor: string | null;
  providerStatusCount: number;
  healthyCount: number;
  laggingCount: number;
  errorCount: number;
  pausedCount: number;
  deadLetteredEnvelopesTotal: number;
  deadLetteredOpsTotal: number;
  thresholds: AdminSyncAlertThresholds;
  alertTotals: AdminSyncAlertTotals;
  alertsTruncated: boolean;
  alerts: AdminSyncAlert[];
  failureCodes: Record<string, number>;
  workspaces: Record<string, SyncStatusResponse>;
}

export interface ListTreeOptions {
  path?: string;
  depth?: number;
  cursor?: string;
  forkId?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface QueryFilesOptions {
  path?: string;
  provider?: string;
  relation?: string;
  permission?: string;
  comment?: string;
  properties?: Record<string, string>;
  cursor?: string;
  limit?: number;
  forkId?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface ReadFileInput {
  workspaceId: string;
  path: string;
  forkId?: string;
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
  cursor?: string;
  limit?: number;
  includeWorkspaces?: boolean;
  statusErrorThreshold?: number;
  lagSecondsThreshold?: number;
  deadLetteredEnvelopesThreshold?: number;
  deadLetteredOpsThreshold?: number;
  maxAlerts?: number;
  includeAlerts?: boolean;
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
  encoding?: "utf-8" | "base64";
  semantics?: FileSemantics;
  forkId?: string;
  contentIdentity?: ContentIdentity;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface DeleteFileInput {
  workspaceId: string;
  path: string;
  baseRevision: string;
  forkId?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface CreateForkInput {
  workspaceId: string;
  proposalId: string;
  ttlSeconds?: number;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface DiscardForkInput {
  workspaceId: string;
  forkId: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface CommitForkInput {
  workspaceId: string;
  forkId: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface CommitForkResponse {
  revision: string;
  writtenCount: number;
  deletedCount: number;
}

export interface IngestWebhookInput {
  workspaceId: string;
  provider: string;
  event_type: string;
  path: string;
  data?: Record<string, unknown>;
  delivery_id?: string;
  timestamp?: string;
  headers?: Record<string, string>;
  correlationId?: string;
  signal?: AbortSignal;
}

export type WritebackDeadLetterErrorCode =
  | "schema_violation"
  | "provider_4xx"
  | "provider_5xx_exhausted"
  | "timeout";

export interface WritebackDeadLetterError {
  code: WritebackDeadLetterErrorCode;
  message: string;
  providerStatus?: number;
  providerResponse?: Record<string, unknown>;
  attempts: number;
  firstAttemptAt: string;
  lastAttemptAt: string;
  opId: string;
}

export interface WritebackItem {
  id: string;
  workspaceId: string;
  path: string;
  revision: string;
  correlationId: string;
  state?: WritebackListState;
  provider?: string;
  action?: WritebackActionType;
  ts?: string;
  code?: WritebackDeadLetterErrorCode | string;
  message?: string;
  providerStatus?: number;
  providerResponse?: Record<string, unknown>;
  attempts?: number;
  firstAttemptAt?: string;
  enqueuedAt?: string;
  lastAttemptAt?: string;
  error?: WritebackDeadLetterError;
}

export interface WritebackItemDetail extends WritebackItem {
  error?: WritebackDeadLetterError;
}

export interface AckWritebackInput {
  workspaceId: string;
  itemId: string;
  success: boolean;
  error?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface AckWritebackResponse {
  status: "acknowledged";
  id: string;
  correlationId?: string;
  success: boolean;
}
