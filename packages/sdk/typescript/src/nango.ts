/**
 * Nango integration bridge for Relayfile.
 *
 * Maps Nango sync webhook events → Relayfile filesystem paths + semantics.
 *
 * @see https://docs.nango.dev/integrate/guides/receive-webhooks-from-nango
 */

import type { RelayFileClient } from "./client.js";
import type {
  FileQueryItem,
  FilesystemEvent,
  FileSemantics,
  QueuedResponse
} from "./types.js";
import {
  IntegrationProvider,
  computeCanonicalPath,
  type WebhookInput,
} from "./provider.js";

// ---------------------------------------------------------------------------
// Nango-specific types
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Nango provider implementation
// ---------------------------------------------------------------------------

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

export class NangoHelpers extends IntegrationProvider {
  readonly name = "nango";

  /**
   * Ingest a Nango webhook event into Relayfile.
   *
   * @param workspaceId - Relayfile workspace ID
   * @param rawInput - NangoWebhookInput
   * @param signal - Optional abort signal
   */
  async ingestWebhook(
    workspaceId: string,
    rawInput: unknown,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    const input = rawInput as NangoWebhookInput;
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

  /**
   * Normalize a Nango webhook input to the generic WebhookInput format.
   */
  normalize(input: NangoWebhookInput): WebhookInput {
    const provider = input.integrationId.split("-")[0] ?? input.integrationId;
    return {
      provider,
      objectType: input.model,
      objectId: input.objectId,
      eventType: input.eventType,
      payload: input.payload,
      relations: input.relations,
      metadata: {
        "nango.connection_id": input.connectionId,
        "nango.integration_id": input.integrationId,
        ...(input.providerConfigKey
          ? { "nango.provider_config_key": input.providerConfigKey }
          : {}),
      },
    };
  }
}
