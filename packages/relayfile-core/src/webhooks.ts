/**
 * Webhook envelope normalization and processing.
 *
 * Extract from workspace.ts:
 *   - generic webhook ingestion
 *   - envelope normalization
 *   - deduplication by delivery_id
 *   - coalescing within time window
 *   - suppression for loop prevention
 *   - stale event filtering
 *   - file materialization from webhook data
 *
 * This module stays pure. It only operates over plain storage records and
 * optional callbacks for suppression/staleness policy.
 */

import type {
  StorageAdapter,
  EnvelopeRow,
  EnvelopeQueryOptions,
  Paginated,
  FileSemantics,
} from "./storage.js";
import { normalizePath } from "./files.js";

export interface IngestWebhookInput {
  provider: string;
  eventType?: string;
  path?: string;
  data?: Record<string, unknown>;
  deliveryId?: string;
  timestamp?: string;
  headers?: Record<string, string>;
  correlationId?: string;
}

export interface IngestResult {
  status: string;
  envelopeId: string;
  correlationId: string;
}

export interface EnvelopeEvent {
  type: string;
  path: string;
  timestamp: string;
  content?: string;
  contentType?: string;
  encoding?: string;
  semantics?: FileSemantics;
  data?: Record<string, unknown>;
}

export interface ApplyEnvelopeOptions {
  now?: () => string;
  shouldSuppress?: (envelope: EnvelopeRow, event: EnvelopeEvent) => boolean;
  isStale?: (envelope: EnvelopeRow, event: EnvelopeEvent) => boolean;
}

export interface ApplyEnvelopeResult {
  status: "processed" | "ignored" | "suppressed" | "stale";
  eventType: string | null;
  path: string | null;
  revision: string | null;
}

export interface IngestWebhookOptions {
  now?: () => string;
  generateEnvelopeId?: () => string;
  coalesceWindowMs?: number;
}

export interface WebhookStorageAdapter extends StorageAdapter {
  getEnvelopeByDelivery(
    workspaceId: string,
    provider: string,
    deliveryId: string,
  ): EnvelopeRow | null;
  putEnvelope(envelope: EnvelopeRow): void;
  putEnvelopeDeliveryAlias?(
    workspaceId: string,
    provider: string,
    deliveryId: string,
    envelopeId: string,
  ): void;
  listEnvelopes(options: EnvelopeQueryOptions): Paginated<EnvelopeRow>;
}

export function ingestWebhook(
  storage: StorageAdapter,
  input: IngestWebhookInput,
  options: IngestWebhookOptions = {},
): IngestResult {
  const envelopeStorage = getWebhookStorage(storage);
  const correlationId = input.correlationId?.trim() ?? "";
  if (!envelopeStorage) {
    return {
      status: "invalid_storage",
      envelopeId: "",
      correlationId,
    };
  }

  const now = options.now ?? nowIso;
  const normalized = normalizeEnvelope(input, now);
  const provider = asOptionalString(normalized.provider) ?? "";
  const payload = asRecord(normalized.payload);
  const path = normalizeEnvelopePath({ payload });
  const receivedAt = asOptionalString(normalized.receivedAt) ?? now();
  const workspaceId = storage.getWorkspaceId();
  const deliveryId =
    asOptionalString(normalized.deliveryId) ??
    defaultDeliveryId(provider, receivedAt);

  if (!provider || !path || !normalizeEnvelopeEventType(payload)) {
    return {
      status: "invalid_input",
      envelopeId: "",
      correlationId,
    };
  }

  const existing = findEnvelopeByDelivery(
    envelopeStorage,
    workspaceId,
    provider,
    deliveryId,
  );
  if (existing) {
    return {
      status: "duplicate",
      envelopeId: existing.envelopeId,
      correlationId: existing.correlationId || correlationId,
    };
  }

  const queued = envelopeStorage.listEnvelopes({
    workspaceId,
    provider,
    status: "queued",
    limit: 100,
  }).items;
  const coalesced = findCoalescedEnvelope(
    queued,
    payload,
    receivedAt,
    options.coalesceWindowMs ?? DEFAULT_COALESCE_WINDOW_MS,
  );
  if (coalesced) {
    const updated: EnvelopeRow = {
      ...coalesced,
      deliveryId: coalesced.deliveryId,
      deliveryIds: mergeDeliveryIds(coalesced, deliveryId),
      receivedAt,
      headers: normalized.headers ?? coalesced.headers ?? {},
      payload,
      correlationId: correlationId || coalesced.correlationId,
      status: "queued",
      lastError: null,
    };
    envelopeStorage.putEnvelope(updated);
    envelopeStorage.putEnvelopeDeliveryAlias?.(
      workspaceId,
      provider,
      deliveryId,
      updated.envelopeId,
    );
    return {
      status: "queued",
      envelopeId: updated.envelopeId,
      correlationId: updated.correlationId,
    };
  }

  const envelopeId = (options.generateEnvelopeId ?? defaultEnvelopeId)();
  envelopeStorage.putEnvelope({
    envelopeId,
    workspaceId,
    provider,
    deliveryId,
    deliveryIds: [deliveryId],
    receivedAt,
    headers: normalized.headers ?? {},
    payload,
    correlationId,
    status: "queued",
    attemptCount: 0,
    lastError: null,
  });
  envelopeStorage.putEnvelopeDeliveryAlias?.(
    workspaceId,
    provider,
    deliveryId,
    envelopeId,
  );

  return {
    status: "queued",
    envelopeId,
    correlationId,
  };
}

