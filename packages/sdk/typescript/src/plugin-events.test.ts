import { afterEach, describe, expect, it, vi } from "vitest";

import type { IngestResult } from "./adapter.js";
import { IntegrationAdapter } from "./adapter.js";
import { RelayFileClient } from "./client.js";
import { createPluginEventEmitter } from "./plugin-events.js";
import type { ConnectionProvider, NormalizedWebhook } from "./provider.js";
import { AdapterRegistry } from "./registry.js";

function makeProvider(name: string): ConnectionProvider {
  return {
    name,
    proxy: vi.fn(),
    healthCheck: vi.fn()
  };
}

function makeEvent(provider: string): NormalizedWebhook {
  return {
    provider,
    connectionId: `conn_${provider}`,
    objectType: "issues",
    objectId: "123",
    eventType: "updated",
    payload: { title: `${provider} issue` }
  };
}

function makeIngestResult(provider: string): IngestResult {
  return {
    filesWritten: 1,
    filesUpdated: 0,
    filesDeleted: 0,
    paths: [`/${provider}/issues/123.json`],
    errors: []
  };
}

function makeClient(): RelayFileClient {
  return new RelayFileClient({
    baseUrl: "https://relayfile.test",
    token: "tok_test",
    fetchImpl: vi.fn() as unknown as typeof fetch,
    retry: { maxRetries: 0 }
  });
}

class TestAdapter extends IntegrationAdapter {
  readonly version = "1.0.0";

  constructor(
    client: RelayFileClient,
    readonly name: string,
    private readonly ingestImpl: (
      workspaceId: string,
      event: NormalizedWebhook
    ) => Promise<IngestResult>,
    private readonly declaredSupportedEvents?: readonly string[]
  ) {
    super(client, makeProvider(name));
  }

  ingestWebhook(
    workspaceId: string,
    event: NormalizedWebhook
  ): Promise<IngestResult> {
    return this.ingestImpl(workspaceId, event);
  }

  computePath(objectType: string, objectId: string): string {
    return `/${this.name}/${objectType}/${objectId}.json`;
  }

  computeSemantics(): Record<string, never> {
    return {};
  }

  supportedEvents(): string[] | undefined {
    return this.declaredSupportedEvents
      ? [...this.declaredSupportedEvents]
      : undefined;
  }
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("plugin events", () => {
  it("listeners receive registered events", () => {
    const registry = new AdapterRegistry();
    const client = makeClient();
    const adapter = new TestAdapter(
      client,
      "github",
      async () => makeIngestResult("github"),
      ["issues.updated"]
    );
    const onRegistered = vi.fn();

    registry.on("registered", onRegistered);
    registry.registerAdapter(adapter);

    expect(onRegistered).toHaveBeenCalledTimes(1);
    expect(onRegistered).toHaveBeenCalledWith({
      adapterName: "github",
      version: "1.0.0",
      providerName: "github",
      supportedEvents: ["issues.updated"]
    });
  });

  it("listeners receive ingested events with result payloads", async () => {
    const registry = new AdapterRegistry();
    const client = makeClient();
    const event = makeEvent("github");
    const result = makeIngestResult("github");
    const adapter = new TestAdapter(client, "github", async () => result);
    const onIngested = vi.fn();

    registry.on("ingested", onIngested);
    registry.registerAdapter(adapter);

    await expect(registry.routeWebhook("ws_123", event)).resolves.toEqual(result);
    expect(onIngested).toHaveBeenCalledTimes(1);
    expect(onIngested).toHaveBeenCalledWith({
      adapterName: "github",
      workspaceId: "ws_123",
      event,
      result
    });
  });

  it("error events include adapter name and failure details", async () => {
    const registry = new AdapterRegistry();
    const client = makeClient();
    const event = makeEvent("github");
    const failure = new Error("Webhook signature mismatch");
    const adapter = new TestAdapter(client, "github", async () => {
      throw failure;
    });
    const onError = vi.fn();

    registry.on("error", onError);
    registry.registerAdapter(adapter);

    await expect(registry.routeWebhook("ws_123", event)).rejects.toThrow(
      "Webhook signature mismatch"
    );
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith({
      adapterName: "github",
      workspaceId: "ws_123",
      event,
      error: failure
    });
  });

  it("unsubscribe removes listeners", () => {
    const emitter = createPluginEventEmitter();
    const listener = vi.fn();
    const unsubscribe = emitter.on("registered", listener);

    expect(emitter.listenerCount("registered")).toBe(1);

    unsubscribe();
    emitter.emit("registered", {
      adapterName: "github",
      version: "1.0.0",
      providerName: "github"
    });

    expect(emitter.listenerCount("registered")).toBe(0);
    expect(listener).not.toHaveBeenCalled();
  });

  it("registry and client hooks do not double-emit", async () => {
    const client = makeClient();
    const event = makeEvent("github");
    const result = makeIngestResult("github");
    const adapter = new TestAdapter(client, "github", async () => result);
    const onRegistered = vi.fn();
    const onIngested = vi.fn();

    client.on("registered", onRegistered);
    client.on("ingested", onIngested);

    client.registerAdapter(adapter);
    await expect(client.routeWebhook("ws_123", event)).resolves.toEqual(result);

    expect(onRegistered).toHaveBeenCalledTimes(1);
    expect(onRegistered).toHaveBeenCalledWith({
      adapterName: "github",
      version: "1.0.0",
      providerName: "github",
      supportedEvents: undefined
    });
    expect(onIngested).toHaveBeenCalledTimes(1);
    expect(onIngested).toHaveBeenCalledWith({
      adapterName: "github",
      workspaceId: "ws_123",
      event,
      result
    });
  });
});
