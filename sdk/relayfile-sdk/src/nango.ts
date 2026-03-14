import type { RelayFileClient } from "./client.js";
import type {
  FileQueryItem,
  FilesystemEvent,
  FileSemantics,
  QueuedResponse
} from "./types.js";

export interface NangoWebhookInput {
  connectionId: string;
  integrationId: string;
  providerConfigKey?: string;
  model: string;
  objectId: string;
  eventType: string;
  payload: Record<string, unknown>;
  relations?: string[];
}

export interface GetProviderFilesOptions {
  provider: string;
  objectType?: string;
  status?: string;
  limit?: number;
  signal?: AbortSignal;
}

export interface WatchProviderEventsOptions {
  provider: string;
  pollIntervalMs?: number;
  cursor?: string;
  signal?: AbortSignal;
}

const PROVIDER_PATH_PREFIX: Record<string, string> = {
  zendesk: "/zendesk",
  shopify: "/shopify",
  github: "/github",
  stripe: "/stripe",
  slack: "/slack",
  linear: "/linear",
  jira: "/jira",
  hubspot: "/hubspot",
  salesforce: "/salesforce"
};

function computeCanonicalPath(provider: string, model: string, objectId: string): string {
  const prefix = PROVIDER_PATH_PREFIX[provider] ?? `/${provider}`;
  return `${prefix}/${model}/${objectId}.json`;
}

function buildSemanticProperties(
  provider: string,
  input: NangoWebhookInput,
  payload: Record<string, unknown>
): Record<string, string> {
  const properties: Record<string, string> = {
    "nango.connection_id": input.connectionId,
    "nango.integration_id": input.integrationId,
    "provider": provider,
    "provider.object_type": input.model,
    "provider.object_id": input.objectId
  };
  if (input.providerConfigKey) {
    properties["nango.provider_config_key"] = input.providerConfigKey;
  }
  if (typeof payload.status === "string") {
    properties["provider.status"] = payload.status;
  }
  if (typeof payload.updated_at === "string") {
    properties["provider.updated_at"] = payload.updated_at;
  }
  if (typeof payload.created_at === "string") {
    properties["provider.created_at"] = payload.created_at;
  }
  return properties;
}

export class NangoHelpers {
  private readonly client: RelayFileClient;

  constructor(client: RelayFileClient) {
    this.client = client;
  }

  async ingestNangoWebhook(
    workspaceId: string,
    input: NangoWebhookInput,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    const provider = input.integrationId.split("-")[0] ?? input.integrationId;
    const path = computeCanonicalPath(provider, input.model, input.objectId);
    const properties = buildSemanticProperties(provider, input, input.payload);

    return this.client.ingestWebhook({
      workspaceId,
      provider: provider as string,
      event_type: input.eventType,
      path,
      data: {
        ...input.payload,
        semantics: {
          properties,
          relations: input.relations ?? []
        } satisfies FileSemantics
      },
      headers: {
        "X-Nango-Connection-Id": input.connectionId,
        "X-Nango-Integration-Id": input.integrationId,
        ...(input.providerConfigKey
          ? { "X-Nango-Provider-Config-Key": input.providerConfigKey }
          : {})
      },
      signal
    });
  }

  async getProviderFiles(
    workspaceId: string,
    options: GetProviderFilesOptions
  ): Promise<FileQueryItem[]> {
    const prefix = PROVIDER_PATH_PREFIX[options.provider] ?? `/${options.provider}`;
    const pathFilter = options.objectType
      ? `${prefix}/${options.objectType}/`
      : `${prefix}/`;

    const properties: Record<string, string> = {
      provider: options.provider
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
        signal: options.signal
      });
      allItems.push(...response.items);
      if (!response.nextCursor || (options.limit && allItems.length >= options.limit)) {
        break;
      }
      cursor = response.nextCursor;
    }
    return options.limit ? allItems.slice(0, options.limit) : allItems;
  }

  async *watchProviderEvents(
    workspaceId: string,
    options: WatchProviderEventsOptions
  ): AsyncGenerator<FilesystemEvent, void, unknown> {
    const pollIntervalMs = options.pollIntervalMs ?? 5000;
    let cursor = options.cursor;

    for (;;) {
      if (options.signal?.aborted) {
        return;
      }
      const response = await this.client.getEvents(workspaceId, {
        provider: options.provider,
        cursor,
        signal: options.signal
      });
      for (const event of response.events) {
        yield event;
      }
      if (response.nextCursor) {
        cursor = response.nextCursor;
      }
      if (options.signal?.aborted) {
        return;
      }
      await new Promise<void>((resolve, reject) => {
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
