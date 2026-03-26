/**
 * Storage adapter interface.
 *
 * All core logic functions accept a storage adapter — they never do I/O directly.
 * Implementors provide the actual storage (DO SQLite, Go in-memory, Postgres, etc.).
 *
 * Methods return plain data. Errors are signaled via return values, not exceptions.
 */

// ---------------------------------------------------------------------------
// Data types (plain objects, no classes)
// ---------------------------------------------------------------------------

export interface FileRow {
  path: string;
  revision: string;
  contentType: string;
  /** Content body. May be inline or a ref key depending on storage backend. */
  content: string;
  encoding: string;
  provider: string;
  lastEditedAt: string;
  semantics: FileSemantics;
}

export interface FileSemantics {
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

export interface EventRow {
  eventId: string;
  type: string;
  path: string;
  revision: string;
  origin: string;
  provider: string;
  correlationId: string;
  timestamp: string;
}

export interface OperationRow {
  opId: string;
  path: string;
  revision: string;
  action: string;
  provider: string;
  status: string;
  attemptCount: number;
  lastError: string | null;
  nextAttemptAt: string | null;
  correlationId: string;
}

export interface WritebackItem {
  id: string;
  workspaceId: string;
  path: string;
  revision: string;
  correlationId: string;
}

export interface EnvelopeRow {
  envelopeId: string;
  workspaceId: string;
  provider: string;
  deliveryId: string;
  deliveryIds?: string[];
  receivedAt: string;
  headers?: Record<string, string>;
  payload: Record<string, unknown>;
  correlationId: string;
  status: string;
  attemptCount: number;
  lastError: string | null;
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

export interface PaginationOptions {
  cursor?: string;
  limit?: number;
}

export interface Paginated<T> {
  items: T[];
  nextCursor: string | null;
}

export interface EnvelopeQueryOptions extends PaginationOptions {
  workspaceId?: string;
  provider?: string;
  status?: string;
}

// ---------------------------------------------------------------------------
// Storage adapter interface
// ---------------------------------------------------------------------------

export interface StorageAdapter {
  // ── Files ──────────────────────────────────────────────────────────
  getFile(path: string): FileRow | null;
  listFiles(): FileRow[];
  putFile(file: FileRow): void;
  deleteFile(path: string): void;
  loadFileContent?(file: FileRow): { content: string; encoding?: string } | string;

  // ── Events ─────────────────────────────────────────────────────────
  appendEvent(event: EventRow): void;
  listEvents(options: PaginationOptions & { provider?: string }): Paginated<EventRow>;
  getRecentEvents(limit: number): EventRow[];

  // ── Operations ─────────────────────────────────────────────────────
  getOperation(opId: string): OperationRow | null;
  putOperation(op: OperationRow): void;
  listOperations(options: PaginationOptions & {
    status?: string;
    action?: string;
    provider?: string;
  }): Paginated<OperationRow>;

  // ── Revision counter ───────────────────────────────────────────────
  nextRevision(): string;
  nextOperationId(): string;
  nextEventId(): string;

  // ── Writeback queue ────────────────────────────────────────────────
  enqueueWriteback(item: WritebackItem): void;
  getPendingWritebacks(): WritebackItem[];

  // ── Webhook envelopes ──────────────────────────────────────────────
  getEnvelopeByDelivery?(
    workspaceId: string,
    provider: string,
    deliveryId: string,
  ): EnvelopeRow | null;
  putEnvelope?(envelope: EnvelopeRow): void;
  putEnvelopeDeliveryAlias?(
    workspaceId: string,
    provider: string,
    deliveryId: string,
    envelopeId: string,
  ): void;
  listEnvelopes?(options: EnvelopeQueryOptions): Paginated<EnvelopeRow>;

  // ── Workspace metadata ─────────────────────────────────────────────
  getWorkspaceId(): string;
}
