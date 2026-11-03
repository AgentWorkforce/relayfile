import { createHash } from "node:crypto";
import {
  applyWebhookEnvelope,
  filePermissionAllows,
  hasOversizedPathSegment,
  ingestWebhook,
  normalizeIfMatch,
  normalizePath,
  resolveFilePermissions,
  type TokenClaims,
} from "@relayfile/core";
import type { ActorSql } from "./db.js";
import { migrate } from "./schema.js";
import { hasAnyScopeGrant, scopeMatches, scopeMatchesPath } from "./scopes.js";
import {
  SqliteWorkspaceStorage,
  decodeContent,
  encodedLength,
  toEvent as rowToEvent,
  type StoredEvent,
  type StoredFile,
  type StoredOperation,
} from "./storage.js";
import {
  HYDRATE_HEADER,
  type ActorHttpRequest,
  type ActorHttpResponse,
  type CallerContext,
  type ContentBlobRef,
  type SocketMetadata,
} from "./types.js";

/**
 * The relayfile workspace API served from one actor's SQLite.
 *
 * Wire-compatible with the Go server (internal/httpapi) for the endpoints the
 * mount daemon, CLI, SDK and writeback workers use; see
 * packages/durable-actors/README.md for the supported surface. Routes the
 * actor does not serve answer 404, which every client treats as "feature
 * unavailable" (merge -> conflict artifact, export -> tree pull, ...).
 */

export const MAX_INLINE_FILE_BYTES = 10 * 1024 * 1024;
const DEFAULT_CONTENT_TYPE = "text/markdown";
const WS_INLINE_MAX = 1024 * 1024;
const EVENT_RETENTION = 100_000;
const EXPORT_MAX_FILES = 50_000;
const TREE_PAGE = 1000;
const MAINTENANCE_INTERVAL_MS = 15 * 60_000;
const DEDUP_TTL_SECONDS = 24 * 60 * 60;
const FANOUT_PAGE = 1000;

export interface SocketHandle {
  id: string;
  metadata: SocketMetadata;
  send(message: Record<string, unknown>): void;
}

export interface SocketPort {
  broadcast(message: Record<string, unknown>, tags?: string[]): void;
  connections(): Promise<SocketHandle[]>;
}

export interface AlarmPort {
  get(): number | null;
  set(timestampMs: number): void;
}

export interface RuntimeOptions {
  alarm?: AlarmPort;
  sockets?: SocketPort;
  now?: () => Date;
}

type Json = Record<string, unknown>;

class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly extra: Json = {},
    readonly headers: Record<string, string> = {},
  ) {
    super(message);
  }
}

interface FileInput {
  path: string;
  ifMatch: string | undefined;
  content: string;
  contentBlob?: ContentBlobRef;
  contentHash?: string;
  encoding?: string;
  contentType?: string;
  type?: string;
  target?: string;
  mode?: number;
  semantics?: unknown;
  contentIdentity?: { kind?: string; key?: string; ttlSeconds?: number };
}

interface WriteOutcome {
  opId: string;
  revision: string;
  contentType: string;
  provider: string;
  writebackState: string;
  status: "queued";
  contentIdentity?: FileInput["contentIdentity"];
}

export class WorkspaceRuntime {
  readonly storage: SqliteWorkspaceStorage;
  private readonly now: () => Date;
  private migrated = false;

  constructor(
    private readonly sql: ActorSql,
    readonly workspaceId: string,
    private readonly options: RuntimeOptions = {},
  ) {
    this.now = options.now ?? (() => new Date());
    this.storage = new SqliteWorkspaceStorage(sql, workspaceId, () => this.now().toISOString());
  }

  private ensureSchema(): void {
    if (this.migrated) return;
    migrate(this.sql);
    this.migrated = true;
  }

  // ─────────────────────────────────────────────────────────────────────────
  // HTTP
  // ─────────────────────────────────────────────────────────────────────────

  /**
   * Every entry point runs inside the actor's transaction; when it throws, the
   * transaction rolls back, including a migration applied in that call. Forget
   * the "migrated" flag then, so the next call migrates again instead of
   * running against tables that no longer exist.
   */
  private async guarded<T>(operation: () => Promise<T>): Promise<T> {
    try {
      return await operation();
    } catch (error) {
      this.migrated = false;
      this.fanOutFrom = null; // the events this call appended were rolled back
      throw error;
    }
  }

  /** Lowest seq whose successors still have to be pushed to sockets (set by handle, drained by flush). */
  private fanOutFrom: number | null = null;

  /**
   * Push the events appended by committed `handle` calls to connected /fs/ws
   * sockets. Call it AFTER the transaction around `handle` has committed, so
   * clients never see an event that a rollback then erases, and a failed
   * socket send can never roll a write back.
   */
  async flush(): Promise<void> {
    const from = this.fanOutFrom;
    this.fanOutFrom = null;
    if (from === null) return;
    try {
      await this.fanOut(from);
    } catch (error) {
      console.error(JSON.stringify({ event: "workspace.fanout_error", message: error instanceof Error ? error.message : String(error) }));
    }
  }

  async handle(request: ActorHttpRequest, caller: CallerContext): Promise<ActorHttpResponse> {
    return this.guarded(async () => {
      this.ensureSchema();
      const seqBefore = this.latestSeq();
      let response: ActorHttpResponse;
      try {
        response = this.route(request, caller);
      } catch (error) {
        if (error instanceof HttpError) {
          response = jsonResponse(
            error.status,
            { code: error.code, message: error.message, correlationId: caller.correlationId, ...error.extra },
            error.headers,
          );
        } else {
          throw error;
        }
      }
      if (this.latestSeq() > seqBefore) this.fanOutFrom = Math.min(this.fanOutFrom ?? seqBefore, seqBefore);
      this.scheduleMaintenance();
      return response;
    });
  }

  private route(request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    const method = request.method.toUpperCase();
    const segments = request.path.split("/").filter(Boolean);
    const query = new URLSearchParams(request.query);
    const key = `${method} /${segments.join("/")}`;

    if (method === "POST" && request.path === "/internal/webhook-envelopes") {
      if (!caller.internal) throw new HttpError(403, "forbidden", "internal route");
      return this.internalEnvelope(request, caller);
    }

    switch (key) {
      case "GET /fs/tree":
        return this.tree(query, caller);
      case "GET /fs/file":
        return this.readFile(query, caller);
      case "PUT /fs/file":
        return this.writeFileRoute(request, query, caller);
      case "DELETE /fs/file":
        return this.deleteFileRoute(request, query, caller);
      case "POST /fs/bulk":
        return this.bulkWrite(request, caller);
      case "POST /fs/bulk-read":
        return this.bulkRead(request, caller);
      case "GET /fs/events":
        return this.events(query, caller);
      case "GET /fs/export":
        return this.exportJson(query, caller);
      case "GET /fs/query":
        return this.queryFiles(query, caller);
      case "GET /ops":
        return this.listOps(query, caller);
      case "GET /sync/status":
        return this.syncStatus(query, caller);
      case "GET /writeback/pending":
        return this.writebackPending(caller);
      case "POST /webhooks/ingest":
        return this.webhookIngest(request, caller);
    }
    if (method === "GET" && segments.length === 2 && segments[0] === "ops") {
      return this.getOp(segments[1]!, caller);
    }
    if (method === "POST" && segments.length === 3 && segments[0] === "writeback" && segments[2] === "ack") {
      return this.writebackAck(segments[1]!, request, caller);
    }
    throw new HttpError(404, "not_found", "route not found");
  }

