/**
 * Event recording and feed.
 *
 * Extract from workspace.ts:
 *   - event creation during writes/deletes
 *   - event feed listing with pagination
 *   - recent events retrieval (for WebSocket catch-up)
 */

import type { StorageAdapter, EventRow, Paginated, PaginationOptions } from "./storage.js";

export interface CreateEventInput {
  type: string;
  path: string;
  revision: string;
  origin: string;
  provider: string;
  correlationId: string;
  timestamp?: string;
}

export function createEvent(storage: StorageAdapter, input: CreateEventInput): EventRow {
  const event: EventRow = {
    eventId: storage.nextEventId(),
    type: input.type,
    path: input.path,
    revision: input.revision,
    origin: input.origin,
    provider: normalizeProvider(input.provider),
    correlationId: input.correlationId || "",
    timestamp: input.timestamp ?? new Date().toISOString(),
  };

  storage.appendEvent(event);
  return event;
}

export function listEvents(
  storage: StorageAdapter,
  options: PaginationOptions & { provider?: string },
): Paginated<EventRow> {
  const provider = normalizeProvider(options.provider);
  return storage.listEvents({
    provider: provider || undefined,
    cursor: options.cursor || undefined,
    limit: clampLimit(options.limit),
  });
}

export function getRecentEvents(storage: StorageAdapter, limit: number): EventRow[] {
  return storage.getRecentEvents(clampLimit(limit));
}

function normalizeProvider(provider?: string): string {
  return provider?.trim().toLowerCase() ?? "";
}

function clampLimit(limit?: number): number {
  return Math.max(1, Math.min(limit ?? 200, 1000));
}
