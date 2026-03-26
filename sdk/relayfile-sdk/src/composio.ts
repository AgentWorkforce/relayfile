/**
 * Composio integration bridge for Relayfile.
 *
 * Maps Composio trigger webhook events → Relayfile filesystem paths + semantics.
 *
 * Composio webhook payload format (V3):
 * {
 *   "type": "composio.trigger.message",
 *   "metadata": {
 *     "trigger_slug": "GITHUB_COMMIT_EVENT",
 *     "trigger_id": "trig_abc123",
 *     "connected_account_id": "ca_xyz789",
 *     "user_id": "user_123",
 *     "toolkit": "github"
 *   },
 *   "data": {
 *     // trigger-specific payload (e.g., commit author, message, url)
 *   }
 * }
 *
 * @see https://docs.composio.dev/docs/triggers
 * @see https://docs.composio.dev/docs/setting-up-triggers/subscribing-to-events
 */

import type { RelayFileClient } from "./client.js";
import type {
  FileSemantics,
  QueuedResponse
} from "./types.js";
import {
  IntegrationProvider,
  computeCanonicalPath,
  type WebhookInput,
} from "./provider.js";

// ---------------------------------------------------------------------------
// Composio-specific types
// ---------------------------------------------------------------------------

/** Raw webhook payload from Composio (V3 format) */
export interface ComposioWebhookPayload {
  type: "composio.trigger.message" | "composio.connected_account.expired";
  metadata: {
    trigger_slug: string;
    trigger_id: string;
    connected_account_id: string;
    user_id?: string;
    toolkit: string;
  };
  data: Record<string, unknown>;
}

/** Options for creating Composio trigger subscriptions */
export interface ComposioTriggerOptions {
  /** Composio API key */
  apiKey: string;
  /** Webhook URL to receive events */
  webhookUrl: string;
  /** Base URL for Composio API (default: https://backend.composio.dev) */
  baseUrl?: string;
}

// ---------------------------------------------------------------------------
// Trigger slug → Relayfile object type mapping
// ---------------------------------------------------------------------------

/**
 * Maps Composio trigger slugs to Relayfile object types.
 * The trigger slug format is usually: {TOOLKIT}_{EVENT_NAME}
 * e.g., GITHUB_COMMIT_EVENT → "commits", SLACK_NEW_MESSAGE → "messages"
 */
const TRIGGER_OBJECT_TYPE: Record<string, string> = {
  // GitHub
  GITHUB_COMMIT_EVENT: "commits",
  GITHUB_PULL_REQUEST_EVENT: "pull_requests",
  GITHUB_ISSUE_EVENT: "issues",
  GITHUB_STAR_EVENT: "stars",
  GITHUB_PUSH_EVENT: "pushes",
  // Slack
  SLACK_NEW_MESSAGE: "messages",
  SLACK_CHANNEL_CREATED: "channels",
  SLACK_REACTION_ADDED: "reactions",
  // Gmail
  GMAIL_NEW_EMAIL: "emails",
  // Zendesk
  ZENDESK_NEW_TICKET: "tickets",
  ZENDESK_TICKET_UPDATED: "tickets",
  // Shopify
  SHOPIFY_NEW_ORDER: "orders",
  SHOPIFY_ORDER_UPDATED: "orders",
  // Stripe
  STRIPE_PAYMENT_RECEIVED: "payments",
  STRIPE_INVOICE_CREATED: "invoices",
  // Jira
  JIRA_ISSUE_CREATED: "issues",
  JIRA_ISSUE_UPDATED: "issues",
  // HubSpot
  HUBSPOT_CONTACT_CREATED: "contacts",
  HUBSPOT_DEAL_CREATED: "deals",
  // Notion
  NOTION_PAGE_UPDATED: "pages",
  // Linear
  LINEAR_ISSUE_CREATED: "issues",
  LINEAR_ISSUE_UPDATED: "issues",
  // Intercom
  INTERCOM_NEW_CONVERSATION: "conversations",
  // Freshdesk
  FRESHDESK_TICKET_CREATED: "tickets",
  FRESHDESK_TICKET_UPDATED: "tickets",
};

/**
 * Extract event type (created/updated/deleted) from trigger slug.
 */
function extractEventType(triggerSlug: string): string {
  const slug = triggerSlug.toUpperCase();
  if (slug.includes("CREATED") || slug.includes("NEW")) return "created";
  if (slug.includes("UPDATED") || slug.includes("CHANGED")) return "updated";
  if (slug.includes("DELETED") || slug.includes("REMOVED")) return "deleted";
  return "event";
}

/**
 * Extract a stable object ID from Composio event data.
 * Different triggers have different ID fields.
 */
function extractObjectId(data: Record<string, unknown>): string | null {
  // Common ID fields across various providers
  const idFields = ["id", "objectId", "object_id", "ticketId", "issueId", "messageId", "orderId", "commitId"];
  for (const field of idFields) {
    const val = data[field];
    if (typeof val === "string" || typeof val === "number") {
      return String(val);
    }
  }
  // Nested ID patterns
  if (typeof data.issue === "object" && data.issue !== null && "id" in data.issue) {
    return String((data.issue as Record<string, unknown>).id);
  }
  if (typeof data.pull_request === "object" && data.pull_request !== null && "id" in data.pull_request) {
    return String((data.pull_request as Record<string, unknown>).id);
  }
  return null;
}

