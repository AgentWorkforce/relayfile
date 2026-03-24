import { describe, it, expect, vi, beforeEach } from "vitest";
import { ComposioHelpers } from "./composio.js";
import type { ComposioWebhookPayload } from "./composio.js";
import type { RelayFileClient } from "./client.js";

// ---------------------------------------------------------------------------
// Mock client
// ---------------------------------------------------------------------------

function createMockClient() {
  return {
    ingestWebhook: vi.fn().mockResolvedValue({
      opId: "op_123",
      status: "queued" as const,
      targetRevision: "rev_1",
    }),
    queryFiles: vi.fn().mockResolvedValue({
      items: [],
      nextCursor: null,
    }),
    getEvents: vi.fn().mockResolvedValue({
      events: [],
      nextCursor: null,
    }),
  } as unknown as RelayFileClient;
}

// ---------------------------------------------------------------------------
// Test payloads
// ---------------------------------------------------------------------------

const githubCommitPayload: ComposioWebhookPayload = {
  type: "composio.trigger.message",
  metadata: {
    trigger_slug: "GITHUB_COMMIT_EVENT",
    trigger_id: "trig_abc123",
    connected_account_id: "ca_xyz789",
    user_id: "user_42",
    toolkit: "github",
  },
  data: {
    id: "sha-abc123",
    author: "khaliq",
    message: "fix: update billing logic",
    url: "https://github.com/org/repo/commit/sha-abc123",
    timestamp: "2026-03-15T10:00:00Z",
  },
};

const slackMessagePayload: ComposioWebhookPayload = {
  type: "composio.trigger.message",
  metadata: {
    trigger_slug: "SLACK_NEW_MESSAGE",
    trigger_id: "trig_slack1",
    connected_account_id: "ca_slack1",
    toolkit: "slack",
  },
  data: {
    id: "msg-123",
    text: "Hello from Slack!",
    channel: "#general",
    created_at: "2026-03-15T10:05:00Z",
  },
};

const zendeskTicketPayload: ComposioWebhookPayload = {
  type: "composio.trigger.message",
  metadata: {
    trigger_slug: "ZENDESK_NEW_TICKET",
    trigger_id: "trig_zd1",
    connected_account_id: "ca_zd1",
    user_id: "user_99",
    toolkit: "zendesk",
  },
  data: {
    id: "ticket-456",
    subject: "Login broken",
    status: "open",
    created_at: "2026-03-15T09:00:00Z",
    updated_at: "2026-03-15T10:00:00Z",
  },
};

const accountExpiredPayload: ComposioWebhookPayload = {
  type: "composio.connected_account.expired",
  metadata: {
    trigger_slug: "",
    trigger_id: "",
    connected_account_id: "ca_expired1",
    user_id: "user_10",
    toolkit: "github",
  },
  data: {},
};

const unknownTriggerPayload: ComposioWebhookPayload = {
  type: "composio.trigger.message",
  metadata: {
    trigger_slug: "NOTION_DATABASE_ROW_CREATED",
    trigger_id: "trig_notion1",
    connected_account_id: "ca_notion1",
    toolkit: "notion",
  },
  data: {
    id: "row-789",
    title: "New task",
  },
};

