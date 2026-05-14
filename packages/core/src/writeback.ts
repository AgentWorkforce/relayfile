/**
 * Writeback lifecycle.
 *
 * Extract from workspace.ts:
 *   - pending writeback listing (filtered by op status)
 *   - writeback acknowledgment
 *   - dead-letter handling
 *   - retry logic
 */

import type {
  OperationRow,
  Paginated,
  StorageAdapter,
  WritebackItem,
} from "./storage.js";
import { createEvent } from "./events.js";

export const MAX_WRITEBACK_ATTEMPTS = 3;
export const WRITEBACK_RETRY_DELAY_MS = 30_000;

export type WritebackListState =
  | "pending"
  | "succeeded"
  | "failed"
  | "dead_lettered";

export interface WritebackListItem extends WritebackItem {
  state: WritebackListState;
  attemptCount: number;
  lastError: string | null;
  nextAttemptAt: string | null;
  /** Provider slug, copied from OperationRow.provider. */
  provider: string;
}

export interface ListWritebacksOptions {
  state: WritebackListState;
  cursor?: string;
  limit?: number;
}

export type DeadLetterErrorCode =
  | "schema_violation"
  | "provider_4xx"
  | "provider_5xx_exhausted"
  | "timeout";

export interface DeadLetterError {
  code: DeadLetterErrorCode;
  message: string;
  providerStatus?: number;
  providerResponse?: Record<string, unknown> | unknown[];
  attempts: number;
  firstAttemptAt: string;
  lastAttemptAt: string;
  opId: string;
}

export interface WritebackResourceSchemaRef {
  /** Provider slug, e.g. "linear". */
  provider: string;
  /** Resource slug, e.g. "comments". */
  resource: string;
  /** Identifier the daemon uses to load the schema. */
  schemaId: string;
}

export interface DispatchWritebackCallbacks {
  send(item: WritebackItem): void;
  onRetryScheduled?(item: WritebackItem, nextAttemptAt: string): void;
  now?(): string;
}

export function listWritebacks(
  storage: StorageAdapter,
  options: ListWritebacksOptions,
): Paginated<WritebackListItem> {
  const state = normalizeWritebackListState(options.state);
  const limit = clampLimit(options.limit);
  const sortedItems = listAllOperationsForState(storage, state)
    .filter((op) => op.status === state)
    .map((op) => toWritebackListItem(storage, op, state))
    .sort(compareWritebackListItems);
  let start = 0;
  if (options.cursor) {
    const cursorIndex = sortedItems.findIndex((item) => item.id === options.cursor);
    if (cursorIndex < 0) {
      return { items: [], nextCursor: null };
    }
    start = cursorIndex + 1;
  }

  const items = sortedItems.slice(start, start + limit);

  return {
    items,
    nextCursor:
      start + limit < sortedItems.length
        ? (items[items.length - 1]?.id ?? null)
        : null,
  };
}

export function normalizeDeadLetterError(input: unknown): DeadLetterError {
  if (!isRecord(input)) {
    throw new TypeError("dead-letter error must be an object");
  }

  const code = normalizeDeadLetterErrorCode(input.code);
  const message = requiredString(input.message, "message").trim();
  const attempts = requiredPositiveInteger(input.attempts, "attempts");
  const firstAttemptAt = requiredIsoTimestamp(
    input.firstAttemptAt,
    "firstAttemptAt",
  );
  const lastAttemptAt = requiredIsoTimestamp(input.lastAttemptAt, "lastAttemptAt");
  const opId = requiredString(input.opId, "opId").trim();

  if (!message) {
    throw new TypeError("dead-letter error message is required");
  }
  if (!opId) {
    throw new TypeError("dead-letter error opId is required");
  }

  const normalized: DeadLetterError = {
    code,
    message,
    attempts,
    firstAttemptAt,
    lastAttemptAt,
    opId,
  };

  if (input.providerStatus !== undefined) {
    normalized.providerStatus = optionalProviderStatus(input.providerStatus);
  }
  if (input.providerResponse !== undefined) {
    normalized.providerResponse = optionalProviderResponse(
      input.providerResponse,
    );
  }

  // Keep this shape aligned with relayfile/schemas/relay/dead-letter-error.schema.json.
  return Object.freeze(normalized);
}