export function normalizeEnvelope(
  input: IngestWebhookInput,
  now: () => string = nowIso,
): Partial<EnvelopeRow> {
  const provider = normalizeProvider(input.provider);
  const eventType = normalizeEventType(input.eventType);
  const path = normalizePath(input.path ?? "");
  const correlationId = input.correlationId?.trim() ?? "";
  const receivedAt = normalizeIsoDate(input.timestamp) ?? now();
  const deliveryId = input.deliveryId?.trim();

  if (!provider || !eventType || !input.path?.trim()) {
    return {
      provider,
      deliveryId,
      receivedAt,
      headers: normalizeHeaderMap(input.headers),
      payload: {},
      correlationId,
    };
  }

  return {
    provider,
    deliveryId,
    receivedAt,
    headers: normalizeHeaderMap(input.headers),
    payload: {
      provider,
      event_type: eventType,
      path,
      timestamp: receivedAt,
      data: asRecord(input.data),
      delivery_id: deliveryId,
    },
    correlationId,
  };
}

export function normalizeEnvelopeEvent(
  envelope: Pick<EnvelopeRow, "payload" | "receivedAt">,
): EnvelopeEvent | null {
  const payload = envelope.payload;
  const eventType = normalizeEnvelopeEventType(payload);
  if (!eventType) {
    return null;
  }

  const path = normalizePath(asOptionalString(payload.path) ?? "/");
  const data = asRecord(payload.data);
  const body = Object.keys(data).length > 0 ? data : payload;
  const timestamp =
    normalizeIsoDate(asOptionalString(payload.timestamp) ?? envelope.receivedAt) ??
    nowIso();
  const content = typeof body.content === "string" ? body.content : undefined;
  const contentType =
    typeof body.contentType === "string"
      ? body.contentType
      : typeof body.content_type === "string"
        ? body.content_type
        : undefined;
  const encoding = typeof body.encoding === "string" ? body.encoding : undefined;
  const semantics =
    body.semantics && typeof body.semantics === "object" && !Array.isArray(body.semantics)
      ? normalizeSemantics(body.semantics as FileSemantics)
      : undefined;

  return {
    type: eventType,
    path,
    timestamp,
    content,
    contentType,
    encoding,
    semantics,
    data: body,
  };
}

export function normalizeEnvelopePath(
  envelope: Pick<EnvelopeRow, "payload">,
): string | null {
  const path = asOptionalString(envelope.payload.path);
  return path ? normalizePath(path) : null;
}