  // ── scope / ACL helpers ──────────────────────────────────────────────────

  private scopes(caller: CallerContext): Set<string> {
    return new Set(caller.scopes);
  }

  private requireScope(caller: CallerContext, scope: string, path?: string): void {
    const granted = this.scopes(caller);
    const ok = path === undefined ? scopeMatches(granted, scope) : scopeMatchesPath(granted, scope, path);
    if (!ok) throw new HttpError(403, "forbidden", `missing required scope: ${scope}`);
  }

  private claims(caller: CallerContext): TokenClaims {
    return { workspaceId: this.workspaceId, agentName: caller.agentName, scopes: this.scopes(caller) };
  }

  private aclAllows(caller: CallerContext, path: string, action: "read" | "write", includeTarget = true): boolean {
    return this.permissionsAllow(caller, resolveFilePermissions(this.storage, path, includeTarget), path, action);
  }

  private permissionsAllow(caller: CallerContext, permissions: string[], path: string, action: "read" | "write"): boolean {
    if (permissions.length === 0) return true;
    const granted = this.scopes(caller);
    return filePermissionAllows(permissions, this.workspaceId, this.claims(caller), {
      requestedPath: path,
      action,
      scopeMatches: (scope) => granted.has(scope) || scopeMatches(granted, scope),
    });
  }

  private canRead(caller: CallerContext, path: string): boolean {
    return scopeMatchesPath(this.scopes(caller), "fs:read", path) && this.aclAllows(caller, path, "read");
  }

  // ── files ────────────────────────────────────────────────────────────────

  private fileJson(file: StoredFile): Json {
    const out: Json = { path: file.path };
    if (file.nodeType === "symlink") out.type = "symlink";
    if (file.target) out.target = file.target;
    if (file.mode) out.mode = file.mode;
    out.revision = file.revision;
    if (file.contentHash) out.contentHash = file.contentHash;
    out.contentType = file.contentType;
    if (file.blobSha256) {
      out.contentBlob = { sha256: file.blobSha256, size: file.size } satisfies ContentBlobRef;
    } else {
      out.content = file.content;
    }
    if (file.encoding === "base64") out.encoding = "base64";
    if (file.provider) out.provider = file.provider;
    if (file.providerObjectId) out.providerObjectId = file.providerObjectId;
    if (file.lastEditedAt) out.lastEditedAt = file.lastEditedAt;
    out.semantics = file.semantics ?? {};
    return out;
  }

  private readFile(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const raw = query.get("path") ?? "";
    if (!raw) throw new HttpError(400, "bad_request", "missing path query");
    // Scope-check the resolved path: `/team/../secret` must be judged as `/secret`.
    const path = normalizePath(raw);
    this.requireScope(caller, "fs:read", path);
    const file = this.storage.getFile(path);
    if (!file) throw new HttpError(404, "not_found", "file not found");
    if (!this.aclAllows(caller, path, "read")) {
      throw new HttpError(403, "forbidden", "file access denied by permission policy");
    }
    const body = this.fileJson(file);
    return jsonResponse(200, body, { etag: file.revision, ...hydrateHeader(body) });
  }

  private writeFileRoute(request: ActorHttpRequest, query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const raw = query.get("path") ?? "";
    if (!raw) throw new HttpError(400, "bad_request", "missing path query");
    this.requireScope(caller, "fs:write", normalizePath(raw));
    const ifMatch = normalizeIfMatch(request.headers["if-match"] ?? "");
    if (!ifMatch) throw new HttpError(412, "precondition_failed", "missing If-Match header");
    const body = parseJsonBody(request);
    const input: FileInput = { ...(body as Partial<FileInput>), path: raw, ifMatch, content: contentField(body.content) };
    const outcome = this.writeOne(input, caller, { requireExplicitIfMatch: true });
    return jsonResponse(202, writeResultJson(outcome));
  }

  private deleteFileRoute(request: ActorHttpRequest, query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const raw = query.get("path") ?? "";
    if (!raw) throw new HttpError(400, "bad_request", "missing path query");
    const path = normalizePath(raw);
    this.requireScope(caller, "fs:write", path);
    const ifMatch = normalizeIfMatch(request.headers["if-match"] ?? "");
    if (!ifMatch) throw new HttpError(412, "precondition_failed", "missing If-Match header");
    const existing = this.storage.getFile(path);
    if (!existing) throw new HttpError(404, "not_found", "file not found");
    if (!this.aclAllows(caller, path, "write")) {
      throw new HttpError(403, "forbidden", "file access denied by permission policy");
    }
    if (ifMatch !== "*" && ifMatch !== existing.revision) throw this.conflict(ifMatch, existing, caller);
    const revision = this.storage.nextRevision();
    const now = this.now().toISOString();
    this.storage.deleteFile(path);
    const opId = this.createOperation(path, revision, "file_delete", existing.provider, caller);
    this.storage.appendEvent({
      eventId: this.storage.nextEventId(),
      type: "file.deleted",
      path,
      revision,
      origin: "agent_write",
      provider: existing.provider,
      correlationId: caller.correlationId,
      timestamp: now,
    });
    return jsonResponse(202, {
      opId,
      status: "queued",
      targetRevision: revision,
      writeback: { provider: existing.provider, state: this.opState(opId) },
    });
  }

