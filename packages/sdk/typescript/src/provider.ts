/**
 * Abstract integration provider interface.
 *
 * Relayfile supports multiple integration providers (Nango, Composio, etc.)
 * for syncing external service data into the Relayfile filesystem.
 *
 * Each provider maps its own webhook/event format into Relayfile's canonical
 * path + semantics model.
 */

import type { RelayFileClient } from "./client.js";
import {
  WebhookNormalizationError,
  fromWebhookInput as fromBridgeWebhookInput,
  isEventType,
  isNormalizedWebhook as isBridgeNormalizedWebhook,
  isObjectId,
  isObjectType,
  isProvider,
  normalizeWebhook as normalizeBridgeWebhook,
  toIngestWebhookInput,
} from "./normalized-webhook.js";
import type {
  FileQueryItem,
  FilesystemEvent,
  QueuedResponse
} from "./types.js";
import type {
  IngestWebhookRequest,
  WebhookEventType,
  WebhookNormalizationErrorCode,
  WebhookNormalizationField,
  WebhookObjectId,
  WebhookObjectType,
  WebhookProvider,
} from "./normalized-webhook.js";

// ---------------------------------------------------------------------------
// Common types
// ---------------------------------------------------------------------------

export type ProviderPayload = Record<string, unknown>;
export type ProviderMetadata = Record<string, string>;
export type ProxyHeaders = Record<string, string>;
export type ProxyQuery = Record<string, string>;
export type ProxyMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/** Normalized webhook input from any provider */
export interface WebhookInput {
  /** Provider name (e.g., "github", "slack", "zendesk") */
  provider: string;
  /** Object type / model (e.g., "tickets", "commits", "messages") */
  objectType: string;
  /** Unique object ID within the provider */
  objectId: string;
  /** Event type (e.g., "created", "updated", "deleted") */
  eventType: string;
  /** Raw payload data */
  payload: ProviderPayload;
  /** Optional relations to other objects */
  relations?: string[];
  /** Provider-specific metadata (connection IDs, user IDs, etc.) */
  metadata?: ProviderMetadata;
}

/**
 * Normalized webhook emitted by a connection provider.
 *
 * This extends the generic webhook shape with the provider connection context
 * adapters need for follow-up API calls.
 */
export interface NormalizedWebhook extends WebhookInput {
  /** Connection handle managed by the provider (e.g., Nango connection ID) */
  connectionId: string;
}

export {
  WebhookNormalizationError,
  isEventType,
  isObjectId,
  isObjectType,
  isProvider,
  toIngestWebhookInput,
};

export type {
  IngestWebhookRequest,
  WebhookEventType,
  WebhookNormalizationErrorCode,
  WebhookNormalizationField,
  WebhookObjectId,
  WebhookObjectType,
  WebhookProvider,
};

export function isNormalizedWebhook(value: unknown): value is NormalizedWebhook {
  return (
    isBridgeNormalizedWebhook(value) &&
    typeof value.connectionId === "string" &&
    value.connectionId.trim().length > 0
  );
}

export function normalizeWebhook(
  raw: unknown,
  provider: unknown
): NormalizedWebhook {
  return requireConnectionId(normalizeBridgeWebhook(raw, provider));
}

export function fromWebhookInput(
  input: WebhookInput | NormalizedWebhook
): NormalizedWebhook {
  return requireConnectionId(fromBridgeWebhookInput(input));
}

/** Authenticated proxy request executed by a connection provider */
export interface ProxyRequest {
  method: ProxyMethod;
  baseUrl: string;
  endpoint: string;
  connectionId: string;
  headers?: ProxyHeaders;
  body?: unknown;
  query?: ProxyQuery;
}

/** Authenticated proxy response returned by a connection provider */
export interface ProxyResponse {
  status: number;
  headers: ProxyHeaders;
  data: unknown;
}

/**
 * Connection provider contract for adapter packages.
 *
 * Providers own auth, token refresh, and API proxying. They do not define
 * Relayfile paths or semantics.
 */
export interface ConnectionProvider {
  readonly name: string;
  proxy(request: ProxyRequest): Promise<ProxyResponse>;
  healthCheck(connectionId: string): Promise<boolean>;
  handleWebhook?(rawPayload: unknown): Promise<NormalizedWebhook>;
}

/** Options for listing files from a specific provider */
export interface ListProviderFilesOptions {
  provider: string;
  objectType?: string;
  status?: string;
  limit?: number;
  signal?: AbortSignal;
}

/** Options for watching provider events */
export interface WatchProviderEventsOptions {
  provider: string;
  pollIntervalMs?: number;
  cursor?: string;
  signal?: AbortSignal;
}