export function applyWebhookEnvelope(
  storage: StorageAdapter,
  envelope: EnvelopeRow,
  options: ApplyEnvelopeOptions = {},
): ApplyEnvelopeResult {
  const event = normalizeEnvelopeEvent(envelope);
  if (!event) {
    return {
      status: "ignored",
      eventType: null,
      path: normalizeEnvelopePath(envelope),
      revision: null,
    };
  }

  if (options.shouldSuppress?.(envelope, event)) {
    const revision = appendSyncEvent(
      storage,
      "sync.suppressed",
      event.path,
      envelope.provider,
      envelope.correlationId,
      options.now?.() ?? event.timestamp,
    );
    return {
      status: "suppressed",
      eventType: "sync.suppressed",
      path: event.path,
      revision,
    };
  }

  if (options.isStale?.(envelope, event)) {
    const revision = appendSyncEvent(
      storage,
      "sync.stale",
      event.path,
      envelope.provider,
      envelope.correlationId,
      options.now?.() ?? event.timestamp,
    );
    return {
      status: "stale",
      eventType: "sync.stale",
      path: event.path,
      revision,
    };
  }

  if (event.type === "file.created" || event.type === "file.updated") {
    const revision = storage.nextRevision();
    const content =
      typeof event.content === "string"
        ? event.content
        : JSON.stringify(event.data ?? {});

    storage.putFile({
      path: event.path,
      revision,
      contentType: event.contentType?.trim() || DEFAULT_CONTENT_TYPE,
      content,
      encoding: normalizeEncoding(event.encoding) ?? "utf-8",
      provider: envelope.provider,
      lastEditedAt: event.timestamp,
      semantics: normalizeSemantics(event.semantics),
    });

    storage.appendEvent({
      eventId: storage.nextEventId(),
      type: event.type,
      path: event.path,
      revision,
      origin: "provider_sync",
      provider: envelope.provider,
      correlationId: envelope.correlationId,
      timestamp: event.timestamp,
    });

    return {
      status: "processed",
      eventType: event.type,
      path: event.path,
      revision,
    };
  }

  if (event.type === "file.deleted") {
    storage.deleteFile(event.path);
    const revision = appendSyncEvent(
      storage,
      "file.deleted",
      event.path,
      envelope.provider,
      envelope.correlationId,
      event.timestamp,
    );
    return {
      status: "processed",
      eventType: "file.deleted",
      path: event.path,
      revision,
    };
  }

  const revision = appendSyncEvent(
    storage,
    event.type,
    event.path,
    envelope.provider,
    envelope.correlationId,
    event.timestamp,
  );
  return {
    status: "processed",
    eventType: event.type,
    path: event.path,
    revision,
  };
}

const DEFAULT_CONTENT_TYPE = "text/markdown";
const DEFAULT_COALESCE_WINDOW_MS = 3_000;
const ENVELOPE_SCAN_PAGE_SIZE = 100;
const VALID_EVENT_TYPES = new Set([
  "file.created",
  "file.updated",
  "file.deleted",
  "dir.created",
  "dir.deleted",
  "sync.error",
  "sync.ignored",
  "sync.suppressed",
  "sync.stale",
  "writeback.failed",
  "writeback.succeeded",
]);

function getWebhookStorage(storage: StorageAdapter): WebhookStorageAdapter | null {
  if (
    typeof storage.getEnvelopeByDelivery !== "function" ||
    typeof storage.putEnvelope !== "function" ||
    typeof storage.listEnvelopes !== "function"
  ) {
    return null;
  }
  return storage as WebhookStorageAdapter;
}

function findEnvelopeByDelivery(
  storage: WebhookStorageAdapter,
  workspaceId: string,
  provider: string,
  deliveryId: string,
): EnvelopeRow | null {
  const direct = storage.getEnvelopeByDelivery(workspaceId, provider, deliveryId);
  if (direct) {
    return direct;
  }

  let cursor: string | undefined;
  for (;;) {
    const page = storage.listEnvelopes({
      workspaceId,
      provider,
      cursor,
      limit: ENVELOPE_SCAN_PAGE_SIZE,
    });

    for (const envelope of page.items) {
      if (deliveryMatches(envelope, deliveryId)) {
        return envelope;
      }
    }

    if (!page.nextCursor || page.nextCursor === cursor) {
      return null;
    }
    cursor = page.nextCursor;
  }
}

function appendSyncEvent(
  storage: StorageAdapter,
  type: string,
  path: string,
  provider: string,
  correlationId: string,
  timestamp: string,
): string {
  const revision = storage.nextRevision();
  storage.appendEvent({
    eventId: storage.nextEventId(),
    type,
    path,
    revision,
    origin: "provider_sync",
    provider,
    correlationId,
    timestamp,
  });
  return revision;
}

function findCoalescedEnvelope(
  envelopes: EnvelopeRow[],
  payload: Record<string, unknown>,
  receivedAt: string,
  windowMs: number,
): EnvelopeRow | null {
  const key = coalesceObjectKey(payload);
  if (!key) {
    return null;
  }

  let match: EnvelopeRow | null = null;
  for (const envelope of envelopes) {
    if (coalesceObjectKey(envelope.payload) !== key) {
      continue;
    }
    if (!withinCoalesceWindow(envelope.receivedAt, receivedAt, windowMs)) {
      continue;
    }
    if (!match || envelope.receivedAt > match.receivedAt) {
      match = envelope;
    }
  }
  return match;
}

