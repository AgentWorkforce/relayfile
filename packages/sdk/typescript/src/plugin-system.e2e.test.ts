import { describe, expect, it, vi } from "vitest";

import {
  assertPathWasWritten,
  buildMockPathFromWebhook,
  createMockNormalizedWebhook,
  createMockRelayFileClient,
  makeRegisteredMockPlugin
} from "./testing/mock-system.js";

describe("plugin system end-to-end", () => {
  it("registers a mock adapter, routes a normalized webhook, and reports the written file", async () => {
    const client = createMockRelayFileClient();
    const registeredListener = vi.fn();
    const ingestedListener = vi.fn();
    const errorListener = vi.fn();

    client.on("registered", registeredListener);
    client.on("ingested", ingestedListener);
    client.on("error", errorListener);

    const adapter = makeRegisteredMockPlugin(client, {
      name: "github",
      version: "1.2.3-test"
    });
    const event = createMockNormalizedWebhook({
      provider: "github",
      eventType: "issues.opened",
      objectType: "issues",
      objectId: "42",
      payload: {
        id: "42",
        title: "Plugin system E2E coverage"
      }
    });
    const expectedPath = buildMockPathFromWebhook(event);

    expect(client.getAdapter("github")).toBe(adapter);
    expect(registeredListener).toHaveBeenCalledTimes(1);
    expect(registeredListener).toHaveBeenCalledWith({
      adapterName: "github",
      version: "1.2.3-test",
      providerName: "github"
    });

    const result = await client.routeWebhook("ws_acme", event);

    expect(client.routedWebhooks).toEqual([event]);
    assertPathWasWritten(client, expectedPath);
    expect(client.listWrittenPaths()).toEqual([expectedPath]);
    expect(client.readFile(expectedPath)).toMatchObject({
      workspaceId: "ws_acme",
      path: expectedPath,
      contentType: "application/json",
      encoding: "utf-8",
      semantics: {
        properties: {
          provider: "github",
          "provider.object_type": "issues",
          "provider.object_id": "42",
          "provider.event_type": "issues.opened",
          "provider.connection_id": "conn_test"
        }
      }
    });

    expect(result).toEqual({
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [expectedPath],
      errors: []
    });
    expect(ingestedListener).toHaveBeenCalledTimes(1);
    expect(ingestedListener).toHaveBeenCalledWith({
      adapterName: "github",
      workspaceId: "ws_acme",
      event,
      result
    });
    expect(errorListener).not.toHaveBeenCalled();
    expect(client.getLifecycleEvents()).toEqual([
      {
        type: "registered",
        payload: {
          adapterName: "github",
          version: "1.2.3-test",
          providerName: "github"
        }
      },
      {
        type: "ingested",
        payload: {
          adapterName: "github",
          workspaceId: "ws_acme",
          event,
          result
        }
      }
    ]);
  });

  it("emits an error event when no adapter is registered for the provider", async () => {
    const client = createMockRelayFileClient();
    const errorListener = vi.fn();
    const event = createMockNormalizedWebhook({
      provider: "slack",
      objectType: "messages",
      objectId: "msg-123"
    });

    client.on("error", errorListener);

    await expect(client.routeWebhook("ws_missing", event)).rejects.toThrow(
      "No adapter registered for provider: slack"
    );

    expect(client.listWrittenPaths()).toEqual([]);
    expect(errorListener).toHaveBeenCalledTimes(1);

    const errorPayload = errorListener.mock.calls[0]?.[0];
    expect(errorPayload?.adapterName).toBeUndefined();
    expect(errorPayload?.workspaceId).toBe("ws_missing");
    expect(errorPayload?.event).toEqual(event);
    expect(errorPayload?.error).toBeInstanceOf(Error);
    expect(errorPayload?.error.message).toBe("No adapter registered for provider: slack");

    expect(client.getLifecycleEvents()).toHaveLength(1);
    expect(client.getLifecycleEvents()[0]).toMatchObject({
      type: "error",
      payload: {
        workspaceId: "ws_missing",
        event
      }
    });
  });

  it("emits an error lifecycle event when a registered adapter throws during ingest", async () => {
    const client = createMockRelayFileClient();
    const ingestedListener = vi.fn();
    const errorListener = vi.fn();
    const event = createMockNormalizedWebhook({
      provider: "github",
      objectType: "pulls",
      objectId: "108"
    });

    client.on("ingested", ingestedListener);
    client.on("error", errorListener);

    makeRegisteredMockPlugin(client, {
      name: "github",
      ingestWebhook: () => {
        throw new Error("adapter ingest failed");
      }
    });

    await expect(client.routeWebhook("ws_failure", event)).rejects.toThrow("adapter ingest failed");

    expect(client.listWrittenPaths()).toEqual([]);
    expect(ingestedListener).not.toHaveBeenCalled();
    expect(errorListener).toHaveBeenCalledTimes(1);

    const errorPayload = errorListener.mock.calls[0]?.[0];
    expect(errorPayload?.adapterName).toBe("github");
    expect(errorPayload?.workspaceId).toBe("ws_failure");
    expect(errorPayload?.event).toEqual(event);
    expect(errorPayload?.error).toBeInstanceOf(Error);
    expect(errorPayload?.error.message).toBe("adapter ingest failed");

    expect(client.getLifecycleEvents().map((record) => record.type)).toEqual(["registered", "error"]);
  });
});