  /**
   * Validate and apply one file write. Shared by PUT /fs/file and /fs/bulk.
   * Throws HttpError with the bulk error code in `extra.bulkCode`.
   */
  private writeOne(input: FileInput, caller: CallerContext, opts: { requireExplicitIfMatch: boolean }): WriteOutcome {
    if (!input.path || !String(input.path).trim()) throw bulkError(400, "invalid_path", "missing path");
    const path = normalizePath(String(input.path));
    if (path === "/" || hasOversizedPathSegment(path)) throw bulkError(400, "invalid_path", "invalid path");

    const encodingRaw = typeof input.encoding === "string" ? input.encoding.trim().toLowerCase() : "";
    let encoding: "utf-8" | "base64";
    if (encodingRaw === "" || encodingRaw === "utf-8" || encodingRaw === "utf8") encoding = "utf-8";
    else if (encodingRaw === "base64") encoding = "base64";
    else throw bulkError(400, "invalid_encoding", "unsupported encoding");

    const nodeType = input.type === undefined || input.type === "" || input.type === "file" ? "file" : input.type;
    if (nodeType !== "file" && nodeType !== "symlink") throw bulkError(400, "invalid_type", "unsupported file type");
    const target = typeof input.target === "string" ? input.target : "";
    if (nodeType === "symlink") {
      if (!target || encoding !== "utf-8" || input.content !== target || input.contentBlob) {
        throw bulkError(400, "invalid_type", "symlink requires a target equal to its utf-8 content");
      }
    } else if (target) {
      throw bulkError(400, "invalid_type", "regular files cannot carry a target");
    }

    const blob = input.contentBlob && typeof input.contentBlob.sha256 === "string" ? input.contentBlob : undefined;
    let contentHash: string;
    let size: number;
    if (blob) {
      if (!/^[a-f0-9]{64}$/.test(blob.sha256) || !Number.isSafeInteger(blob.size) || blob.size < 0) {
        throw bulkError(400, "invalid_content", "invalid content blob reference");
      }
      contentHash = blob.sha256;
      size = encodedLength(blob.size, encoding);
    } else {
      if (typeof input.content !== "string") throw bulkError(400, "invalid_input", "content must be a string");
      const bytes = decodeContent(input.content, encoding);
      if (!bytes) throw bulkError(400, "invalid_content", "content is not valid base64");
      const contentBytes = Buffer.byteLength(input.content);
      if (contentBytes > MAX_INLINE_FILE_BYTES) {
        throw bulkError(413, "payload_too_large", "file exceeds the inline size limit; configure a blob store");
      }
      contentHash =
        typeof input.contentHash === "string" && /^[a-f0-9]{64}$/.test(input.contentHash)
          ? input.contentHash
          : createHash("sha256").update(bytes).digest("hex");
      size = contentBytes; // Go: len(file.Content), the stored string's byte length
    }

    // Permissions first, before dedup hits or revision details can answer:
    // an existing file is judged by its own rules too, whatever If-Match says
    // (Go handleWriteFile / handleBulkWrite); a new file by its ancestors.
    const existing = this.storage.getFile(path);
    if (!this.aclAllows(caller, path, "write", existing !== null)) {
      throw bulkError(403, "forbidden", "file access denied by permission policy");
    }

    // Idempotent writes keyed by contentIdentity (webhook retries, fan-in).
    // A hit only answers for the same path it was recorded for.
    const identity = input.contentIdentity;
    if (identity && typeof identity.kind === "string" && typeof identity.key === "string" && identity.kind && identity.key) {
      const hit = this.sql.exec<{ result: unknown; op_id: unknown }>(
        `SELECT result, op_id FROM rf_dedup WHERE kind = ? AND key = ? AND expires_at > ?`,
        identity.kind,
        identity.key,
        Math.floor(this.now().getTime() / 1000),
      )[0];
      if (hit && typeof hit.result === "string" && this.storage.getOperation(String(hit.op_id))?.path === path) {
        return JSON.parse(hit.result) as WriteOutcome;
      }
    }

    const ifMatch = input.ifMatch === undefined ? "" : normalizeIfMatch(String(input.ifMatch));
    if (opts.requireExplicitIfMatch && !ifMatch) throw new HttpError(412, "precondition_failed", "missing If-Match header");
    if (ifMatch) {
      if (!existing && ifMatch !== "0" && ifMatch !== "*") throw bulkError(404, "not_found", "file not found");
      if (existing && ifMatch !== "*" && ifMatch !== existing.revision) throw this.conflict(ifMatch, existing, caller);
    }

    const revision = this.storage.nextRevision();
    const provider = existing?.provider || inferProvider(path);
    const semantics = normalizeSemanticsInput(input.semantics);
    const mode = Number.isInteger(input.mode) && (input.mode as number) > 0 ? (input.mode as number) : existing?.nodeType === "file" ? existing.mode : 0;
    const contentType =
      (typeof input.contentType === "string" && input.contentType.trim()) || DEFAULT_CONTENT_TYPE;

    this.storage.pendingFileMeta = {
      contentHash,
      size,
      blobSha256: blob ? blob.sha256 : null,
      nodeType,
      target,
      mode,
    };
    try {
      this.storage.putFile({
        path,
        revision,
        contentType,
        content: blob ? "" : input.content,
        encoding,
        provider,
        lastEditedAt: this.now().toISOString(),
        semantics: isEmptySemantics(semantics) && existing ? existing.semantics : semantics,
      });
    } finally {
      this.storage.pendingFileMeta = null;
    }
    const opId = this.createOperation(path, revision, "file_upsert", provider, caller);
    this.storage.appendEvent({
      eventId: this.storage.nextEventId(),
      type: existing ? "file.updated" : "file.created",
      path,
      revision,
      origin: "agent_write",
      provider,
      correlationId: caller.correlationId,
      timestamp: this.now().toISOString(),
    });
    const outcome: WriteOutcome = {
      opId,
      revision,
      contentType,
      provider,
      writebackState: this.opState(opId),
      status: "queued",
      ...(identity ? { contentIdentity: identity } : {}),
    };
    if (identity && typeof identity.kind === "string" && typeof identity.key === "string" && identity.kind && identity.key) {
      const ttl = Number.isInteger(identity.ttlSeconds) && (identity.ttlSeconds as number) > 0 ? (identity.ttlSeconds as number) : DEDUP_TTL_SECONDS;
      this.sql.exec(
        `INSERT OR REPLACE INTO rf_dedup (kind, key, op_id, result, expires_at) VALUES (?, ?, ?, ?, ?)`,
        identity.kind,
        identity.key,
        opId,
        JSON.stringify(outcome),
        Math.floor(this.now().getTime() / 1000) + ttl,
      );
    }
    return outcome;
  }

  /**
   * Record the writeback operation for an agent write. Providers that an
   * external worker handles stay `pending` until /writeback/{id}/ack; all
   * others complete immediately (the Go server's no-provider behaviour), so
   * mount outboxes never wait on a writeback nobody will perform.
   */
  private createOperation(path: string, revision: string, action: string, provider: string, caller: CallerContext): string {
    const opId = this.storage.nextOperationId();
    const external = caller.externalWritebackProviders ?? [];
    const pending = provider !== "" && (external.includes("*") || external.includes(provider));
    this.storage.putOperation({
      opId,
      path,
      revision,
      action,
      provider,
      status: pending ? "pending" : "succeeded",
      attemptCount: pending ? 0 : 1,
      lastError: null,
      nextAttemptAt: null,
      correlationId: caller.correlationId,
      ...(pending ? {} : { providerResult: { providerRevision: revision } }),
    } as unknown as StoredOperation);
    return opId;
  }

