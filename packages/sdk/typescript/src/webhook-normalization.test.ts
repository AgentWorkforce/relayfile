import { describe, expect, it } from "vitest";

import * as sdk from "./index.js";
import {
  WebhookNormalizationError as ProviderWebhookNormalizationError,
  fromWebhookInput as providerFromWebhookInput,
  normalizeWebhook as providerNormalizeWebhook,
  toIngestWebhookInput as providerToIngestWebhookInput,
} from "./provider.js";

describe("webhook normalization", () => {
  it("converts legacy WebhookInput shapes into normalized webhook events", () => {
    const legacyInput = {
      provider: " github ",
      connection_id: " conn_123 ",
      object_type: " issues ",
      object_id: " issue_42 ",
      event_type: " issue.updated ",
      data: {
        id: "issue_42",
        title: "Normalization regression",
      },
      relations: [" parent_1 ", " repo_9 "],
      metadata: {
        source: " legacy-bridge ",
        ignored: 42,
      },
      headers: {
        "x-request-id": " req_123 ",
        attempts: 2,
      },
      delivery_id: " delivery_1 ",
      received_at: " 2026-03-28T11:00:00Z ",
    };

    const normalized = sdk.fromWebhookInput(
      legacyInput as unknown as sdk.WebhookInput
    );

    expect(normalized.provider).toBe("github");
    expect(normalized.connectionId).toBe("conn_123");
    expect(normalized.objectType).toBe("issues");
    expect(normalized.objectId).toBe("issue_42");
    expect(normalized.eventType).toBe("issue.updated");
    expect(normalized.payload).toEqual(legacyInput.data);
    expect(normalized.relations).toEqual(["parent_1", "repo_9"]);
    expect(normalized).toMatchObject({
      metadata: { source: "legacy-bridge" },
      headers: { "x-request-id": "req_123" },
      deliveryId: "delivery_1",
      timestamp: "2026-03-28T11:00:00Z",
    });
  });

  it("throws typed errors for missing fields and malformed payloads", () => {
    expect(() =>
      sdk.normalizeWebhook(
        {
          connectionId: "conn_1",
          objectId: "issue_42",
          eventType: "updated",
          payload: {},
        },
        "github"
      )
    ).toThrowError(
      expect.objectContaining({
        name: "WebhookNormalizationError",
        code: "invalid_object_type",
        field: "objectType",
      })
    );

    expect(() =>
      sdk.normalizeWebhook(
        {
          connectionId: "conn_1",
          objectType: "issues",
          objectId: "issue_42",
          eventType: "updated",
          payload: "broken",
        },
        "github"
      )
    ).toThrowError(
      expect.objectContaining({
        name: "WebhookNormalizationError",
        code: "invalid_payload",
        field: "payload",
      })
    );
  });

  it("toIngestWebhookInput preserves provider, event type, and data", () => {
    const legacyInput = {
      provider: "github",
      connectionId: "conn_123",
      objectType: "issues",
      objectId: "issue_42",
      eventType: "issue.updated",
      payload: {
        id: "issue_42",
        title: "Normalization regression",
      },
      headers: {
        "x-request-id": "req_123",
      },
      deliveryId: "delivery_1",
      timestamp: "2026-03-28T11:00:00Z",
    };

    const ingestInput = sdk.toIngestWebhookInput(
      legacyInput as unknown as sdk.WebhookInput,
      " /github/issues/issue_42.json "
    );

    expect(ingestInput).toEqual({
      provider: "github",
      event_type: "issue.updated",
      path: "/github/issues/issue_42.json",
      data: {
        id: "issue_42",
        title: "Normalization regression",
      },
      headers: {
        "x-request-id": "req_123",
      },
      delivery_id: "delivery_1",
      timestamp: "2026-03-28T11:00:00Z",
    });
  });

  it("re-exports webhook normalization APIs from src/index.ts", () => {
    expect(sdk.WebhookNormalizationError).toBe(
      ProviderWebhookNormalizationError
    );
    expect(sdk.fromWebhookInput).toBe(providerFromWebhookInput);
    expect(sdk.normalizeWebhook).toBe(providerNormalizeWebhook);
    expect(sdk.toIngestWebhookInput).toBe(providerToIngestWebhookInput);
    expect(sdk.computeCanonicalPath("github", "issues", "42")).toBe(
      "/github/issues/42.json"
    );
  });
});
