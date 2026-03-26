/**
 * Writeback lifecycle.
 *
 * Extract from workspace.ts:
 *   - pending writeback listing (filtered by op status)
 *   - writeback acknowledgment
 *   - dead-letter handling
 *   - retry logic
 */

import type { StorageAdapter, WritebackItem } from "./storage.js";
import { createEvent } from "./events.js";

export const MAX_WRITEBACK_ATTEMPTS = 3;
export const WRITEBACK_RETRY_DELAY_MS = 30_000;

export interface DispatchWritebackCallbacks {
  send(item: WritebackItem): void;
  onRetryScheduled?(item: WritebackItem, nextAttemptAt: string): void;
  now?(): string;
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
      ...page.items.map((op) => ({
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

  storage.putOperation({
    ...op,
    status: success ? "succeeded" : "failed",
    nextAttemptAt: null,
    lastError: success ? null : normalizeError(errorMsg),
  });

  createEvent(storage, {
    type: success ? "writeback.succeeded" : "writeback.failed",
    path: op.path,
    revision: op.revision,
    origin: "system",
    provider: op.provider,
    correlationId: op.correlationId,
    timestamp,
  });

  void correlationId;
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