  /** 409 revision_conflict; the content preview only for callers who may read the file. */
  private conflict(expected: string, existing: StoredFile, caller: CallerContext): HttpError {
    const extra: Json = { expectedRevision: expected, currentRevision: existing.revision, bulkCode: "conflict" };
    if (!existing.blobSha256 && this.canRead(caller, existing.path)) extra.currentContentPreview = existing.content.slice(0, 4000);
    return new HttpError(409, "revision_conflict", "revision conflict", extra);
  }

  private opState(opId: string): string {
    return this.storage.getOperation(opId)?.status ?? "pending";
  }

  private bulkWrite(request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    // Coarse gate: any fs:write grant, path-narrowed ones included. Each file
    // is then checked against the caller's path scopes and its permissions.
    if (!hasAnyScopeGrant(this.scopes(caller), "fs:write")) {
      throw new HttpError(403, "forbidden", "missing required scope: fs:write");
    }
    const body = parseJsonBody(request);
    const files = Array.isArray(body.files) ? (body.files as unknown[]) : null;
    if (!files || files.length === 0) throw new HttpError(400, "bad_request", "missing files");
    const errors: Json[] = [];
    const results: Json[] = [];
    for (const entry of files) {
      const file: Json = entry && typeof entry === "object" && !Array.isArray(entry) ? (entry as Json) : {};
      const rawPath = typeof file.path === "string" ? file.path : "";
      try {
        if (rawPath && !scopeMatchesPath(this.scopes(caller), "fs:write", normalizePath(rawPath))) {
          throw bulkError(403, "forbidden", "missing required scope: fs:write");
        }
        const input: FileInput = {
          ...(file as Partial<FileInput>),
          path: rawPath,
          ifMatch: typeof file.ifMatch === "string" ? file.ifMatch : undefined,
          content: contentField(file.content),
        };
        const outcome = this.writeOne(input, caller, { requireExplicitIfMatch: false });
        results.push({
          path: normalizePath(rawPath),
          revision: outcome.revision,
          contentType: outcome.contentType,
          opId: outcome.opId,
          ...(outcome.contentIdentity ? { contentIdentity: outcome.contentIdentity } : {}),
          writeback: { provider: outcome.provider, state: outcome.writebackState },
        });
      } catch (error) {
        if (!(error instanceof HttpError)) throw error;
        const code = error.code === "revision_conflict" ? "conflict" : String(error.extra.bulkCode ?? error.code);
        errors.push({
          path: rawPath,
          code,
          message: error.message,
          ...(error.code === "revision_conflict"
            ? { expectedRevision: error.extra.expectedRevision, currentRevision: error.extra.currentRevision }
            : {}),
        });
      }
    }
    return jsonResponse(202, {
      written: results.length,
      errorCount: errors.length,
      errors,
      results,
      correlationId: caller.correlationId,
    });
  }

  private bulkRead(request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    const body = parseJsonBody(request);
    const unknown = Object.keys(body).filter((key) => key !== "paths");
    if (unknown.length) throw new HttpError(400, "bad_request", `unknown field "${unknown[0]}"`);
    const paths = body.paths;
    if (!Array.isArray(paths) || paths.length === 0 || paths.length > 32) {
      throw new HttpError(400, "bad_request", "paths must contain 1 to 32 entries");
    }
    let total = 0;
    const seen = new Set<string>();
    for (const path of paths) {
      if (typeof path !== "string" || !path.trim() || path === "/" || Buffer.byteLength(path) > 4096) {
        throw new HttpError(400, "bad_request", "invalid path");
      }
      total += Buffer.byteLength(path);
      if (seen.has(path)) throw new HttpError(400, "bad_request", "duplicate path");
      seen.add(path);
    }
    if (total > 32 * 1024) throw new HttpError(400, "bad_request", "paths too large");
    for (const path of paths as string[]) {
      if (!scopeMatchesPath(this.scopes(caller), "fs:read", normalizePath(path))) {
        throw new HttpError(403, "forbidden", "missing required scope: fs:read");
      }
    }
    const files = (paths as string[]).map((raw) => {
      const path = normalizePath(raw);
      const file = this.storage.getFile(path);
      if (!file) return { path: raw, error: { status: 404, code: "not_found", message: "file not found" } };
      if (!this.aclAllows(caller, path, "read")) {
        return { path: raw, error: { status: 403, code: "forbidden", message: "file access denied by permission policy" } };
      }
      return { ...this.fileJson(file), path: raw };
    });
    const out = { files };
    return jsonResponse(200, out, hydrateHeader(out));
  }

  // ── tree / query / export ────────────────────────────────────────────────

  private tree(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const base = normalizePath(query.get("path") || "/");
    this.requireScope(caller, "fs:read", base);
    const depth = boundedInt(query.get("depth"), 10, 1, 100);
    const cursor = query.get("cursor") ?? "";
    const baseDepth = base === "/" ? 0 : base.split("/").length - 1;

    // Walk every visible file under base once: count, synthesize directories, collect entries.
    const entries = new Map<string, Json>();
    let totalFiles = 0;
    let after = "";
    for (;;) {
      const batch = this.storage.listFilesUnder(base, after, 1000);
      if (batch.length === 0) break;
      after = batch[batch.length - 1]!.path;
      for (const file of batch) {
        if (!this.canRead(caller, file.path)) continue;
        totalFiles += 1;
        const parts = file.path.split("/").filter(Boolean);
        const relativeDepth = parts.length - baseDepth;
        // Directories between base and the file, up to depth.
        for (let d = baseDepth + 1; d < parts.length && d - baseDepth <= depth; d++) {
          const dirPath = `/${parts.slice(0, d).join("/")}`;
          if (!entries.has(dirPath)) entries.set(dirPath, { path: dirPath, type: "dir", revision: "dir" });
        }
        if (relativeDepth <= depth && file.path !== base) entries.set(file.path, this.treeEntry(file));
      }
    }
    const sorted = [...entries.values()].sort((a, b) => (String(a.path) < String(b.path) ? -1 : 1));
    let start = 0;
    if (cursor) {
      const index = sorted.findIndex((entry) => entry.path === cursor);
      if (index < 0) throw new HttpError(400, "invalid_cursor", "invalid tree cursor");
      start = index + 1;
    }
    const page = sorted.slice(start, start + TREE_PAGE);
    const nextCursor = start + TREE_PAGE < sorted.length ? String(page[page.length - 1]!.path) : null;
    return jsonResponse(200, { path: base, entries: page, nextCursor, ...(totalFiles ? { totalFiles } : {}) });
  }

