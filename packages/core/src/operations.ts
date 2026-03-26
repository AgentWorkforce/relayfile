/**
 * Operation tracking state machine.
 *
 * Extract from workspace.ts:
 *   - operation creation during writes
 *   - status transitions: pending → running → succeeded/failed/dead_lettered
 *   - operation listing with filters
 *   - operation replay
 */

import type { StorageAdapter, OperationRow, Paginated, PaginationOptions } from "./storage.js";

export type DispatchWriteback = (opId: string) => void;

export function createOperation(
  storage: StorageAdapter,
  path: string,
  revision: string,
  action: string,
  provider: string,
  correlationId: string,
  dispatchWriteback?: DispatchWriteback,
): OperationRow {
  const op: OperationRow = {
    opId: storage.nextOperationId(),
    path: normalizePath(path),
    revision,
    action: action.trim() || "file_upsert",
    provider: normalizeProvider(provider),
    status: "pending",
    attemptCount: 0,
    lastError: null,
    nextAttemptAt: null,
    correlationId: correlationId || "",
  };

  storage.putOperation(op);
  dispatchWriteback?.(op.opId);
  return op;
}

export function getOperation(storage: StorageAdapter, opId: string): OperationRow | null {
  const normalized = opId.trim();
  if (!normalized) {
    return null;
  }
  return storage.getOperation(normalized);
}

export function listOperations(
  storage: StorageAdapter,
  options: PaginationOptions & { status?: string; action?: string; provider?: string },
): Paginated<OperationRow> {
  const status = options.status?.trim() || undefined;
  const action = options.action?.trim() || undefined;
  const provider = normalizeProvider(options.provider);
  return storage.listOperations({
    status,
    action,
    provider: provider || undefined,
    cursor: options.cursor || undefined,
    limit: clampLimit(options.limit),
  });
}

export function acknowledgeOperation(
  storage: StorageAdapter,
  opId: string,
  success: boolean,
  errorMsg?: string,
): OperationRow | null {
  const op = getOperation(storage, opId);
  if (!op) {
    return null;
  }

  const updated: OperationRow = {
    ...op,
    status: success ? "succeeded" : "failed",
    nextAttemptAt: null,
    lastError: success ? null : normalizeError(errorMsg),
  };
  storage.putOperation(updated);
  return updated;
}

export function replayOperation(
  storage: StorageAdapter,
  opId: string,
  dispatchWriteback?: DispatchWriteback,
): OperationRow | null {
  const op = getOperation(storage, opId);
  if (!op || !isReplayableOperationStatus(op.status)) {
    return null;
  }

  const updated: OperationRow = {
    ...op,
    status: "pending",
    nextAttemptAt: null,
    lastError: null,
  };
  storage.putOperation(updated);
  dispatchWriteback?.(updated.opId);
  return updated;
}

function clampLimit(limit?: number): number {
  return Math.max(1, Math.min(limit ?? 100, 1000));
}

function normalizeProvider(provider?: string): string {
  return provider?.trim().toLowerCase() ?? "";
}

function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) {
    return "/";
  }
  const prefixed = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return prefixed.length > 1 ? prefixed.replace(/\/+$/, "") : "/";
}

function normalizeError(errorMsg?: string): string {
  return errorMsg?.trim() || "provider reported failure";
}

function isReplayableOperationStatus(status?: string): boolean {
  return status === "failed" || status === "dead_lettered" || status === "canceled";
}
