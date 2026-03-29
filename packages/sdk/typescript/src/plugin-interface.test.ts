import { describe, expect, it, vi } from "vitest";

import { RelayFileClient } from "./client.js";
import type {
  ConnectionProvider,
  NormalizedWebhook,
  ProxyRequest,
  ProxyResponse,
} from "./provider.js";
import type { FileSemantics } from "./types.js";
import * as sdk from "./index.js";
import type {
  ConnectionProvider as ExportedConnectionProvider,
  IngestResult as ExportedIngestResult,
  SyncResult as ExportedSyncResult,
} from "./index.js";

function makeClient() {
  return new RelayFileClient({
    baseUrl: "https://relay.test",
    token: "tok_test",
    fetchImpl: vi.fn() as unknown as typeof fetch,
    retry: { maxRetries: 0 },
  });
}

function createMockProvider() {
  return {
    name: "mock-provider",
    proxy: vi.fn(
      async (request: ProxyRequest): Promise<ProxyResponse> => ({
        status: 200,
        headers: {
          "x-provider": "mock-provider",
          "x-connection-id": request.connectionId,
        },
        data: {
          endpoint: request.endpoint,
          method: request.method,
        },
      }),
    ),
    healthCheck: vi.fn(async (connectionId: string) => connectionId.startsWith("conn_")),
    handleWebhook: vi.fn(async (rawPayload: unknown): Promise<NormalizedWebhook> => ({
      provider: "mock-provider",
      connectionId: "conn_123",
      objectType: "issues",
      objectId: "42",
      eventType: "updated",
      payload: rawPayload as Record<string, unknown>,
      metadata: {
        source: "test",
      },
    })),
  } satisfies ConnectionProvider;
}

class MockAdapter extends sdk.IntegrationAdapter {
  readonly name = "mock-provider";
  readonly version = "1.0.0";

  async ingestWebhook(
    _workspaceId: string,
    event: NormalizedWebhook,
  ): Promise<ExportedIngestResult> {
    const path = this.computePath(event.objectType, event.objectId);
    const proxyResponse = await this.provider.proxy({
      method: "GET",
      baseUrl: "https://api.mock-provider.test",
      endpoint: `/objects/${event.objectId}`,
      connectionId: event.connectionId,
    });
    const healthy = await this.provider.healthCheck(event.connectionId);

    return {
      filesWritten: healthy && proxyResponse.status === 200 ? 1 : 0,
      filesUpdated: event.eventType === "updated" ? 1 : 0,
      filesDeleted: event.eventType === "deleted" ? 1 : 0,
      paths: [path],
      errors: [],
    };
  }

  computePath(objectType: string, objectId: string): string {
    return `/${this.provider.name}/${objectType}/${objectId}.json`;
  }

  computeSemantics(
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>,
  ): FileSemantics {
    return {
      properties: {
        provider: this.provider.name,
        "provider.object_type": objectType,
        "provider.object_id": objectId,
        title: String(payload.title ?? ""),
      },
      relations: ["/workspaces/ws_acme"],
    };
  }

  async sync(workspaceId: string): Promise<ExportedSyncResult> {
    return {
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [`/${this.provider.name}/sync/${workspaceId}.json`],
      errors: [],
      nextCursor: "cursor_sync_1",
    };
  }

  supportedEvents(): string[] {
    return ["created", "updated", "deleted"];
  }
}