  private treeEntry(file: StoredFile): Json {
    const sem = file.semantics ?? {};
    const out: Json = { path: file.path, type: file.nodeType };
    if (file.target) out.target = file.target;
    if (file.mode) out.mode = file.mode;
    out.revision = file.revision;
    if (file.contentHash) out.contentHash = file.contentHash;
    if (file.provider) out.provider = file.provider;
    if (file.providerObjectId) out.providerObjectId = file.providerObjectId;
    out.size = file.size;
    if (file.lastEditedAt) out.updatedAt = file.lastEditedAt;
    const counts: Array<[string, number]> = [
      ["propertyCount", Object.keys(sem.properties ?? {}).length],
      ["relationCount", sem.relations?.length ?? 0],
      ["permissionCount", sem.permissions?.length ?? 0],
      ["commentCount", sem.comments?.length ?? 0],
    ];
    for (const [name, count] of counts) if (count) out[name] = count;
    return out;
  }

  private exportJson(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const format = query.get("format") || "json";
    if (format !== "json") throw new HttpError(404, "not_found", `export format ${format} is not served by this backend`);
    const base = normalizePath(query.get("path") || "/");
    this.requireScope(caller, "fs:read", base);
    const out: Json[] = [];
    let after = "";
    for (;;) {
      const batch = this.storage.listFilesUnder(base, after, 1000);
      if (batch.length === 0) break;
      after = batch[batch.length - 1]!.path;
      for (const file of batch) {
        if (!this.canRead(caller, file.path)) continue;
        out.push(this.fileJson(file));
        if (out.length > EXPORT_MAX_FILES) throw new HttpError(413, "export_too_large", "too many files to export");
      }
    }
    return jsonResponse(200, out, hydrateHeader(out));
  }

  private queryFiles(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    const base = normalizePath(query.get("path") || "/");
    this.requireScope(caller, "fs:read", base);
    const limit = boundedInt(query.get("limit"), 100, 1, 1000);
    const provider = query.get("provider") ?? "";
    const relation = query.get("relation") ?? "";
    const permission = query.get("permission") ?? "";
    const comment = query.get("comment") ?? "";
    const properties: Array<[string, string]> = [];
    for (const [key, value] of query.entries()) if (key.startsWith("property.")) properties.push([key.slice(9), value]);
    let after = query.get("cursor") ?? "";
    const items: Json[] = [];
    let nextCursor: string | null = null;
    outer: for (;;) {
      const batch = this.storage.listFilesUnder(base, after, 1000);
      if (batch.length === 0) break;
      after = batch[batch.length - 1]!.path;
      for (const file of batch) {
        if (!this.canRead(caller, file.path)) continue;
        const sem = file.semantics ?? {};
        if (provider && file.provider !== provider) continue;
        if (relation && !(sem.relations ?? []).includes(relation)) continue;
        if (permission && !resolveFilePermissions(this.storage, file.path, true).includes(permission)) continue;
        if (comment && !(sem.comments ?? []).includes(comment)) continue;
        if (properties.some(([k, v]) => sem.properties?.[k] !== v)) continue;
        if (items.length === limit) {
          nextCursor = String(items[items.length - 1]!.path);
          break outer;
        }
        items.push({
          path: file.path,
          revision: file.revision,
          contentType: file.contentType,
          ...(file.provider ? { provider: file.provider } : {}),
          ...(file.providerObjectId ? { providerObjectId: file.providerObjectId } : {}),
          ...(file.lastEditedAt ? { lastEditedAt: file.lastEditedAt } : {}),
          size: file.size,
          ...(sem.properties ? { properties: sem.properties } : {}),
          ...(sem.relations ? { relations: sem.relations } : {}),
          ...(sem.permissions ? { permissions: sem.permissions } : {}),
          ...(sem.comments ? { comments: sem.comments } : {}),
        });
      }
    }
    return jsonResponse(200, { items, nextCursor });
  }

  // ── events ───────────────────────────────────────────────────────────────

  private eventJson(event: StoredEvent): Json {
    const out: Json = {
      eventId: event.eventId,
      type: event.type,
      path: event.path,
      revision: event.revision,
    };
    if (event.contentHash) out.contentHash = event.contentHash;
    out.origin = event.origin;
    if (event.provider) out.provider = event.provider;
    out.correlationId = event.correlationId;
    out.timestamp = event.timestamp;
    if (event.typeMetadata) out.typeMetadata = event.typeMetadata;
    return out;
  }

  private eventVisible(caller: CallerContext, event: StoredEvent): boolean {
    if (event.type === "file.deleted") {
      // The file is gone, so its own rules come from the snapshot taken at
      // delete time. No snapshot = cannot rule out a file-level deny: hide it
      // (Go eventVisibleToClaims fails closed the same way).
      if (!event.path || event.aclPermissions === null) return false;
      return (
        scopeMatchesPath(this.scopes(caller), "fs:read", event.path) &&
        this.permissionsAllow(caller, event.aclPermissions, event.path, "read") &&
        this.aclAllows(caller, event.path, "read", false)
      );
    }
    if (!event.path) return true; // pathless control events (sync.reconcile)
    if (!scopeMatchesPath(this.scopes(caller), "fs:read", event.path)) return false;
    if (event.aclPermissions !== null && !this.permissionsAllow(caller, event.aclPermissions, event.path, "read")) return false;
    return this.aclAllows(caller, event.path, "read");
  }

  private events(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    // Any fs:read grant may read the feed; every event is filtered by eventVisible.
    if (!hasAnyScopeGrant(this.scopes(caller), "fs:read")) {
      throw new HttpError(403, "forbidden", "missing required scope: fs:read");
    }
    const limit = boundedInt(query.get("limit"), 200, 1, 1000);
    const provider = query.get("provider") ?? "";
    const direction = query.get("direction") || "asc";
    if (direction !== "asc" && direction !== "desc") throw new HttpError(400, "bad_request", "direction must be asc or desc");
    const cursor = query.get("cursor") ?? "";

    let bound: number | null = null;
    if (cursor) {
      const seq = this.storage.eventSeq(cursor);
      if (seq >= 0) bound = seq;
      else if (eventNumber(cursor) !== null && eventNumber(cursor)! < this.oldestEventNumber()) {
        // The cursor fell out of retention: tell the client to resync instead of silently skipping events.
        throw new HttpError(410, "cursor_expired", "event cursor expired", { action: "full_resync" });
      }
    }
    const items: StoredEvent[] = [];
    let scan = bound;
    let exhausted = false;
    while (items.length <= limit && !exhausted) {
      const rows =
        direction === "asc"
          ? this.sql
              .exec(
                `SELECT * FROM rf_events WHERE seq > ? ${provider ? "AND provider = ?" : ""} ORDER BY seq ASC LIMIT 500`,
                scan ?? 0,
                ...(provider ? [provider] : []),
              )
          : this.sql.exec(
              `SELECT * FROM rf_events WHERE seq < ? ${provider ? "AND provider = ?" : ""} ORDER BY seq DESC LIMIT 500`,
              scan ?? Number.MAX_SAFE_INTEGER,
              ...(provider ? [provider] : []),
            );
      if (rows.length < 500) exhausted = true;
      for (const row of rows) {
        const event = rowToEvent(row);
        scan = event.seq;
        if (!this.eventVisible(caller, event)) continue;
        items.push(event);
        if (items.length > limit) break;
      }
    }
    const page = items.slice(0, limit);
    const nextCursor = items.length > limit ? page[page.length - 1]!.eventId : null;
    return jsonResponse(200, { events: page.map((event) => this.eventJson(event)), nextCursor });
  }

