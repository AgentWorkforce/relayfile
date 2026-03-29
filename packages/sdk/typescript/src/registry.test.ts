import { afterEach, describe, expect, it, vi } from "vitest";

import type { IngestResult } from "./adapter.js";
import { IntegrationAdapter } from "./adapter.js";
import { RelayFileClient } from "./client.js";
import {
  DuplicateAdapterNameError,
  MissingAdapterError
} from "./errors.js";
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
    payload: { title: `${provider} event` }
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

class TestAdapter extends IntegrationAdapter {
  readonly version = "1.0.0";

  constructor(
    readonly name: string,
    private readonly result: IngestResult = makeIngestResult(name)
  ) {
    super({} as RelayFileClient, makeProvider(name));
  }

  async ingestWebhook(
    _workspaceId: string,
    _event: NormalizedWebhook
  ): Promise<IngestResult> {
    return this.result;
  }

  computePath(objectType: string, objectId: string): string {
    return `/${this.name}/${objectType}/${objectId}.json`;
  }

  computeSemantics(
    _objectType: string,
    _objectId: string,
    _payload: Record<string, unknown>
  ): Record<string, never> {
    return {};
  }
}

function makeAdapter(name: string, result = makeIngestResult(name)) {
  const adapter = new TestAdapter(name, result);

  return {
    adapter,
    ingestWebhook: vi.spyOn(adapter, "ingestWebhook"),
    result
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

afterEach(() => {
  vi.restoreAllMocks();
});

describe("AdapterRegistry", () => {
  it("registerAdapter stores adapters by name", () => {
    const registry = new AdapterRegistry();
    const { adapter } = makeAdapter("github");

    expect(registry.registerAdapter(adapter)).toBe(adapter);
    expect(registry.getAdapter("github")).toBe(adapter);
  });

  it("throws when duplicate adapter names are registered", () => {
    const registry = new AdapterRegistry();

    registry.registerAdapter(makeAdapter("github").adapter);

    try {
      registry.registerAdapter(makeAdapter("github").adapter);
      throw new Error("Expected duplicate registration to throw.");
    } catch (error) {
      expect(error).toBeInstanceOf(DuplicateAdapterNameError);
      expect(error).toMatchObject({
        code: "duplicate_adapter_name",
        adapterName: "github",
        message: "Adapter already registered: github"
      });
    }
  });

  it("routeWebhook dispatches to the correct adapter", async () => {
    const registry = new AdapterRegistry();
    const github = makeAdapter("github");
    const slack = makeAdapter("slack");
    const event = makeEvent("slack");

    registry.registerAdapter(github.adapter);
    registry.registerAdapter(slack.adapter);

    await expect(registry.routeWebhook("ws_123", event)).resolves.toEqual(slack.result);
    expect(slack.ingestWebhook).toHaveBeenCalledWith("ws_123", event);
    expect(github.ingestWebhook).not.toHaveBeenCalled();
  });

  it("returns a clear error for unknown providers", async () => {
    const registry = new AdapterRegistry();

    await expect(registry.routeWebhook("ws_123", makeEvent("notion"))).rejects.toMatchObject({
      name: "MissingAdapterError",
      code: "missing_adapter",
      provider: "notion",
      message: "No adapter registered for provider: notion"
    });
  });
});

describe("RelayFileClient", () => {
  it("forwards registerAdapter to the registry", () => {
    const registerAdapter = vi.spyOn(AdapterRegistry.prototype, "registerAdapter");
    const client = makeClient();
    const { adapter } = makeAdapter("github");

    expect(client.registerAdapter(adapter)).toBe(adapter);
    expect(registerAdapter).toHaveBeenCalledWith(adapter);
  });

  it("forwards routeWebhook to the registry", async () => {
    const client = makeClient();
    const event = makeEvent("github");
    const result = makeIngestResult("github");
    const routeWebhook = vi
      .spyOn(AdapterRegistry.prototype, "routeWebhook")
      .mockResolvedValueOnce(result);

    await expect(client.routeWebhook("ws_123", event)).resolves.toEqual(result);
    expect(routeWebhook).toHaveBeenCalledWith("ws_123", event);
  });
});