const nestedIdPayload: ComposioWebhookPayload = {
  type: "composio.trigger.message",
  metadata: {
    trigger_slug: "GITHUB_ISSUE_EVENT",
    trigger_id: "trig_issue1",
    connected_account_id: "ca_gh1",
    toolkit: "github",
  },
  data: {
    issue: { id: "issue-999", title: "Bug report" },
    action: "opened",
  },
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ComposioHelpers", () => {
  let client: ReturnType<typeof createMockClient>;
  let composio: ComposioHelpers;

  beforeEach(() => {
    client = createMockClient();
    composio = new ComposioHelpers(client as unknown as RelayFileClient);
  });

  describe("ingestWebhook", () => {
    it("ingests a GitHub commit event with correct path and semantics", async () => {
      await composio.ingestWebhook("ws_1", githubCommitPayload);

      expect(client.ingestWebhook).toHaveBeenCalledTimes(1);
      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];

      expect(call.workspaceId).toBe("ws_1");
      expect(call.provider).toBe("github");
      expect(call.event_type).toBe("event"); // GITHUB_COMMIT_EVENT doesn't match created/updated/deleted
      expect(call.path).toBe("/github/commits/sha-abc123.json");
      expect(call.headers["X-Composio-Trigger-Id"]).toBe("trig_abc123");
      expect(call.headers["X-Composio-Trigger-Slug"]).toBe("GITHUB_COMMIT_EVENT");
      expect(call.headers["X-Composio-Connected-Account"]).toBe("ca_xyz789");
      expect(call.headers["X-Composio-User-Id"]).toBe("user_42");

      // Check semantic properties in data
      expect(call.data.semantics.properties["composio.trigger_id"]).toBe("trig_abc123");
      expect(call.data.semantics.properties["composio.user_id"]).toBe("user_42");
      expect(call.data.semantics.properties.provider).toBe("github");
      expect(call.data.semantics.properties["provider.object_type"]).toBe("commits");
      expect(call.data.semantics.properties["provider.object_id"]).toBe("sha-abc123");
    });

    it("ingests a Slack message with correct path", async () => {
      await composio.ingestWebhook("ws_1", slackMessagePayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.path).toBe("/slack/messages/msg-123.json");
      expect(call.provider).toBe("slack");
      expect(call.event_type).toBe("created"); // NEW_MESSAGE → created
    });

    it("ingests a Zendesk ticket with status and timestamps", async () => {
      await composio.ingestWebhook("ws_1", zendeskTicketPayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.path).toBe("/zendesk/tickets/ticket-456.json");
      expect(call.event_type).toBe("created"); // NEW_TICKET → created

      const props = call.data.semantics.properties;
      expect(props["provider.status"]).toBe("open");
      expect(props["provider.created_at"]).toBe("2026-03-15T09:00:00Z");
      expect(props["provider.updated_at"]).toBe("2026-03-15T10:00:00Z");
    });

    it("handles account expired events", async () => {
      await composio.ingestWebhook("ws_1", accountExpiredPayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.path).toBe("/.system/composio/expired/ca_expired1.json");
      expect(call.event_type).toBe("account_expired");
      expect(call.data.connected_account_id).toBe("ca_expired1");
    });

    it("infers object type for unmapped trigger slugs", async () => {
      await composio.ingestWebhook("ws_1", unknownTriggerPayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      // NOTION_DATABASE_ROW_CREATED → infer "database_rows" (pluralized middle parts)
      expect(call.path).toBe("/notion/database_rows/row-789.json");
      expect(call.event_type).toBe("created");
    });

    it("extracts nested object IDs (e.g., issue.id)", async () => {
      await composio.ingestWebhook("ws_1", nestedIdPayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.path).toBe("/github/issues/issue-999.json");
    });

    it("omits user ID header when not provided", async () => {
      await composio.ingestWebhook("ws_1", slackMessagePayload);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.headers["X-Composio-User-Id"]).toBeUndefined();
    });

    it("passes through abort signal", async () => {
      const ac = new AbortController();
      await composio.ingestWebhook("ws_1", githubCommitPayload, ac.signal);

      const call = (client.ingestWebhook as ReturnType<typeof vi.fn>).mock.calls[0]![0];
      expect(call.signal).toBe(ac.signal);
    });
  });

  describe("normalize", () => {
    it("normalizes a GitHub commit to WebhookInput", () => {
      const normalized = composio.normalize(githubCommitPayload);

      expect(normalized.provider).toBe("github");
      expect(normalized.objectType).toBe("commits");
      expect(normalized.objectId).toBe("sha-abc123");
      expect(normalized.eventType).toBe("event");
      expect(normalized.payload).toBe(githubCommitPayload.data);
      expect(normalized.metadata?.["composio.trigger_id"]).toBe("trig_abc123");
      expect(normalized.metadata?.["composio.user_id"]).toBe("user_42");
    });

    it("normalizes a Slack message to WebhookInput", () => {
      const normalized = composio.normalize(slackMessagePayload);

      expect(normalized.provider).toBe("slack");
      expect(normalized.objectType).toBe("messages");
      expect(normalized.objectId).toBe("msg-123");
      expect(normalized.eventType).toBe("created");
      expect(normalized.metadata?.["composio.user_id"]).toBeUndefined();
    });

    it("normalizes unknown triggers with inferred object type", () => {
      const normalized = composio.normalize(unknownTriggerPayload);

      expect(normalized.provider).toBe("notion");
      expect(normalized.objectType).toBe("database_rows");
      expect(normalized.objectId).toBe("row-789");
      expect(normalized.eventType).toBe("created");
    });
  });

  describe("getProviderFiles", () => {
    it("queries files for a specific provider", async () => {
      await composio.getProviderFiles("ws_1", { provider: "github" });

      expect(client.queryFiles).toHaveBeenCalledWith("ws_1", {
        path: "/github/",
        properties: { provider: "github" },
        cursor: undefined,
        limit: undefined,
        signal: undefined,
      });
    });

    it("filters by object type", async () => {
      await composio.getProviderFiles("ws_1", {
        provider: "zendesk",
        objectType: "tickets",
        status: "open",
      });

      expect(client.queryFiles).toHaveBeenCalledWith("ws_1", {
        path: "/zendesk/tickets/",
        properties: {
          provider: "zendesk",
          "provider.object_type": "tickets",
          "provider.status": "open",
        },
        cursor: undefined,
        limit: undefined,
        signal: undefined,
      });
    });
  });

  describe("name", () => {
    it("returns 'composio'", () => {
      expect(composio.name).toBe("composio");
    });
  });
});