  private latestSeq(): number {
    const row = this.sql.exec<{ seq: unknown }>(`SELECT MAX(seq) AS seq FROM rf_events`)[0];
    return Number(row?.seq ?? 0) || 0;
  }

  private oldestEventNumber(): number {
    const row = this.sql.exec<{ id: unknown }>(`SELECT event_id AS id FROM rf_events ORDER BY seq ASC LIMIT 1`)[0];
    return row ? eventNumber(String(row.id)) ?? 0 : 0;
  }

  // ── ops / writeback / sync ───────────────────────────────────────────────

  private opJson(op: StoredOperation): Json {
    const out: Json = { opId: op.opId };
    if (op.path) out.path = op.path;
    if (op.revision) out.revision = op.revision;
    if (op.action) out.action = op.action;
    if (op.provider) out.provider = op.provider;
    out.status = op.status;
    out.attemptCount = op.attemptCount;
    if (op.nextAttemptAt) out.nextAttemptAt = op.nextAttemptAt;
    if (op.lastError) out.lastError = op.lastError;
    if (op.providerResult) out.providerResult = op.providerResult;
    if (op.correlationId) out.correlationId = op.correlationId;
    if (op.createdAt) out.createdAt = op.createdAt;
    if (op.updatedAt) out.updatedAt = op.updatedAt;
    if (op.completedAt) out.completedAt = op.completedAt;
    return out;
  }