function mergeDeliveryIds(envelope: EnvelopeRow, deliveryId: string): string[] {
  return Array.from(
    new Set([envelope.deliveryId, ...(envelope.deliveryIds ?? []), deliveryId]),
  );
}

function deliveryMatches(envelope: EnvelopeRow, deliveryId: string): boolean {
  return envelope.deliveryId === deliveryId || envelope.deliveryIds?.includes(deliveryId) === true;
}

function coalesceObjectKey(payload: Record<string, unknown>): string {
  const data = asRecord(payload.data);
  const providerObjectId =
    asOptionalString(data.providerObjectId) ??
    asOptionalString(data.provider_object_id) ??
    asOptionalString(data.objectId) ??
    asOptionalString(payload.providerObjectId) ??
    asOptionalString(payload.provider_object_id) ??
    asOptionalString(payload.objectId);
  if (providerObjectId) {
    return `object:${providerObjectId}`;
  }

  const path = normalizeEnvelopePath({ payload });
  return path && path !== "/" ? `path:${path}` : "";
}

function withinCoalesceWindow(
  existingReceivedAt: string,
  incomingReceivedAt: string,
  windowMs: number,
): boolean {
  if (windowMs <= 0) {
    return true;
  }
  const existingTs = Date.parse(existingReceivedAt);
  const incomingTs = Date.parse(incomingReceivedAt);
  if (Number.isNaN(existingTs) || Number.isNaN(incomingTs)) {
    return false;
  }
  if (incomingTs < existingTs) {
    return false;
  }
  return incomingTs - existingTs <= windowMs;
}

function normalizeProvider(provider?: string): string {
  return provider?.trim().toLowerCase() ?? "";
}

function normalizeEncoding(encoding?: string): "utf-8" | "base64" | null {
  const value = encoding?.trim().toLowerCase() ?? "";
  if (!value || value === "utf-8" || value === "utf8") {
    return "utf-8";
  }
  if (value === "base64") {
    return "base64";
  }
  return null;
}

function normalizeEventType(value?: string): string | null {
  const type = value?.trim() ?? "";
  return VALID_EVENT_TYPES.has(type) ? type : null;
}

function normalizeEnvelopeEventType(
  payload: Record<string, unknown>,
): string | null {
  return normalizeEventType(
    asOptionalString(payload.event_type) ??
      asOptionalString(payload.eventType) ??
      asOptionalString(payload.type) ??
      undefined,
  );
}

function normalizeIsoDate(value?: string | null): string | null {
  if (!value?.trim()) {
    return null;
  }
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? null : new Date(timestamp).toISOString();
}

function normalizeHeaderMap(
  headers?: Record<string, string>,
): Record<string, string> {
  if (!headers) {
    return {};
  }
  return Object.entries(headers).reduce<Record<string, string>>(
    (acc, [key, value]) => {
      const normalizedKey = key.trim();
      if (normalizedKey) {
        acc[normalizedKey] = String(value);
      }
      return acc;
    },
    {},
  );
}

function normalizeSemantics(input?: FileSemantics): FileSemantics {
  return {
    properties: normalizeProperties(input?.properties),
    relations: normalizeStringArray(input?.relations),
    permissions: normalizeStringArray(input?.permissions),
    comments: normalizeStringArray(input?.comments),
  };
}

function normalizeProperties(
  input?: Record<string, string>,
): Record<string, string> | undefined {
  if (!input) {
    return undefined;
  }
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(input)) {
    const normalizedKey = key.trim();
    if (!normalizedKey) {
      continue;
    }
    out[normalizedKey] = String(value).trim();
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function normalizeStringArray(values?: string[]): string[] | undefined {
  if (!values || values.length === 0) {
    return undefined;
  }
  const normalized = Array.from(
    new Set(values.map((value) => value.trim()).filter(Boolean)),
  );
  normalized.sort((left, right) => left.localeCompare(right));
  return normalized.length > 0 ? normalized : undefined;
}

function asOptionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function defaultEnvelopeId(): string {
  return `env_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
}

function defaultDeliveryId(provider: string, receivedAt: string): string {
  const safeProvider = provider || "webhook";
  return `dlv_${safeProvider}_${Date.parse(receivedAt) || Date.now()}`;
}

function nowIso(): string {
  return new Date().toISOString();
}
