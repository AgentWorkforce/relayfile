import type {
  EnvelopeQueryOptions,
  EnvelopeRow,
  EventRow,
  FileRow,
  FileSemantics,
  OperationRow,
  Paginated,
  PaginationOptions,
  WebhookStorageAdapter,
  WritebackItem,
} from "@relayfile/core";
import { resolveFilePermissions } from "@relayfile/core";
import { createHash } from "node:crypto";
import type { ActorSql, SqlValue } from "./db.js";
import { nullableStr, num, str } from "./db.js";

/** A file row with the fields core's FileRow does not carry. */
export interface StoredFile extends FileRow {
  /** sha256 hex of the decoded bytes; `blobSha256` equals it when the bytes live in object storage. */
  contentHash: string;
  size: number;
  blobSha256: string | null;
  providerObjectId: string;
  nodeType: "file" | "symlink";
  target: string;
  mode: number;
}

export interface StoredEvent extends EventRow {
  seq: number;
  contentHash: string;
  typeMetadata: { type?: string; target?: string; mode?: number } | null;
  /** file.* events: the file's effective permissions when the event was appended; null = not captured. */
  aclPermissions: string[] | null;
}

export interface StoredOperation extends OperationRow {
  createdAt: string;
  updatedAt: string;
  completedAt: string | null;
  providerResult: Record<string, unknown> | null;
}

const DEFAULT_PAGE = 100;
const MAX_PAGE = 1000;

/**
 * @relayfile/core StorageAdapter over a workspace actor's SQLite.
 *
 * Every call runs inside the actor's mailbox (one call at a time) and inside
 * the runtime's per-call transaction, which satisfies core's requirement that
 * read-modify-write sequences are serialized.
 */
export class SqliteWorkspaceStorage implements WebhookStorageAdapter {
  /** Set by the runtime before core writes a file so putFile can record bytes metadata. */
  pendingFileMeta: Partial<Pick<StoredFile, "contentHash" | "size" | "blobSha256" | "providerObjectId" | "nodeType" | "target" | "mode">> | null = null;

  /** Hash of the last file put, so the event core appends right after can carry it. */
  private lastPut: { path: string; contentHash: string; nodeType: string; target: string; mode: number } | null = null;

  constructor(
    private readonly sql: ActorSql,
    private readonly workspaceId: string,
    private readonly now: () => string = () => new Date().toISOString(),
  ) {}

  getWorkspaceId(): string {
    return this.workspaceId;
  }

  // ── Files ──────────────────────────────────────────────────────────────
  getFile(path: string): StoredFile | null {
    const rows = this.sql.exec(`SELECT * FROM rf_files WHERE path = ?`, path);
    return rows[0] ? toFile(rows[0]) : null;
  }

  listFiles(): StoredFile[] {
    return this.sql.exec(`SELECT * FROM rf_files ORDER BY path`).map(toFile);
  }

  listFilesUnder(prefix: string, afterPath: string, limit: number): StoredFile[] {
    const base = prefix === "/" ? "/" : `${prefix.replace(/\/$/, "")}/`;
    return this.sql
      .exec(
        `SELECT * FROM rf_files WHERE (path = ? OR substr(path, 1, ?) = ?) AND path > ? ORDER BY path LIMIT ?`,
        prefix,
        base.length,
        base,
        afterPath,
        limit,
      )
      .map(toFile);
  }

  countFiles(): number {
    return num(this.sql.exec<{ n: unknown }>(`SELECT COUNT(*) AS n FROM rf_files`)[0]?.n);
  }

