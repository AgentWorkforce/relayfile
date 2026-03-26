/**
 * File CRUD operations.
 *
 * Extract from workspace.ts:
 *   - handleWriteFile → writeFile()
 *   - handleReadFile → readFile()
 *   - handleDeleteFile → deleteFile()
 *   - normalizeIfMatchHeader → normalizeIfMatch()
 *   - revision conflict detection
 *   - content encoding normalization/validation
 *   - provider inference from path
 */

import type { StorageAdapter, FileRow, FileSemantics } from "./storage.js";
import { normalizePath } from "./utils.js";
import { normalizeSemantics } from "./semantics.js";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface WriteFileRequest {
  path: string;
  ifMatch: string;
  content: string;
  contentType?: string;
  encoding?: string;
  semantics?: FileSemantics;
  correlationId?: string;
}

export interface WriteFileResult {
  opId: string;
  status: string;
  targetRevision: string;
  writeback: { provider: string; state: string };
}

export interface ConflictError {
  type: "conflict";
  expectedRevision: string;
  currentRevision: string;
  currentContentPreview?: string;
}

export interface DeleteFileRequest {
  path: string;
  ifMatch: string;
  correlationId?: string;
}

export type WriteResult =
  | { ok: true; result: WriteFileResult }
  | { ok: false; error: "not_found" | "missing_precondition" | "invalid_input" | ConflictError };

export type DeleteResult =
  | { ok: true; result: WriteFileResult }
  | { ok: false; error: "not_found" | "missing_precondition" | "invalid_input" | ConflictError };

// ---------------------------------------------------------------------------
// Functions — agent-3: extract from workspace.ts
// ---------------------------------------------------------------------------

export function normalizeIfMatch(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    return "";
  }
  if (trimmed === "*" || trimmed === "0") {
    return trimmed;
  }
  const weak = trimmed.startsWith("W/") ? trimmed.slice(2).trim() : trimmed;
  if (weak.startsWith('"') && weak.endsWith('"') && weak.length >= 2) {
    return weak.slice(1, -1);
  }
  return weak;
}

// Re-export normalizePath so existing importers (e.g. webhooks.ts) are not broken.
export { normalizePath };

export function inferProvider(path: string): string {
  const normalized = normalizePath(path).slice(1);
  const [provider = ""] = normalized.split("/", 1);
  return provider.trim().toLowerCase();
}

export function writeFile(storage: StorageAdapter, req: WriteFileRequest): WriteResult {
  if (!req.path.trim()) {
    return { ok: false, error: "invalid_input" };
  }

  const path = normalizePath(req.path);
  const ifMatch = normalizeIfMatch(req.ifMatch);
  if (!ifMatch) {
    return { ok: false, error: "missing_precondition" };
  }
  if (typeof req.content !== "string") {
    return { ok: false, error: "invalid_input" };
  }

  const encoding = normalizeEncoding(req.encoding);
  if (!encoding || !validateEncodedContent(req.content, encoding)) {
    return { ok: false, error: "invalid_input" };
  }
  if (encodedSize(req.content, encoding) > MAX_FILE_BYTES) {
    return { ok: false, error: "invalid_input" };
  }

  const existing = storage.getFile(path);
  if (!existing && ifMatch !== "0" && ifMatch !== "*") {
    return { ok: false, error: "not_found" };
  }
  if (existing && ifMatch !== "*" && ifMatch !== existing.revision) {
    return {
      ok: false,
      error: {
        type: "conflict",
        expectedRevision: ifMatch,
        currentRevision: existing.revision,
      },
    };
  }

  const revision = storage.nextRevision();
  const now = new Date().toISOString();
  const provider = existing?.provider || inferProvider(path);
  const correlationId = req.correlationId ?? "";
  const opId = storage.nextOperationId();

  storage.putFile({
    path,
    revision,
    contentType: req.contentType?.trim() || DEFAULT_CONTENT_TYPE,
    content: req.content,
    encoding,
    provider,
    lastEditedAt: now,
    semantics: normalizeSemantics(req.semantics),
  });

  storage.putOperation({
    opId,
    path,
    revision,
    action: "file_upsert",
    provider,
    status: "pending",
    attemptCount: 0,
    lastError: null,
    nextAttemptAt: null,
    correlationId,
  });

  storage.appendEvent({
    eventId: storage.nextEventId(),
    type: existing ? "file.updated" : "file.created",
    path,
    revision,
    origin: "agent_write",
    provider,
    correlationId,
    timestamp: now,
  });

  storage.enqueueWriteback({
    id: opId,
    workspaceId: storage.getWorkspaceId(),
    path,
    revision,
    correlationId,
  });

  return {
    ok: true,
    result: {
      opId,
      status: "queued",
      targetRevision: revision,
      writeback: {
        provider,
        state: "pending",
      },
    },
  };
}