export function resolveWritebackSchemaRef(
  storage: StorageAdapter,
  writebackPath: string,
): WritebackResourceSchemaRef | null {
  const parsed = parseWritebackPath(writebackPath);
  if (!parsed) {
    return null;
  }

  const registeredSchemaId = storage.getWritebackSchemaId?.(
    parsed.provider,
    parsed.resource,
  );
  if (registeredSchemaId === null) {
    return null;
  }

  return {
    provider: parsed.provider,
    resource: parsed.resource,
    schemaId:
      registeredSchemaId?.trim() ||
      `${parsed.provider}/${parsed.resource}.schema.json`,
  };
}

export function getPendingWritebacks(storage: StorageAdapter): WritebackItem[] {
  const items: WritebackItem[] = [];
  let cursor: string | undefined;

  do {
    const page = storage.listOperations({
      status: "pending",
      cursor,
      limit: 1000,
    });

    items.push(
      ...page.items
        .filter((op) => op.status === "pending")
        .map((op) => ({
          id: op.opId,
          workspaceId: storage.getWorkspaceId(),
          path: normalizePath(op.path),
          revision: op.revision,
          correlationId: op.correlationId || "",
        })),
    );

    cursor = page.nextCursor ?? undefined;
  } while (cursor);

  return items;
}

export function acknowledgeWriteback(
  storage: StorageAdapter,
  itemId: string,
  success: boolean,
  errorMsg?: string,
  correlationId?: string,
  now: () => string = defaultNow,
): { status: string; id: string; success: boolean } | null {
  const op = storage.getOperation(itemId.trim());
  if (!op) {
    return null;
  }

  const timestamp = now();

  const effectiveCorrelationId = correlationId ?? op.correlationId;

  storage.putOperation({
    ...op,
    status: success ? "succeeded" : "failed",
    nextAttemptAt: null,
    lastError: success ? null : normalizeError(errorMsg),
    correlationId: effectiveCorrelationId,
  });

  createEvent(storage, {
    type: success ? "writeback.succeeded" : "writeback.failed",
    path: op.path,
    revision: op.revision,
    origin: "system",
    provider: op.provider,
    correlationId: effectiveCorrelationId,
    timestamp,
  });

  return { status: "acknowledged", id: op.opId, success };
}

export function dispatchWriteback(
  storage: StorageAdapter,
  opId: string,
  callbacks: DispatchWritebackCallbacks,
): boolean {
  const op = storage.getOperation(opId.trim());
  if (!op) {
    return false;
  }
  if (op.status !== "pending" && op.status !== "running") {
    return true;
  }

  const now = callbacks.now ?? defaultNow;

  const runningOp = {
    ...op,
    status: "running",
    nextAttemptAt: null,
    lastError: null,
  };
  storage.putOperation(runningOp);

  const item = toWritebackItem(storage, runningOp);
  try {
    callbacks.send(item);
    storage.putOperation({ ...runningOp, status: "dispatched" });
    return true;
  } catch (error) {
    const lastError = error instanceof Error ? error.message : "queue send failed";
    const attemptCount = (op.attemptCount ?? 0) + 1;

    if (attemptCount >= MAX_WRITEBACK_ATTEMPTS) {
      const timestamp = now();
      const deadLetteredOp = {
        ...op,
        status: "dead_lettered",
        attemptCount,
        nextAttemptAt: null,
        lastError,
      };
      storage.putOperation(deadLetteredOp);
      createEvent(storage, {
        type: "writeback.failed",
        path: op.path,
        revision: op.revision,
        origin: "system",
        provider: op.provider,
        correlationId: op.correlationId,
        timestamp,
      });
      return false;
    }

    const nextAttemptAt = addDelay(now(), WRITEBACK_RETRY_DELAY_MS);
    const pendingOp = {
      ...op,
      status: "pending",
      attemptCount,
      nextAttemptAt,
      lastError,
    };
    storage.putOperation(pendingOp);
    callbacks.onRetryScheduled?.(item, nextAttemptAt);
    return false;
  }
}

function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) {
    return "/";
  }
  const prefixed = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return prefixed.length > 1 ? prefixed.replace(/\/+$/, "") : "/";
}

function normalizeWritebackListState(state: unknown): WritebackListState {
  if (
    state === "pending" ||
    state === "succeeded" ||
    state === "failed" ||
    state === "dead_lettered"
  ) {
    return state;
  }

  throw new TypeError(
    "writeback state must be pending, succeeded, failed, or dead_lettered",
  );
}