  private getOp(opId: string, caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "ops:read");
    const op = this.storage.getOperation(opId);
    if (!op) throw new HttpError(404, "not_found", "operation not found");
    return jsonResponse(200, this.opJson(op));
  }

  private listOps(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "ops:read");
    const limit = boundedInt(query.get("limit"), 100, 1, 1000);
    const clauses: string[] = [];
    const bindings: Array<string | number> = [];
    const cursor = query.get("cursor");
    if (cursor) {
      const row = this.sql.exec<{ seq: unknown }>(`SELECT seq FROM rf_operations WHERE op_id = ?`, cursor)[0];
      if (!row) throw new HttpError(400, "bad_request", "invalid cursor");
      clauses.push("seq < ?");
      bindings.push(Number(row.seq));
    }
    for (const column of ["status", "action", "provider"]) {
      const value = query.get(column);
      if (value) {
        clauses.push(`${column} = ?`);
        bindings.push(column === "provider" ? value.trim().toLowerCase() : value);
      }
    }
    const where = clauses.length ? `WHERE ${clauses.join(" AND ")}` : "";
    const rows = this.sql.exec(`SELECT op_id FROM rf_operations ${where} ORDER BY seq DESC LIMIT ?`, ...bindings, limit + 1);
    const ops = rows.slice(0, limit).map((row) => this.storage.getOperation(String(row.op_id))!);
    const nextCursor = rows.length > limit ? ops[ops.length - 1]!.opId : null;
    return jsonResponse(200, { items: ops.map((op) => this.opJson(op)), nextCursor });
  }

  private writebackPending(caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "sync:read");
    // Every pending item, oldest first, like the Go server (the endpoint has no cursor).
    const rows = this.sql.exec(
      `SELECT op_id, path, revision, correlation_id FROM rf_operations WHERE status IN ('pending', 'running') ORDER BY seq ASC`,
    );
    const items = rows.map((row) => ({
      id: String(row.op_id),
      workspaceId: this.workspaceId,
      path: String(row.path),
      revision: String(row.revision),
      correlationId: String(row.correlation_id ?? ""),
    }));
    return jsonResponse(200, items);
  }

  private writebackAck(itemId: string, request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "sync:trigger");
    const body = parseJsonBody(request);
    if (typeof body.success !== "boolean") throw new HttpError(400, "bad_request", "success must be a boolean");
    const op = this.storage.getOperation(itemId);
    if (!op) throw new HttpError(404, "not_found", "writeback item not found");
    const success = body.success;
    const providerResult: Json = {};
    if (body.providerResult && typeof body.providerResult === "object" && !Array.isArray(body.providerResult)) {
      Object.assign(providerResult, body.providerResult);
      delete providerResult.providerRevision;
    }
    if (typeof body.externalId === "string" && body.externalId) providerResult.externalId = body.externalId;
    providerResult.providerRevision = op.revision;
    this.storage.putOperation({
      ...op,
      status: success ? "succeeded" : "dead_lettered",
      lastError: success ? null : typeof body.error === "string" ? body.error : "writeback failed",
      nextAttemptAt: null,
      ...(success ? { providerResult } : {}),
    } as StoredOperation);
    this.storage.appendEvent({
      eventId: this.storage.nextEventId(),
      type: success ? "writeback.succeeded" : "writeback.failed",
      path: op.path,
      revision: op.revision,
      origin: "system",
      provider: op.provider,
      correlationId: caller.correlationId,
      timestamp: this.now().toISOString(),
    });
    return jsonResponse(200, { status: "acknowledged", id: op.opId, correlationId: caller.correlationId, success });
  }

  private syncStatus(query: URLSearchParams, caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "sync:read");
    const filter = query.get("provider") ?? "";
    const rows = this.sql.exec(
      `SELECT provider,
              SUM(CASE WHEN status = 'dead_lettered' THEN 1 ELSE 0 END) AS dead_ops
         FROM rf_operations WHERE provider != '' GROUP BY provider`,
    );
    const envelopeRows = this.sql.exec(
      `SELECT provider, MAX(received_at) AS watermark,
              SUM(CASE WHEN status = 'dead_lettered' THEN 1 ELSE 0 END) AS dead_envelopes
         FROM rf_envelopes GROUP BY provider`,
    );
    const providers = new Map<string, Json>();
    for (const row of rows) {
      const provider = String(row.provider);
      providers.set(provider, { provider, status: "healthy", deadLetteredOps: Number(row.dead_ops ?? 0) });
    }
    for (const row of envelopeRows) {
      const provider = String(row.provider);
      const entry = providers.get(provider) ?? { provider, status: "healthy" };
      if (row.watermark) entry.watermarkTs = String(row.watermark);
      entry.deadLetteredEnvelopes = Number(row.dead_envelopes ?? 0);
      providers.set(provider, entry);
    }
    const list = [...providers.values()].filter((p) => !filter || p.provider === filter);
    return jsonResponse(200, { workspaceId: this.workspaceId, providers: list });
  }

  // ── provider ingestion ───────────────────────────────────────────────────

  private webhookIngest(request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    this.requireScope(caller, "sync:trigger");
    const body = parseJsonBody(request);
    for (const field of ["provider", "event_type", "path"]) {
      if (typeof body[field] !== "string" || !(body[field] as string).trim()) {
        throw new HttpError(400, "bad_request", `missing ${field}`);
      }
    }
    const result = ingestWebhook(
      this.storage,
      {
        provider: body.provider as string,
        eventType: body.event_type as string,
        path: body.path as string,
        data: body.data && typeof body.data === "object" ? (body.data as Json) : undefined,
        deliveryId: typeof body.delivery_id === "string" ? body.delivery_id : undefined,
        timestamp: typeof body.timestamp === "string" ? body.timestamp : undefined,
        headers: body.headers && typeof body.headers === "object" ? (body.headers as Record<string, string>) : undefined,
        correlationId: caller.correlationId,
      },
      { requireSignature: false, generateEnvelopeId: () => this.storage.nextEnvelopeId(), coalesceWindowMs: 0 },
    );
    if (result.status === "invalid_input") throw new HttpError(400, "bad_request", "invalid webhook envelope");
    if (result.status === "queued") this.processEnvelope(result.envelopeId, caller);
    return jsonResponse(202, { status: "queued", id: result.envelopeId, correlationId: result.correlationId || caller.correlationId });
  }

  private internalEnvelope(request: ActorHttpRequest, caller: CallerContext): ActorHttpResponse {
    const body = parseJsonBody(request);
    const provider = typeof body.provider === "string" ? body.provider.trim() : "";
    if (!provider) throw new HttpError(400, "bad_request", "missing provider");
    // No deliveryId: key the envelope on its HMAC signature (covers timestamp
    // + body), so the same signed request is applied once across replicas.
    const signature = (request.headers["x-relay-signature"] ?? "").trim().toLowerCase();
    const deliveryId =
      typeof body.deliveryId === "string" && body.deliveryId
        ? body.deliveryId
        : signature
          ? `sig:${signature}`
          : `${provider}:${this.storage.nextEnvelopeId()}`;
    const existing = this.storage.getEnvelopeByDelivery(this.workspaceId, provider, deliveryId);
    if (existing) return jsonResponse(202, { status: "queued", id: existing.envelopeId, correlationId: existing.correlationId });
    const envelopeId = typeof body.envelopeId === "string" && body.envelopeId ? body.envelopeId : this.storage.nextEnvelopeId();
    const correlationId = typeof body.correlationId === "string" && body.correlationId ? body.correlationId : caller.correlationId;
    this.storage.putEnvelope({
      envelopeId,
      workspaceId: this.workspaceId,
      provider,
      deliveryId,
      receivedAt: typeof body.receivedAt === "string" && body.receivedAt ? body.receivedAt : this.now().toISOString(),
      headers: (body.headers as Record<string, string>) ?? {},
      payload: (body.payload as Json) ?? {},
      correlationId,
      status: "queued",
      attemptCount: 0,
      lastError: null,
    });
    this.storage.putEnvelopeDeliveryAlias(this.workspaceId, provider, deliveryId, envelopeId);
    this.processEnvelope(envelopeId);
    return jsonResponse(202, { status: "queued", id: envelopeId, correlationId });
  }

  /**
   * Apply one queued envelope now, inside the same actor transaction.
   * Bearer-submitted envelopes (`caller` given) may only create, overwrite or
   * delete files the caller could write itself: file permission rules (an
   * existing file's own rules included) gate the mutation, and a denied one is
   * dead-lettered as `permission_denied`. HMAC-verified internal envelopes are
   * trusted provider sync and are not gated. Webhook semantics never carry
   * permissions (core strips them).
   */
  private processEnvelope(envelopeId: string, caller?: CallerContext): void {
    const envelope = this.storage.getEnvelope(envelopeId);
    if (!envelope || envelope.status !== "queued") return;
    let denied = false;
    const result = applyWebhookEnvelope(this.storage, envelope, {
      now: () => this.now().toISOString(),
      ...(caller
        ? {
            isPathWriteAllowed: (path: string) => {
              const allowed = this.aclAllows(caller, path, "write", this.storage.getFile(normalizePath(path)) !== null);
              if (!allowed) denied = true;
              return allowed;
            },
          }
        : {}),
    });
    this.storage.putEnvelope({
      ...envelope,
      status: result.status === "rejected" || denied ? "dead_lettered" : "processed",
      attemptCount: envelope.attemptCount + 1,
      lastError: denied ? "permission_denied" : (result.reason ?? null),
    });
  }

  // ─────────────────────────────────────────────────────────────────────────
  // Maintenance (actor alarm)
  // ─────────────────────────────────────────────────────────────────────────

  private scheduleMaintenance(): void {
    const alarm = this.options.alarm;
    if (!alarm) return;
    const due = this.now().getTime() + MAINTENANCE_INTERVAL_MS;
    const current = alarm.get();
    if (current === null || current > due) alarm.set(due);
  }

  /** Event retention and dedup expiry. Re-arms while the workspace has data. */
  async maintain(): Promise<void> {
    return this.guarded(async () => this.maintainNow());
  }

  private maintainNow(): void {
    this.ensureSchema();
    this.storage.trimEvents(EVENT_RETENTION);
    this.sql.exec(`DELETE FROM rf_dedup WHERE expires_at <= ?`, Math.floor(this.now().getTime() / 1000));
    this.sql.exec(
      `DELETE FROM rf_envelopes WHERE status = 'processed' AND received_at < ?`,
      new Date(this.now().getTime() - 7 * 24 * 60 * 60_000).toISOString(),
    );
    this.sql.exec(`DELETE FROM rf_envelope_aliases WHERE envelope_id NOT IN (SELECT envelope_id FROM rf_envelopes)`);
    if (this.storage.countFiles() > 0) this.options.alarm?.set(this.now().getTime() + MAINTENANCE_INTERVAL_MS);
  }

  // ─────────────────────────────────────────────────────────────────────────
  // WebSocket (/fs/ws)
  // ─────────────────────────────────────────────────────────────────────────

  async connect(socket: SocketHandle, setTags: (tags: string[]) => void): Promise<void> {
    return this.guarded(async () => this.connectNow(socket, setTags));
  }

  private connectNow(socket: SocketHandle, setTags: (tags: string[]) => void): void {
    this.ensureSchema();
    setTags(["fs"]);
    const meta = socket.metadata;
    const caller = socketCaller(meta);
    if (meta.fromNow) return;
    let events: StoredEvent[];
    if (meta.cursor) {
      const seq = this.storage.eventSeq(meta.cursor);
      if (seq < 0) return; // unknown cursor: no catch-up (Go behaviour)
      events = [];
      let after = seq;
      for (;;) {
        const batch = this.storage.eventsAfterSeq(after, 100);
        if (batch.length === 0) break;
        events.push(...batch);
        after = batch[batch.length - 1]!.seq;
      }
    } else {
      events = this.storage.latestEvents(100);
    }
    for (const event of events) {
      const frame = this.socketFrame(event, caller, meta);
      if (frame) socket.send(frame);
    }
  }

  message(socket: SocketHandle, message: Record<string, unknown>): void {
    if (message?.type === "ping") socket.send({ type: "pong", timestamp: this.now().toISOString() });
  }

  private socketFrame(event: StoredEvent, caller: CallerContext, meta: SocketMetadata): Json | null {
    if (event.path && !pathFilterMatches(meta.paths, event.path)) return null;
    if (!this.eventVisible(caller, event)) return null;
    const frame = this.eventJson(event);
    if ((event.type === "file.created" || event.type === "file.updated") && event.path) {
      const file = this.storage.getFile(event.path);
      if (
        file &&
        file.revision === event.revision &&
        !file.blobSha256 &&
        file.content.length <= WS_INLINE_MAX &&
        this.canRead(caller, event.path)
      ) {
        frame.contentType = file.contentType;
        frame.content = file.content;
        if (file.encoding === "base64") frame.encoding = "base64";
        frame.inlineContent = true;
        if (file.contentHash) frame.contentHash = file.contentHash;
      }
    }
    return frame;
  }

  /** Push events appended by this call to connected /fs/ws sockets. */
  private async fanOut(seqBefore: number): Promise<void> {
    const sockets = this.options.sockets;
    if (!sockets) return;
    let batch = this.storage.eventsAfterSeq(seqBefore, FANOUT_PAGE);
    if (batch.length === 0) return;
    const connections = await sockets.connections();
    // Page through every event this call appended (a large bulk write can append more than one page).
    while (batch.length > 0) {
      for (const socket of connections) {
        const caller = socketCaller(socket.metadata);
        try {
          for (const event of batch) {
            const frame = this.socketFrame(event, caller, socket.metadata);
            if (frame) socket.send(frame);
          }
        } catch {
          // One broken socket must not starve the others; it reconnects with its cursor.
        }
      }
      if (batch.length < FANOUT_PAGE) break;
      batch = this.storage.eventsAfterSeq(batch[batch.length - 1]!.seq, FANOUT_PAGE);
    }
  }
}

