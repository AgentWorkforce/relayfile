import type { WebhookInput } from "./provider.js";
import type { IngestWebhookInput } from "./types.js";

export type WebhookProvider = string & { readonly __brand: "WebhookProvider" };
export type WebhookObjectType = string & {
  readonly __brand: "WebhookObjectType";
};
export type WebhookObjectId = string & { readonly __brand: "WebhookObjectId" };
export type WebhookEventType = string & { readonly __brand: "WebhookEventType" };

export interface NormalizedWebhook {
  provider: WebhookProvider;
  connectionId?: string;
  eventType: WebhookEventType;
  objectType: WebhookObjectType;
  objectId: WebhookObjectId;
  payload: Record<string, unknown>;
  relations?: string[];
  metadata?: Record<string, string>;
  headers?: Record<string, string>;
  deliveryId?: string;
  timestamp?: string;
}

export type IngestWebhookRequest = Omit<
  IngestWebhookInput,
  "workspaceId" | "correlationId" | "signal"
>;

export type WebhookNormalizationField =
  | "provider"
  | "objectType"
  | "objectId"
  | "eventType"
  | "payload"
  | "path";

export type WebhookNormalizationErrorCode =
  | "invalid_webhook"
  | "invalid_provider"
  | "invalid_object_type"
  | "invalid_object_id"
  | "invalid_event_type"
  | "invalid_payload"
  | "invalid_path";

const STRING_VARIANTS = {
  connectionId: ["connectionId", "connection_id"],
  eventType: ["eventType", "event_type"],
  objectType: ["objectType", "object_type"],
  objectId: ["objectId", "object_id"],
  deliveryId: ["deliveryId", "delivery_id"],
  timestamp: ["timestamp", "receivedAt", "received_at"],
} as const;

export class WebhookNormalizationError extends Error {
  readonly code: WebhookNormalizationErrorCode;
  readonly field?: WebhookNormalizationField;
  readonly value?: unknown;

  constructor(
    code: WebhookNormalizationErrorCode,
    message: string,
    options: {
      field?: WebhookNormalizationField;
      value?: unknown;
      cause?: unknown;
    } = {}
  ) {
    super(message);
    this.name = "WebhookNormalizationError";
    this.code = code;
    this.field = options.field;
    this.value = options.value;
    if (options.cause !== undefined) {
      Object.defineProperty(this, "cause", {
        configurable: true,
        enumerable: false,
        value: options.cause,
        writable: true,
      });
    }
  }
}

export function isProvider(value: unknown): value is WebhookProvider {
  return isNonEmptyString(value);
}

export function isObjectType(value: unknown): value is WebhookObjectType {
  return isNonEmptyString(value);
}

export function isObjectId(value: unknown): value is WebhookObjectId {
  return isNonEmptyString(value);
}

export function isEventType(value: unknown): value is WebhookEventType {
  return isNonEmptyString(value);
}

export function isNormalizedWebhook(value: unknown): value is NormalizedWebhook {
  if (!isRecord(value)) {
    return false;
  }

  return (
    isProvider(value.provider) &&
    isEventType(readFirstString(value, STRING_VARIANTS.eventType)) &&
    isObjectType(readFirstString(value, STRING_VARIANTS.objectType)) &&
    isObjectId(readFirstString(value, STRING_VARIANTS.objectId)) &&
    isRecord(readPayload(value))
  );
}

export function normalizeWebhook(
  raw: unknown,
  provider: unknown
): NormalizedWebhook {
  const input = ensureRecord(raw);
  const normalizedProvider = asProvider(provider);
  const embeddedProvider = input.provider;

  if (embeddedProvider !== undefined && !isProvider(embeddedProvider)) {
    throw invalidField("provider", "invalid_provider", embeddedProvider);
  }
  if (
    isProvider(embeddedProvider) &&
    embeddedProvider.trim() !== normalizedProvider
  ) {
    throw new WebhookNormalizationError(
      "invalid_provider",
      `Webhook provider "${embeddedProvider}" does not match "${normalizedProvider}".`,
      { field: "provider", value: embeddedProvider }
    );
  }

  return buildNormalizedWebhook(input, normalizedProvider);
}

export function fromWebhookInput(
  input: WebhookInput | NormalizedWebhook
): NormalizedWebhook {
  const record = ensureRecord(input);
  return buildNormalizedWebhook(record, asProvider(record.provider));
}