  putFile(file: FileRow): void {
    const meta = this.pendingFileMeta ?? {};
    const existing = this.getFile(file.path);
    const inlineContent = meta.blobSha256 ? null : file.content;
    const bytes = meta.blobSha256 ? null : decodeContent(file.content, file.encoding);
    const contentHash = meta.contentHash || (bytes ? createHash("sha256").update(bytes).digest("hex") : "");
    const size = meta.size ?? (bytes ? encodedLength(bytes.byteLength, file.encoding) : 0);
    this.lastPut = {
      path: file.path,
      contentHash,
      nodeType: meta.nodeType ?? "file",
      target: meta.target ?? "",
      mode: meta.mode ?? existing?.mode ?? 0,
    };
    this.sql.exec(
      `INSERT INTO rf_files (path, revision, content_type, content, blob_sha256, size, encoding, provider,
         provider_object_id, node_type, target, mode, content_hash, last_edited_at, semantics)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(path) DO UPDATE SET revision = excluded.revision, content_type = excluded.content_type,
         content = excluded.content, blob_sha256 = excluded.blob_sha256, size = excluded.size,
         encoding = excluded.encoding, provider = excluded.provider, provider_object_id = excluded.provider_object_id,
         node_type = excluded.node_type, target = excluded.target, mode = excluded.mode,
         content_hash = excluded.content_hash, last_edited_at = excluded.last_edited_at, semantics = excluded.semantics`,
      file.path,
      file.revision,
      file.contentType,
      inlineContent,
      meta.blobSha256 ?? null,
      size,
      file.encoding || "utf-8",
      file.provider ?? "",
      meta.providerObjectId ?? existing?.providerObjectId ?? "",
      this.lastPut.nodeType,
      this.lastPut.target,
      this.lastPut.mode,
      contentHash,
      file.lastEditedAt,
      JSON.stringify(file.semantics ?? {}),
    );
  }

  deleteFile(path: string): void {
    // Snapshot the effective permissions (ancestor markers + the file's own
    // rules) before the row disappears, for the file.deleted event that the
    // caller (runtime or core) appends next.
    this.lastDelete = { path, permissions: resolveFilePermissions(this, path, true) };
    this.sql.exec(`DELETE FROM rf_files WHERE path = ?`, path);
  }

  private lastDelete: { path: string; permissions: string[] } | null = null;

  // ── Events ─────────────────────────────────────────────────────────────
  appendEvent(event: EventRow): void {
    const fileEvent = event.type === "file.created" || event.type === "file.updated";
    const put = fileEvent && this.lastPut?.path === event.path ? this.lastPut : null;
    const explicit = event as Partial<StoredEvent>;
    // Effective permissions at event time (Go Event.ACLPermissions), so a
    // file's events stay hidden from readers it denied even after the file
    // is deleted or its rules change.
    let aclPermissions: string[] | null = null;
    if (event.type === "file.deleted") {
      if (this.lastDelete?.path === event.path) aclPermissions = this.lastDelete.permissions;
      this.lastDelete = null;
    } else if (event.type.startsWith("file.") && event.path) {
      aclPermissions = resolveFilePermissions(this, event.path, true);
    }
    const typeMetadata =
      explicit.typeMetadata ??
      (put ? { type: put.nodeType, ...(put.target ? { target: put.target } : {}), ...(put.mode ? { mode: put.mode } : {}) } : null);
    this.sql.exec(
      `INSERT INTO rf_events (event_id, type, path, revision, content_hash, origin, provider, correlation_id, timestamp, type_metadata, acl_permissions)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      event.eventId,
      event.type,
      event.path,
      event.revision ?? "",
      explicit.contentHash ?? put?.contentHash ?? "",
      event.origin ?? "",
      event.provider ?? "",
      event.correlationId ?? "",
      event.timestamp,
      typeMetadata ? JSON.stringify(typeMetadata) : null,
      aclPermissions ? JSON.stringify(aclPermissions) : null,
    );
    this.lastEventSeq = num(this.sql.exec<{ seq: unknown }>(`SELECT last_insert_rowid() AS seq`)[0]?.seq);
  }

  /** seq of the last event appended through this adapter instance (for socket fan-out). */
  lastEventSeq = 0;

  /** Ascending page after an exclusive cursor (an event id). */
  listEvents(options: PaginationOptions & { provider?: string }): Paginated<StoredEvent> {
    const limit = clampLimit(options.limit);
    const after = options.cursor ? this.eventSeq(options.cursor) : 0;
    const bindings: SqlValue[] = [after];
    let where = `seq > ?`;
    if (options.provider) {
      where += ` AND provider = ?`;
      bindings.push(options.provider);
    }
    const rows = this.sql
      .exec(`SELECT * FROM rf_events WHERE ${where} ORDER BY seq LIMIT ?`, ...bindings, limit + 1)
      .map(toEvent);
    const items = rows.slice(0, limit);
    return { items, nextCursor: rows.length > limit ? items[items.length - 1]!.eventId : null };
  }

  /** seq for an event id; -1 when it is unknown (expired or never issued). */
  eventSeq(eventId: string): number {
    const rows = this.sql.exec<{ seq: unknown }>(`SELECT seq FROM rf_events WHERE event_id = ?`, eventId);
    return rows[0] ? num(rows[0].seq) : -1;
  }

  eventsAfterSeq(seq: number, limit: number): StoredEvent[] {
    return this.sql.exec(`SELECT * FROM rf_events WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit).map(toEvent);
  }