function clampLimit(limit?: number): number {
  return Math.max(1, Math.min(limit ?? 100, 1000));
}

function listAllOperationsForState(
  storage: StorageAdapter,
  state: WritebackListState,
): OperationRow[] {
  const operations: OperationRow[] = [];
  let cursor: string | undefined;

  do {
    const page = storage.listOperations({
      status: state,
      cursor,
      limit: 1000,
    });
    operations.push(...page.items);
    const nextCursor = page.nextCursor ?? undefined;
    if (!nextCursor || nextCursor === cursor) {
      break;
    }
    cursor = nextCursor;
  } while (cursor);

  return operations;
}

function normalizeError(errorMsg?: string): string {
  return errorMsg?.trim() || "provider reported failure";
}

function addDelay(isoTimestamp: string, delayMs: number): string {
  const baseMs = Date.parse(isoTimestamp);
  if (Number.isNaN(baseMs)) {
    throw new Error("invalid timestamp");
  }
  return new Date(baseMs + delayMs).toISOString();
}

function defaultNow(): string {
  return new Date().toISOString();
}

function toWritebackItem(
  storage: StorageAdapter,
  item: { opId: string; path: string; revision: string; correlationId: string },
): WritebackItem {
  return {
    id: item.opId,
    workspaceId: storage.getWorkspaceId(),
    path: item.path,
    revision: item.revision,
    correlationId: item.correlationId,
  };
}

function toWritebackListItem(
  storage: StorageAdapter,
  op: OperationRow,
  state: WritebackListState,
): WritebackListItem {
  return {
    id: op.opId,
    workspaceId: storage.getWorkspaceId(),
    path: normalizePath(op.path),
    revision: op.revision,
    correlationId: op.correlationId || "",
    state,
    attemptCount: op.attemptCount,
    lastError: op.lastError,
    nextAttemptAt: op.nextAttemptAt,
    provider: op.provider,
  };
}

function compareWritebackListItems(
  left: WritebackListItem,
  right: WritebackListItem,
): number {
  if (left.nextAttemptAt && right.nextAttemptAt) {
    const byNextAttempt = left.nextAttemptAt.localeCompare(right.nextAttemptAt);
    if (byNextAttempt !== 0) {
      return byNextAttempt;
    }
  } else if (left.nextAttemptAt) {
    return -1;
  } else if (right.nextAttemptAt) {
    return 1;
  }

  return left.id.localeCompare(right.id);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function normalizeDeadLetterErrorCode(value: unknown): DeadLetterErrorCode {
  if (
    value === "schema_violation" ||
    value === "provider_4xx" ||
    value === "provider_5xx_exhausted" ||
    value === "timeout"
  ) {
    return value;
  }

  throw new TypeError("dead-letter error code is invalid");
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string") {
    throw new TypeError(`dead-letter error ${field} must be a string`);
  }
  return value;
}

function requiredPositiveInteger(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 1) {
    throw new TypeError(`dead-letter error ${field} must be a positive integer`);
  }
  return value;
}

function requiredIsoTimestamp(value: unknown, field: string): string {
  const timestamp = requiredString(value, field).trim();
  if (!timestamp || Number.isNaN(Date.parse(timestamp))) {
    throw new TypeError(`dead-letter error ${field} must be an ISO timestamp`);
  }
  return timestamp;
}

function optionalProviderStatus(value: unknown): number {
  if (
    typeof value !== "number" ||
    !Number.isInteger(value) ||
    value < 100 ||
    value > 599
  ) {
    throw new TypeError("dead-letter error providerStatus must be an HTTP status");
  }
  return value;
}

function optionalProviderResponse(value: unknown): Record<string, unknown> | unknown[] {
  if (!isRecord(value) && !Array.isArray(value)) {
    throw new TypeError(
      "dead-letter error providerResponse must be an object or array",
    );
  }
  return value;
}

function parseWritebackPath(
  writebackPath: string,
): { provider: string; resource: string } | null {
  const parts = normalizePath(writebackPath).split("/").filter(Boolean);
  if (parts.length < 3) {
    return null;
  }

  const provider = parts[0]?.trim().toLowerCase();
  const resource = parts[parts.length - 2]?.trim().toLowerCase();
  if (!provider || !resource || resource.startsWith(".")) {
    return null;
  }

  return { provider, resource };
}