export function readFile(storage: StorageAdapter, path: string): FileRow | null {
  if (!path.trim()) {
    return null;
  }
  return storage.getFile(normalizePath(path));
}

export function deleteFile(storage: StorageAdapter, req: DeleteFileRequest): DeleteResult {
  if (!req.path.trim()) {
    return { ok: false, error: "invalid_input" };
  }

  const path = normalizePath(req.path);
  const ifMatch = normalizeIfMatch(req.ifMatch);
  if (!ifMatch) {
    return { ok: false, error: "missing_precondition" };
  }

  const existing = storage.getFile(path);
  if (!existing) {
    return { ok: false, error: "not_found" };
  }
  if (ifMatch !== "*" && ifMatch !== existing.revision) {
    return {
      ok: false,
      error: {
        type: "conflict",
        expectedRevision: ifMatch,
        currentRevision: existing.revision,
      },
    };
  }

  const revision = storage.nextRevision();
  const now = new Date().toISOString();
  const correlationId = req.correlationId ?? "";
  const opId = storage.nextOperationId();

  storage.deleteFile(path);
  storage.putOperation({
    opId,
    path,
    revision,
    action: "file_delete",
    provider: existing.provider,
    status: "pending",
    attemptCount: 0,
    lastError: null,
    nextAttemptAt: null,
    correlationId,
  });
  storage.appendEvent({
    eventId: storage.nextEventId(),
    type: "file.deleted",
    path,
    revision,
    origin: "agent_write",
    provider: existing.provider,
    correlationId,
    timestamp: now,
  });
  storage.enqueueWriteback({
    id: opId,
    workspaceId: storage.getWorkspaceId(),
    path,
    revision,
    correlationId,
  });

  return {
    ok: true,
    result: {
      opId,
      status: "queued",
      targetRevision: revision,
      writeback: {
        provider: existing.provider,
        state: "pending",
      },
    },
  };
}

const DEFAULT_CONTENT_TYPE = "text/markdown";
const MAX_FILE_BYTES = 10 * 1024 * 1024;

function normalizeEncoding(value?: string): "utf-8" | "base64" | null {
  const encoding = value?.trim().toLowerCase() ?? "";
  if (!encoding || encoding === "utf-8" || encoding === "utf8") {
    return "utf-8";
  }
  if (encoding === "base64") {
    return "base64";
  }
  return null;
}

function validateEncodedContent(
  content: string,
  encoding: "utf-8" | "base64",
): boolean {
  if (encoding !== "base64") {
    return true;
  }

  try {
    atob(content);
    return true;
  } catch {
    return false;
  }
}

function encodedSize(content: string, encoding: "utf-8" | "base64"): number {
  if (encoding === "base64") {
    try {
      return Uint8Array.from(atob(content), (char) => char.charCodeAt(0))
        .byteLength;
    } catch {
      return content.length;
    }
  }
  return new TextEncoder().encode(content).byteLength;
}