  latestEvents(limit: number): StoredEvent[] {
    return this.sql
      .exec(`SELECT * FROM rf_events ORDER BY seq DESC LIMIT ?`, limit)
      .map(toEvent)
      .reverse();
  }

  getRecentEvents(limit: number): StoredEvent[] {
    return this.latestEvents(limit);
  }

  lastEvent(): StoredEvent | null {
    const rows = this.sql.exec(`SELECT * FROM rf_events ORDER BY seq DESC LIMIT 1`);
    return rows[0] ? toEvent(rows[0]) : null;
  }

  /** Retention: keep the newest `keep` events. Returns how many were removed. */
  trimEvents(keep: number): number {
    const cutoff = this.sql.exec<{ seq: unknown }>(`SELECT seq FROM rf_events ORDER BY seq DESC LIMIT 1 OFFSET ?`, keep);
    if (!cutoff[0]) return 0;
    const seq = num(cutoff[0].seq);
    const before = num(this.sql.exec<{ n: unknown }>(`SELECT COUNT(*) AS n FROM rf_events WHERE seq <= ?`, seq)[0]?.n);
    this.sql.exec(`DELETE FROM rf_events WHERE seq <= ?`, seq);
    return before;
  }

  // ── Operations ─────────────────────────────────────────────────────────
  getOperation(opId: string): StoredOperation | null {
    const rows = this.sql.exec(`SELECT * FROM rf_operations WHERE op_id = ?`, opId);
    return rows[0] ? toOperation(rows[0]) : null;
  }

  putOperation(op: OperationRow): void {
    const now = this.now();
    const terminal = op.status === "succeeded" || op.status === "dead_lettered" || op.status === "canceled";
    const extra = op as Partial<StoredOperation>;
    this.sql.exec(
      `INSERT INTO rf_operations (op_id, path, revision, action, provider, status, attempt_count, last_error,
         next_attempt_at, correlation_id, provider_result, created_at, updated_at, completed_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(op_id) DO UPDATE SET path = excluded.path, revision = excluded.revision, action = excluded.action,
         provider = excluded.provider, status = excluded.status, attempt_count = excluded.attempt_count,
         last_error = excluded.last_error, next_attempt_at = excluded.next_attempt_at,
         correlation_id = excluded.correlation_id,
         provider_result = COALESCE(excluded.provider_result, rf_operations.provider_result),
         updated_at = excluded.updated_at,
         completed_at = COALESCE(rf_operations.completed_at, excluded.completed_at)`,
      op.opId,
      op.path,
      op.revision,
      op.action,
      op.provider ?? "",
      op.status,
      op.attemptCount ?? 0,
      op.lastError ?? null,
      op.nextAttemptAt ?? null,
      op.correlationId ?? "",
      extra.providerResult ? JSON.stringify(extra.providerResult) : null,
      now,
      now,
      terminal ? now : null,
    );
  }

  listOperations(
    options: PaginationOptions & { status?: string; action?: string; provider?: string },
  ): Paginated<StoredOperation> {
    const limit = clampLimit(options.limit);
    const clauses: string[] = [];
    const bindings: SqlValue[] = [];
    if (options.cursor) {
      const seq = this.sql.exec<{ seq: unknown }>(`SELECT seq FROM rf_operations WHERE op_id = ?`, options.cursor)[0];
      clauses.push(`seq > ?`);
      bindings.push(seq ? num(seq.seq) : Number.MAX_SAFE_INTEGER);
    }
    for (const [column, value] of [
      ["status", options.status],
      ["action", options.action],
      ["provider", options.provider],
    ] as const) {
      if (value) {
        clauses.push(`${column} = ?`);
        bindings.push(value);
      }
    }
    const where = clauses.length ? `WHERE ${clauses.join(" AND ")}` : "";
    const rows = this.sql
      .exec(`SELECT * FROM rf_operations ${where} ORDER BY seq LIMIT ?`, ...bindings, limit + 1)
      .map(toOperation);
    const items = rows.slice(0, limit);
    return { items, nextCursor: rows.length > limit ? items[items.length - 1]!.opId : null };
  }

  /** Earliest scheduled retry among pending operations, for the alarm. */
  nextOperationRetryAt(): string | null {
    const rows = this.sql.exec<{ at: unknown }>(
      `SELECT MIN(next_attempt_at) AS at FROM rf_operations WHERE status = 'pending' AND next_attempt_at IS NOT NULL`,
    );
    return nullableStr(rows[0]?.at);
  }