// ───────────────────────────────────────────────────────────────────────────
// helpers
// ───────────────────────────────────────────────────────────────────────────

function socketCaller(meta: SocketMetadata): CallerContext {
  return { agentName: meta.agentName, scopes: meta.scopes, correlationId: "" };
}

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): ActorHttpResponse {
  return { status, headers: { "content-type": "application/json", ...headers }, body: JSON.stringify(body) };
}

function hydrateHeader(body: unknown): Record<string, string> {
  return JSON.stringify(body).includes('"contentBlob"') ? { [HYDRATE_HEADER]: "1" } : {};
}

function parseJsonBody(request: ActorHttpRequest): Json {
  if (!request.body) return {};
  try {
    const parsed = JSON.parse(request.body) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("not an object");
    return parsed as Json;
  } catch {
    throw new HttpError(400, "bad_request", "invalid json body");
  }
}

/** Missing content is an empty file (Go decodes it as ""); any other non-string is kept so writeOne answers 400. */
function contentField(value: unknown): string {
  return value === undefined || value === null ? "" : (value as string);
}

function bulkError(status: number, code: string, message: string): HttpError {
  const httpCode = status === 400 ? "bad_request" : status === 403 ? "forbidden" : status === 404 ? "not_found" : code;
  return new HttpError(status, httpCode, message, { bulkCode: code });
}


function writeResultJson(outcome: WriteOutcome): Json {
  return {
    opId: outcome.opId,
    status: "queued",
    targetRevision: outcome.revision,
    writeback: { provider: outcome.provider, state: outcome.writebackState },
  };
}

function boundedInt(raw: string | null, fallback: number, min: number, max: number): number {
  if (raw === null || raw === "") return fallback;
  const value = Number.parseInt(raw, 10);
  if (!Number.isFinite(value) || value < min) return fallback;
  return Math.min(value, max);
}

function inferProvider(path: string): string {
  const [first = ""] = path.slice(1).split("/", 1);
  return first.trim().toLowerCase();
}

function normalizeSemanticsInput(raw: unknown): { properties?: Record<string, string>; relations?: string[]; permissions?: string[]; comments?: string[] } {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const input = raw as Record<string, unknown>;
  const out: { properties?: Record<string, string>; relations?: string[]; permissions?: string[]; comments?: string[] } = {};
  if (input.properties && typeof input.properties === "object" && !Array.isArray(input.properties)) {
    const props: Record<string, string> = {};
    for (const [key, value] of Object.entries(input.properties as Record<string, unknown>)) {
      if (!key.trim()) continue;
      props[key] = typeof value === "string" ? value : JSON.stringify(value);
    }
    if (Object.keys(props).length) out.properties = props;
  }
  for (const field of ["relations", "permissions", "comments"] as const) {
    const values = input[field];
    if (Array.isArray(values)) {
      const list = [...new Set(values.filter((v): v is string => typeof v === "string" && v.trim() !== "").map((v) => v.trim()))];
      if (list.length) out[field] = list;
    }
  }
  return out;
}

function isEmptySemantics(sem: ReturnType<typeof normalizeSemanticsInput>): boolean {
  return !sem.properties && !sem.relations && !sem.permissions && !sem.comments;
}

function eventNumber(eventId: string): number | null {
  const match = /^evt_(\d+)$/.exec(eventId);
  return match ? Number(match[1]) : null;
}

/** /fs/ws path filters: exact, `*` = one segment, trailing `**` = any depth; else equal segment counts. */
export function pathFilterMatches(filters: string[], path: string): boolean {
  if (!filters || filters.length === 0) return true;
  const target = path.split("/").filter(Boolean);
  return filters.some((filter) => {
    if (filter === path) return true;
    const pattern = filter.split("/").filter(Boolean);
    for (let i = 0; i < pattern.length; i++) {
      const part = pattern[i]!;
      if (part === "**" && i === pattern.length - 1) return target.length >= i;
      if (i >= target.length) return false;
      if (part !== "*" && part !== target[i]) return false;
    }
    return pattern.length === target.length;
  });
}