function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") {
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map((item) => stableStringify(item)).join(",")}]`;
  }
  const entries = Object.entries(value as Record<string, unknown>)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, entryValue]) => `${JSON.stringify(key)}:${stableStringify(entryValue)}`);
  return `{${entries.join(",")}}`;
}

function hashString(value: string): string {
  let hash = 2166136261;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function deriveObjectId(input: ComposioWebhookPayload): string {
  const directId = extractObjectId(input.data);
  if (directId) {
    return directId;
  }
  const fallbackSeed = stableStringify({
    type: input.type,
    metadata: input.metadata,
    data: input.data,
  });
  return `auto-${hashString(fallbackSeed)}`;
}

/**
 * Infer the object type from trigger slug when not in the mapping table.
 * Format: TOOLKIT_OBJECT_EVENT → "objects" (pluralized)
 */
function inferObjectType(triggerSlug: string): string {
  // Remove toolkit prefix and event suffix
  const parts = triggerSlug.toLowerCase().split("_");
  if (parts.length >= 3) {
    // e.g., GITHUB_COMMIT_EVENT → "commit" → "commits"
    const objectPart = parts.slice(1, -1).join("_");
    return objectPart.endsWith("s") ? objectPart : `${objectPart}s`;
  }
  return "events";
}

// ---------------------------------------------------------------------------
// Composio provider implementation
// ---------------------------------------------------------------------------

export class ComposioHelpers extends IntegrationProvider {
  readonly name = "composio";

  /**
   * Ingest a Composio webhook payload into Relayfile.
   *
   * @param workspaceId - Relayfile workspace ID
   * @param rawInput - Raw Composio webhook payload (ComposioWebhookPayload)
   * @param signal - Optional abort signal
   */
  async ingestWebhook(
    workspaceId: string,
    rawInput: unknown,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    const input = rawInput as ComposioWebhookPayload;

    if (input.type === "composio.connected_account.expired") {
      // Handle account expiry as a system event
      return this.client.ingestWebhook({
        workspaceId,
        provider: input.metadata.toolkit || "composio",
        event_type: "account_expired",
        path: `/.system/composio/expired/${input.metadata.connected_account_id}.json`,
        data: {
          connected_account_id: input.metadata.connected_account_id,
          user_id: input.metadata.user_id,
          toolkit: input.metadata.toolkit,
          expired_at: new Date().toISOString(),
        },
        headers: {},
        signal,
      });
    }

    const toolkit = input.metadata.toolkit;
    const triggerSlug = input.metadata.trigger_slug;
    const objectType =
      TRIGGER_OBJECT_TYPE[triggerSlug] ?? inferObjectType(triggerSlug);
    const objectId = deriveObjectId(input);
    const eventType = extractEventType(triggerSlug);

    const path = computeCanonicalPath(toolkit, objectType, objectId);
    const properties = this.buildSemanticProperties(input, objectId);
    const semantics: FileSemantics = {
      properties,
      relations: [],
    };

    return this.client.ingestWebhook({
      workspaceId,
      provider: toolkit,
      event_type: eventType,
      path,
      data: {
        ...input.data,
        semantics,
      },
      headers: {
        "X-Composio-Trigger-Id": input.metadata.trigger_id,
        "X-Composio-Trigger-Slug": triggerSlug,
        "X-Composio-Connected-Account": input.metadata.connected_account_id,
        ...(input.metadata.user_id
          ? { "X-Composio-User-Id": input.metadata.user_id }
          : {}),
      },
      signal,
    });
  }

  /**
   * Normalize a Composio webhook payload to the generic WebhookInput format.
   * Useful for custom processing pipelines.
   */
  normalize(input: ComposioWebhookPayload): WebhookInput {
    const toolkit = input.metadata.toolkit;
    const triggerSlug = input.metadata.trigger_slug;
    const objectId = deriveObjectId(input);
    return {
      provider: toolkit,
      objectType:
        TRIGGER_OBJECT_TYPE[triggerSlug] ?? inferObjectType(triggerSlug),
      objectId,
      eventType: extractEventType(triggerSlug),
      payload: input.data,
      metadata: {
        "composio.trigger_id": input.metadata.trigger_id,
        "composio.trigger_slug": triggerSlug,
        "composio.connected_account_id": input.metadata.connected_account_id,
        ...(input.metadata.user_id
          ? { "composio.user_id": input.metadata.user_id }
          : {}),
      },
    };
  }

  private buildSemanticProperties(
    input: ComposioWebhookPayload,
    objectId: string
  ): Record<string, string> {
    const properties: Record<string, string> = {
      "composio.trigger_id": input.metadata.trigger_id,
      "composio.trigger_slug": input.metadata.trigger_slug,
      "composio.connected_account_id": input.metadata.connected_account_id,
      provider: input.metadata.toolkit,
      "provider.object_type":
        TRIGGER_OBJECT_TYPE[input.metadata.trigger_slug] ??
        inferObjectType(input.metadata.trigger_slug),
      "provider.object_id": objectId,
    };
    if (input.metadata.user_id) {
      properties["composio.user_id"] = input.metadata.user_id;
    }
    // Extract common fields from data
    const data = input.data;
    if (typeof data.status === "string") {
      properties["provider.status"] = data.status;
    }
    if (typeof data.updated_at === "string") {
      properties["provider.updated_at"] = data.updated_at;
    }
    if (typeof data.created_at === "string") {
      properties["provider.created_at"] = data.created_at;
    }
    return properties;
  }
}