  // ── Counters ───────────────────────────────────────────────────────────
  private next(name: string): number {
    this.sql.exec(
      `INSERT INTO rf_counters (name, value) VALUES (?, 1) ON CONFLICT(name) DO UPDATE SET value = value + 1`,
      name,
    );
    return num(this.sql.exec<{ value: unknown }>(`SELECT value FROM rf_counters WHERE name = ?`, name)[0]?.value);
  }

  nextRevision(): string {
    return `rev_${this.next("revision")}`;
  }
  nextOperationId(): string {
    return `op_${this.next("operation")}`;
  }
  nextEventId(): string {
    return `evt_${this.next("event")}`;
  }
  nextEnvelopeId(): string {
    return `env_${this.next("envelope")}`;
  }

  // ── Writeback queue: the operations table is the queue ─────────────────
  enqueueWriteback(_item: WritebackItem): void {}

  getPendingWritebacks(): WritebackItem[] {
    return this.listOperations({ status: "pending", limit: MAX_PAGE }).items.map((op) => ({
      id: op.opId,
      workspaceId: this.workspaceId,
      path: op.path,
      revision: op.revision,
      correlationId: op.correlationId,
    }));
  }

  // ── Webhook envelopes ──────────────────────────────────────────────────
  getEnvelopeByDelivery(_workspaceId: string, provider: string, deliveryId: string): EnvelopeRow | null {
    const alias = this.sql.exec<{ envelope_id: unknown }>(
      `SELECT envelope_id FROM rf_envelope_aliases WHERE provider = ? AND delivery_id = ?`,
      provider,
      deliveryId,
    )[0];
    const rows = alias
      ? this.sql.exec(`SELECT * FROM rf_envelopes WHERE envelope_id = ?`, str(alias.envelope_id))
      : this.sql.exec(`SELECT * FROM rf_envelopes WHERE provider = ? AND delivery_id = ?`, provider, deliveryId);
    return rows[0] ? toEnvelope(rows[0], this.workspaceId) : null;
  }

  getEnvelope(envelopeId: string): EnvelopeRow | null {
    const rows = this.sql.exec(`SELECT * FROM rf_envelopes WHERE envelope_id = ?`, envelopeId);
    return rows[0] ? toEnvelope(rows[0], this.workspaceId) : null;
  }

  putEnvelope(envelope: EnvelopeRow): void {
    this.sql.exec(
      `INSERT INTO rf_envelopes (envelope_id, provider, delivery_id, received_at, headers, payload, correlation_id, status, attempt_count, last_error)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(envelope_id) DO UPDATE SET headers = excluded.headers, payload = excluded.payload,
         status = excluded.status, attempt_count = excluded.attempt_count, last_error = excluded.last_error`,
      envelope.envelopeId,
      envelope.provider,
      envelope.deliveryId,
      envelope.receivedAt,
      JSON.stringify(envelope.headers ?? {}),
      JSON.stringify(envelope.payload ?? {}),
      envelope.correlationId ?? "",
      envelope.status,
      envelope.attemptCount ?? 0,
      envelope.lastError ?? null,
    );
  }

  putEnvelopeDeliveryAlias(_workspaceId: string, provider: string, deliveryId: string, envelopeId: string): void {
    this.sql.exec(
      `INSERT OR REPLACE INTO rf_envelope_aliases (provider, delivery_id, envelope_id) VALUES (?, ?, ?)`,
      provider,
      deliveryId,
      envelopeId,
    );
  }

  listEnvelopes(options: EnvelopeQueryOptions): Paginated<EnvelopeRow> {
    const limit = clampLimit(options.limit);
    const clauses: string[] = [];
    const bindings: SqlValue[] = [];
    if (options.cursor) {
      const seq = this.sql.exec<{ seq: unknown }>(`SELECT seq FROM rf_envelopes WHERE envelope_id = ?`, options.cursor)[0];
      clauses.push(`seq > ?`);
      bindings.push(seq ? num(seq.seq) : Number.MAX_SAFE_INTEGER);
    }
    if (options.provider) {
      clauses.push(`provider = ?`);
      bindings.push(options.provider);
    }
    if (options.status) {
      clauses.push(`status = ?`);
      bindings.push(options.status);
    }
    const where = clauses.length ? `WHERE ${clauses.join(" AND ")}` : "";
    const rows = this.sql
      .exec(`SELECT * FROM rf_envelopes ${where} ORDER BY seq LIMIT ?`, ...bindings, limit + 1)
      .map((row) => toEnvelope(row, this.workspaceId));
    const items = rows.slice(0, limit);
    return { items, nextCursor: rows.length > limit ? items[items.length - 1]!.envelopeId : null };
  }