describe("plugin interface surface", () => {
  it("ConnectionProvider mock satisfies the contract", async () => {
    const provider = createMockProvider();
    const exportedProvider: ExportedConnectionProvider = provider;
    const validated = sdk.validateConnectionProvider(provider);

    expect(exportedProvider.name).toBe("mock-provider");
    expect(validated).toEqual({
      type: "connection-provider",
      name: "mock-provider",
      provider,
    });

    const proxyResponse = await provider.proxy({
      method: "GET",
      baseUrl: "https://api.mock-provider.test",
      endpoint: "/objects/42",
      connectionId: "conn_123",
    });

    expect(proxyResponse).toEqual({
      status: 200,
      headers: {
        "x-provider": "mock-provider",
        "x-connection-id": "conn_123",
      },
      data: {
        endpoint: "/objects/42",
        method: "GET",
      },
    });
    expect(await provider.healthCheck("conn_123")).toBe(true);
    await expect(provider.handleWebhook?.({ title: "Contract check" })).resolves.toEqual({
      provider: "mock-provider",
      connectionId: "conn_123",
      objectType: "issues",
      objectId: "42",
      eventType: "updated",
      payload: { title: "Contract check" },
      metadata: {
        source: "test",
      },
    });
  });

  it("IntegrationAdapter subclass compiles and runs", async () => {
    const client = makeClient();
    const provider = createMockProvider();
    const adapter = new MockAdapter(client, provider);
    const event = await provider.handleWebhook?.({ title: "SDK adapter test" });
    const validated = sdk.validateIntegrationAdapter(adapter);

    expect(event).toBeDefined();
    expect(validated).toMatchObject({
      type: "integration-adapter",
      name: "mock-provider",
      version: "1.0.0",
      key: "mock-provider@1.0.0",
      providerName: "mock-provider",
    });
    expect(adapter.computePath("issues", "42")).toBe("/mock-provider/issues/42.json");
    expect(adapter.computeSemantics("issues", "42", { title: "SDK adapter test" })).toEqual({
      properties: {
        provider: "mock-provider",
        "provider.object_type": "issues",
        "provider.object_id": "42",
        title: "SDK adapter test",
      },
      relations: ["/workspaces/ws_acme"],
    });
    expect(adapter.supportedEvents()).toEqual(["created", "updated", "deleted"]);

    const ingestResult = await adapter.ingestWebhook("ws_acme", event!);
    expect(ingestResult).toEqual({
      filesWritten: 1,
      filesUpdated: 1,
      filesDeleted: 0,
      paths: ["/mock-provider/issues/42.json"],
      errors: [],
    });

    await expect(adapter.sync?.("ws_acme")).resolves.toEqual({
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: ["/mock-provider/sync/ws_acme.json"],
      errors: [],
      nextCursor: "cursor_sync_1",
    });
  });

  it("IngestResult and SyncResult shapes are stable", () => {
    const ingestResult = {
      filesWritten: 2,
      filesUpdated: 1,
      filesDeleted: 0,
      paths: ["/mock-provider/issues/42.json", "/mock-provider/issues/99.json"],
      errors: [{ path: "/mock-provider/issues/99.json", error: "conflict" }],
    } satisfies ExportedIngestResult;

    const syncResult = {
      ...ingestResult,
      nextCursor: "cursor_next",
    } satisfies ExportedSyncResult;

    expect(Object.keys(ingestResult).sort()).toEqual([
      "errors",
      "filesDeleted",
      "filesUpdated",
      "filesWritten",
      "paths",
    ]);
    expect(ingestResult).toEqual({
      filesWritten: 2,
      filesUpdated: 1,
      filesDeleted: 0,
      paths: ["/mock-provider/issues/42.json", "/mock-provider/issues/99.json"],
      errors: [{ path: "/mock-provider/issues/99.json", error: "conflict" }],
    });

    expect(Object.keys(syncResult).sort()).toEqual([
      "errors",
      "filesDeleted",
      "filesUpdated",
      "filesWritten",
      "nextCursor",
      "paths",
    ]);
    expect(syncResult).toEqual({
      filesWritten: 2,
      filesUpdated: 1,
      filesDeleted: 0,
      paths: ["/mock-provider/issues/42.json", "/mock-provider/issues/99.json"],
      errors: [{ path: "/mock-provider/issues/99.json", error: "conflict" }],
      nextCursor: "cursor_next",
    });
  });

  it("new exports are available from src/index.ts", () => {
    expect(sdk.IntegrationAdapter).toBeTypeOf("function");
    expect(sdk.validateConnectionProvider).toBeTypeOf("function");
    expect(sdk.validateIntegrationAdapter).toBeTypeOf("function");

    const exportedProvider: ExportedConnectionProvider = createMockProvider();
    const exportedIngestResult: ExportedIngestResult = {
      filesWritten: 0,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [],
      errors: [],
    };
    const exportedSyncResult: ExportedSyncResult = {
      ...exportedIngestResult,
      nextCursor: null,
    };

    expect(exportedProvider.name).toBe("mock-provider");
    expect(exportedIngestResult).toEqual({
      filesWritten: 0,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [],
      errors: [],
    });
    expect(exportedSyncResult).toEqual({
      filesWritten: 0,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [],
      errors: [],
      nextCursor: null,
    });
  });
});