export function toIngestWebhookInput(
  event: WebhookInput | NormalizedWebhook,
  path: string
): IngestWebhookRequest {
  const normalized = fromWebhookInput(event);
  const trimmedPath = path.trim();

  if (!trimmedPath) {
    throw new WebhookNormalizationError(
      "invalid_path",
      "Webhook path must be a non-empty string.",
      { field: "path", value: path }
    );
  }

  return {
    provider: normalized.provider,
    event_type: normalized.eventType,
    path: trimmedPath,
    data: normalized.payload,
    ...(normalized.deliveryId ? { delivery_id: normalized.deliveryId } : {}),
    ...(normalized.timestamp ? { timestamp: normalized.timestamp } : {}),
    ...(normalized.headers ? { headers: normalized.headers } : {}),
  };
}

function buildNormalizedWebhook(
  input: Record<string, unknown>,
  provider: WebhookProvider
): NormalizedWebhook {
  const connectionId = asOptionalString(
    readFirstString(input, STRING_VARIANTS.connectionId)
  );
  const relations = readStringArray(input.relations);
  const metadata = readStringMap(input.metadata);
  const headers = readStringMap(input.headers);
  const deliveryId = asOptionalString(
    readFirstString(input, STRING_VARIANTS.deliveryId)
  );
  const timestamp = asOptionalString(
    readFirstString(input, STRING_VARIANTS.timestamp)
  );

  return {
    provider,
    objectType: asObjectType(readRequiredString(input, "objectType")),
    objectId: asObjectId(readRequiredString(input, "objectId")),
    eventType: asEventType(readRequiredString(input, "eventType")),
    payload: ensurePayload(readPayload(input)),
    ...(connectionId ? { connectionId } : {}),
    ...(relations ? { relations } : {}),
    ...(metadata ? { metadata } : {}),
    ...(headers ? { headers } : {}),
    ...(deliveryId ? { deliveryId } : {}),
    ...(timestamp ? { timestamp } : {}),
  };
}

function readPayload(input: Record<string, unknown>): unknown {
  if ("payload" in input) {
    return input.payload;
  }
  if ("data" in input) {
    return input.data;
  }
  return undefined;
}

function readRequiredString(
  input: Record<string, unknown>,
  field: "objectType" | "objectId" | "eventType"
): string {
  const value = readFirstString(input, STRING_VARIANTS[field]);
  if (!isNonEmptyString(value)) {
    throw invalidField(field, errorCodeForField(field), value);
  }
  return value.trim();
}

function readFirstString(
  input: Record<string, unknown>,
  keys: readonly string[]
): unknown {
  for (const key of keys) {
    if (key in input) {
      return input[key];
    }
  }
  return undefined;
}

function readStringArray(value: unknown): string[] | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!Array.isArray(value) || value.some((item) => !isNonEmptyString(item))) {
    return undefined;
  }
  return value.map((item) => item.trim());
}

function readStringMap(value: unknown): Record<string, string> | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!isRecord(value)) {
    return undefined;
  }

  const entries: Array<[string, string]> = [];
  for (const [key, entryValue] of Object.entries(value)) {
    if (isNonEmptyString(entryValue)) {
      entries.push([key, entryValue.trim()]);
    }
  }
  if (entries.length === 0) {
    return undefined;
  }

  return Object.fromEntries(entries);
}

function ensureRecord(value: unknown): Record<string, unknown> {
  if (!isRecord(value)) {
    throw new WebhookNormalizationError(
      "invalid_webhook",
      "Webhook input must be an object.",
      { value }
    );
  }
  return value;
}

function ensurePayload(value: unknown): Record<string, unknown> {
  if (!isRecord(value)) {
    throw new WebhookNormalizationError(
      "invalid_payload",
      "Webhook payload must be an object.",
      { field: "payload", value }
    );
  }
  return value;
}

function invalidField(
  field: Exclude<WebhookNormalizationField, "payload" | "path">,
  code: WebhookNormalizationErrorCode,
  value: unknown
): WebhookNormalizationError {
  return new WebhookNormalizationError(
    code,
    `Webhook ${field} must be a non-empty string.`,
    { field, value }
  );
}

function errorCodeForField(
  field: "objectType" | "objectId" | "eventType"
): WebhookNormalizationErrorCode {
  switch (field) {
    case "objectType":
      return "invalid_object_type";
    case "objectId":
      return "invalid_object_id";
    case "eventType":
      return "invalid_event_type";
  }
}

function asProvider(value: unknown): WebhookProvider {
  if (!isProvider(value)) {
    throw invalidField("provider", "invalid_provider", value);
  }
  return value.trim() as WebhookProvider;
}

function asObjectType(value: string): WebhookObjectType {
  return value as WebhookObjectType;
}

function asObjectId(value: string): WebhookObjectId {
  return value as WebhookObjectId;
}

function asEventType(value: string): WebhookEventType {
  return value as WebhookEventType;
}

function asOptionalString(value: unknown): string | undefined {
  if (!isNonEmptyString(value)) {
    return undefined;
  }
  return value.trim();
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}