// ---------------------------------------------------------------------------
// Provider path mapping
// ---------------------------------------------------------------------------

const DEFAULT_PATH_PREFIXES: Record<string, string> = {
  zendesk: "/zendesk",
  shopify: "/shopify",
  github: "/github",
  stripe: "/stripe",
  slack: "/slack",
  linear: "/linear",
  jira: "/jira",
  hubspot: "/hubspot",
  salesforce: "/salesforce",
  gmail: "/gmail",
  notion: "/notion",
  asana: "/asana",
  trello: "/trello",
  intercom: "/intercom",
  freshdesk: "/freshdesk",
  discord: "/discord",
  twilio: "/twilio",
};

export function computeCanonicalPath(
  provider: string,
  objectType: string,
  objectId: string
): string {
  const prefix = DEFAULT_PATH_PREFIXES[provider] ?? `/${provider}`;
  return `${prefix}/${objectType}/${objectId}.json`;
}

// ---------------------------------------------------------------------------
// Legacy-compatible provider abstraction
// ---------------------------------------------------------------------------

/**
 * Legacy-compatible abstraction used by the in-SDK provider helpers.
 *
 * New standalone provider packages should implement ConnectionProvider instead.
 */
export abstract class IntegrationProvider {
  protected readonly client: RelayFileClient;
  abstract readonly name: string;

  constructor(client: RelayFileClient) {
    this.client = client;
  }

  protected toNormalizedWebhook(
    input: WebhookInput | NormalizedWebhook
  ): NormalizedWebhook {
    return fromWebhookInput(input);
  }

  /**
   * Ingest a webhook event from this provider into Relayfile.
   * Each provider implementation normalizes its event format to WebhookInput,
   * then writes it to the canonical path.
   */
  abstract ingestWebhook(
    workspaceId: string,
    rawInput: unknown,
    signal?: AbortSignal
  ): Promise<QueuedResponse>;

  /**
   * Query files from a specific provider.
   */
  async getProviderFiles(
    workspaceId: string,
    options: ListProviderFilesOptions
  ): Promise<FileQueryItem[]> {
    const prefix = DEFAULT_PATH_PREFIXES[options.provider] ?? `/${options.provider}`;
    const pathFilter = options.objectType
      ? `${prefix}/${options.objectType}/`
      : `${prefix}/`;

    const properties: Record<string, string> = {
      provider: options.provider,
    };
    if (options.objectType) {
      properties["provider.object_type"] = options.objectType;
    }
    if (options.status) {
      properties["provider.status"] = options.status;
    }

    const allItems: FileQueryItem[] = [];
    let cursor: string | undefined;
    for (;;) {
      const response = await this.client.queryFiles(workspaceId, {
        path: pathFilter,
        properties,
        cursor,
        limit: options.limit,
        signal: options.signal,
      });
      allItems.push(...response.items);
      if (
        !response.nextCursor ||
        (options.limit && allItems.length >= options.limit)
      ) {
        break;
      }
      cursor = response.nextCursor;
    }
    return options.limit ? allItems.slice(0, options.limit) : allItems;
  }

  /**
   * Watch for file events from a specific provider.
   */
  async *watchProviderEvents(
    workspaceId: string,
    options: WatchProviderEventsOptions
  ): AsyncGenerator<FilesystemEvent, void, unknown> {
    const pollIntervalMs = options.pollIntervalMs ?? 5000;
    let cursor = options.cursor;

    for (;;) {
      if (options.signal?.aborted) return;
      const response = await this.client.getEvents(workspaceId, {
        provider: options.provider,
        cursor,
        signal: options.signal,
      });
      for (const event of response.events) {
        yield event;
      }
      if (response.nextCursor) {
        cursor = response.nextCursor;
      }
      if (options.signal?.aborted) return;
      await new Promise<void>((resolve) => {
        const timer = setTimeout(() => {
          options.signal?.removeEventListener("abort", onAbort);
          resolve();
        }, pollIntervalMs);
        const onAbort = () => {
          clearTimeout(timer);
          resolve();
        };
        options.signal?.addEventListener("abort", onAbort, { once: true });
      });
    }
  }
}

function requireConnectionId(
  event: WebhookInput & { connectionId?: unknown }
): NormalizedWebhook {
  if (
    typeof event.connectionId !== "string" ||
    event.connectionId.trim().length === 0
  ) {
    throw new WebhookNormalizationError(
      "invalid_webhook",
      "Webhook connectionId must be a non-empty string.",
      { value: event.connectionId }
    );
  }

  return {
    ...event,
    connectionId: event.connectionId.trim(),
  };
}
