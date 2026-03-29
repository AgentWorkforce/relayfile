import { describe, expect, it, vi } from "vitest";

import * as sdk from "./index.js";

describe("adapter test helpers", () => {
  it("mock provider records proxy calls", async () => {
    const responseFixture = sdk.testing.createProxyResponseFixture({
      status: 202,
      headers: {
        "content-type": "application/json",
        "x-proxy": "mock",
      },
      data: {
        ok: true,
        source: "proxy",
      },
    });
    const provider = sdk.testing.createMockProvider({
      name: "github",
      proxyResponse: responseFixture,
    });

    const response = await provider.proxy({
      method: "POST",
      baseUrl: "https://api.github.test",
      endpoint: "/repos/acme/issues",
      connectionId: "conn_github_test",
      headers: {
        authorization: "Bearer test-token",
      },
      query: {
        page: "1",
      },
      body: {
        title: "Captured request",
      },
    });

    expect(response).toEqual(responseFixture);
    expect(provider.proxyCalls).toEqual([
      {
        method: "POST",
        baseUrl: "https://api.github.test",
        endpoint: "/repos/acme/issues",
        connectionId: "conn_github_test",
        headers: {
          authorization: "Bearer test-token",
        },
        query: {
          page: "1",
        },
        body: {
          title: "Captured request",
        },
      },
    ]);
    expect(provider.getLastProxyCall()).toEqual(provider.proxyCalls[0]);
    expect(provider.results.proxy).toEqual([responseFixture]);
  });

  it("mock adapter can ingest a normalized webhook", async () => {
    const queuedResponse = {
      status: "queued" as const,
      id: "ing_123",
      correlationId: "corr_123",
    };
    const client = {
      ingestWebhook: vi.fn(async () => queuedResponse),
    };
    const provider = sdk.testing.createMockProvider({
      name: "slack",
    });
    const event = sdk.testing.createNormalizedWebhookFixture({
      provider: "slack",
      connectionId: "conn_slack_test",
      eventType: "message.posted",
      objectType: "messages",
      objectId: "1700000000.100200",
      payload: {
        channel: "C123456",
        ts: "1700000000.100200",
        text: "Hello from Relayfile",
      },
    });
    const adapter = sdk.testing.createMockAdapter(client, provider);

    const result = await adapter.ingestWebhook("ws_acme", event);

    expect(result).toEqual({
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: ["/slack/messages/1700000000.100200.json"],
      errors: [],
    });
    expect(adapter.getLastIngestCall()).toEqual({
      workspaceId: "ws_acme",
      event,
    });
    expect(adapter.computePathCalls).toEqual([
      {
        objectType: "messages",
        objectId: "1700000000.100200",
        event,
      },
    ]);
    expect(adapter.computeSemanticsCalls).toEqual([
      {
        objectType: "messages",
        objectId: "1700000000.100200",
        payload: {
          channel: "C123456",
          ts: "1700000000.100200",
          text: "Hello from Relayfile",
        },
        event,
      },
    ]);
    expect(client.ingestWebhook).toHaveBeenCalledWith({
      workspaceId: "ws_acme",
      provider: "slack",
      event_type: "message.posted",
      path: "/slack/messages/1700000000.100200.json",
      data: {
        channel: "C123456",
        ts: "1700000000.100200",
        text: "Hello from Relayfile",
        semantics: {
          properties: {
            provider: "slack",
            "provider.object_type": "messages",
            "provider.object_id": "1700000000.100200",
            "provider.event_type": "message.posted",
            "provider.connection_id": "conn_slack_test",
          },
        },
      },
      headers: {
        "X-Mock-Connection-Id": "conn_slack_test",
      },
    });
    expect(adapter.getLastWrite()).toEqual({
      kind: "ingestWebhook",
      input: client.ingestWebhook.mock.calls[0]?.[0],
      response: queuedResponse,
    });
  });

  it("fixtures are reusable and correctly shaped", () => {
    const webhookA = sdk.testing.createNormalizedWebhookFixture(
      sdk.testing.defaultNormalizedWebhookFixture,
    );
    const webhookB = sdk.testing.createNormalizedWebhookFixture(
      sdk.testing.defaultNormalizedWebhookFixture,
    );
    const proxyA = sdk.testing.createProxyResponseFixture(
      sdk.testing.defaultProxyResponseFixture,
    );
    const proxyB = sdk.testing.createProxyResponseFixture(
      sdk.testing.defaultProxyResponseFixture,
    );
    const ingestA = sdk.testing.createIngestResultFixture(
      sdk.testing.defaultIngestResultFixture,
    );
    const ingestB = sdk.testing.createIngestResultFixture(
      sdk.testing.defaultIngestResultFixture,
    );

    webhookA.payload.title = "Mutated title";
    proxyA.headers["x-extra"] = "1";
    ingestA.paths.push("/github/issues/99.json");
    ingestA.errors.push({
      path: "/github/issues/99.json",
      error: "conflict",
    });

    expect(webhookB).toMatchObject({
      provider: "github",
      connectionId: "conn_github_test",
      eventType: "issues.opened",
      objectType: "issues",
      objectId: "42",
      payload: {
        id: "42",
        number: 42,
        title: "Fixture issue",
        state: "open",
      },
      relations: ["repo:agent-workforce/relayfile"],
      metadata: {
        installationId: "12345",
        organization: "AgentWorkforce",
      },
    });
    expect(proxyB).toEqual({
      status: 200,
      headers: {
        "content-type": "application/json",
      },
      data: {
        ok: true,
      },
    });
    expect(ingestB).toEqual({
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: ["/github/issues/42.json"],
      errors: [],
    });
    expect(sdk.testing.normalizedWebhookFixtures.slackMessagePosted).toMatchObject({
      provider: "slack",
      connectionId: "conn_slack_test",
      eventType: "message.posted",
      objectType: "messages",
      objectId: "1700000000.100200",
    });
  });

  it("testing exports are available from src/index.ts", () => {
    expect(sdk.testing).toBeDefined();
    expect(sdk.testing.createMockProvider).toBeTypeOf("function");
    expect(sdk.testing.createMockAdapter).toBeTypeOf("function");
    expect(sdk.testing.createNormalizedWebhookFixture).toBeTypeOf("function");
    expect(sdk.testing.defaultNormalizedWebhookFixture).toMatchObject({
      provider: "github",
      objectType: "issues",
    });
    expect(sdk.testing.proxyResponseFixtures.created).toMatchObject({
      status: 201,
    });
  });

  it("overrides let callers customize behavior", async () => {
    const writeResponse = {
      opId: "op_123",
      status: "queued" as const,
      targetRevision: "rev_2",
    };
    const client = {
      writeFile: vi.fn(async () => writeResponse),
    };
    const provider = sdk.testing.createMockProvider({
      name: "github",
      proxy: async (request) => ({
        status: 207,
        headers: {
          "x-provider": request.connectionId,
        },
        data: {
          endpoint: request.endpoint,
        },
      }),
      handleWebhook: async (payload) => ({
        provider: "github",
        connectionId: "conn_custom",
        eventType: "issues.reopened",
        objectType: "issues",
        objectId: String((payload as { id: number }).id),
        payload: payload as Record<string, unknown>,
        metadata: {
          source: "override",
        },
      }),
    });
    const adapter = sdk.testing.createMockAdapter(client, provider, {
      name: "github-custom",
      supportedEvents: ["issues.reopened"],
      baseRevision: "rev_1",
      computePath: (objectType, objectId) => `/custom/${objectType}/${objectId}.md`,
      computeSemantics: (_objectType, objectId, payload, event) => ({
        properties: {
          provider: event?.provider ?? "unknown",
          "provider.object_id": objectId,
          title: String(payload.title ?? ""),
        },
        relations: [`issue:${objectId}`],
      }),
      renderContent: (event) => `# ${String(event.payload.title ?? "Untitled")}\n`,
    });

    const proxyResponse = await provider.proxy({
      method: "GET",
      baseUrl: "https://api.github.test",
      endpoint: "/issues/42",
      connectionId: "conn_custom",
    });
    const event = await provider.handleWebhook?.({
      id: 42,
      title: "Customized issue",
    });
    const result = await adapter.ingestWebhook("ws_custom", event!);

    expect(proxyResponse).toEqual({
      status: 207,
      headers: {
        "x-provider": "conn_custom",
      },
      data: {
        endpoint: "/issues/42",
      },
    });
    expect(adapter.supportedEvents()).toEqual(["issues.reopened"]);
    expect(client.writeFile).toHaveBeenCalledWith({
      workspaceId: "ws_custom",
      path: "/custom/issues/42.md",
      baseRevision: "rev_1",
      content: "# Customized issue\n",
      contentType: "application/json",
      semantics: {
        properties: {
          provider: "github",
          "provider.object_id": "42",
          title: "Customized issue",
        },
        relations: ["issue:42"],
      },
    });
    expect(result).toEqual({
      filesWritten: 0,
      filesUpdated: 1,
      filesDeleted: 0,
      paths: ["/custom/issues/42.md"],
      errors: [],
    });
  });
});