  // ── Metadata ───────────────────────────────────────────────────────────
  getMeta(key: string): string | null {
    return nullableStr(this.sql.exec<{ value: unknown }>(`SELECT value FROM rf_meta WHERE key = ?`, key)[0]?.value);
  }
  setMeta(key: string, value: string): void {
    this.sql.exec(`INSERT OR REPLACE INTO rf_meta (key, value) VALUES (?, ?)`, key, value);
  }
}

function clampLimit(limit: number | undefined): number {
  if (!limit || !Number.isFinite(limit) || limit <= 0) return DEFAULT_PAGE;
  return Math.min(Math.floor(limit), MAX_PAGE);
}

function parseJson<T>(value: unknown, fallback: T): T {
  if (typeof value !== "string" || value === "") return fallback;
  try {
    return JSON.parse(value) as T;
  } catch {
    return fallback;
  }
}

function toFile(row: Record<string, unknown>): StoredFile {
  const nodeType = str(row.node_type, "file") === "symlink" ? "symlink" : "file";
  return {
    path: str(row.path),
    revision: str(row.revision),
    contentType: str(row.content_type),
    content: str(row.content),
    encoding: str(row.encoding, "utf-8"),
    provider: str(row.provider),
    lastEditedAt: str(row.last_edited_at),
    semantics: parseJson<FileSemantics>(row.semantics, {}),
    contentHash: str(row.content_hash),
    size: num(row.size),
    blobSha256: nullableStr(row.blob_sha256),
    providerObjectId: str(row.provider_object_id),
    nodeType,
    target: str(row.target),
    mode: num(row.mode),
  };
}

export function toEvent(row: Record<string, unknown>): StoredEvent {
  return {
    seq: num(row.seq),
    eventId: str(row.event_id),
    type: str(row.type),
    path: str(row.path),
    revision: str(row.revision),
    contentHash: str(row.content_hash),
    origin: str(row.origin),
    provider: str(row.provider),
    correlationId: str(row.correlation_id),
    timestamp: str(row.timestamp),
    typeMetadata: parseJson<StoredEvent["typeMetadata"]>(row.type_metadata, null),
    aclPermissions: parseJson<string[] | null>(row.acl_permissions, null),
  };
}

/** Decoded bytes of stored content; null for invalid base64. */
export function decodeContent(content: string, encoding: string | undefined): Uint8Array | null {
  if (encoding === "base64") {
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(content)) return null;
    return Buffer.from(content, "base64");
  }
  return Buffer.from(content, "utf8");
}

/** Length of the stored, encoded content (what Go reports as TreeEntry.size). */
export function encodedLength(decodedBytes: number, encoding: string | undefined): number {
  return encoding === "base64" ? Math.ceil(decodedBytes / 3) * 4 : decodedBytes;
}

function toOperation(row: Record<string, unknown>): StoredOperation {
  return {
    opId: str(row.op_id),
    path: str(row.path),
    revision: str(row.revision),
    action: str(row.action),
    provider: str(row.provider),
    status: str(row.status),
    attemptCount: num(row.attempt_count),
    lastError: nullableStr(row.last_error),
    nextAttemptAt: nullableStr(row.next_attempt_at),
    correlationId: str(row.correlation_id),
    createdAt: str(row.created_at),
    updatedAt: str(row.updated_at),
    completedAt: nullableStr(row.completed_at),
    providerResult: parseJson<Record<string, unknown> | null>(row.provider_result, null),
  };
}

function toEnvelope(row: Record<string, unknown>, workspaceId: string): EnvelopeRow {
  return {
    envelopeId: str(row.envelope_id),
    workspaceId,
    provider: str(row.provider),
    deliveryId: str(row.delivery_id),
    receivedAt: str(row.received_at),
    headers: parseJson<Record<string, string>>(row.headers, {}),
    payload: parseJson<Record<string, unknown>>(row.payload, {}),
    correlationId: str(row.correlation_id),
    status: str(row.status),
    attemptCount: num(row.attempt_count),
    lastError: nullableStr(row.last_error),
  };
}
