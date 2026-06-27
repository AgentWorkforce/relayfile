import { describe, expect, it, vi } from "vitest";
import { createInboundForwardHandler, handleWritebackDelivery, matchBinding, type RelayBindingRecord } from "./forward.js";
import { IntegrationAdapter, type AdapterWebhook, type IngestResult } from "@relayfile/sdk";
import type { ConnectionProvider, FileSemantics, RelayFileClient } from "@relayfile/sdk";

class TestAdapter extends IntegrationAdapter {
  readonly name = "linear";
  readonly version = "1.0.0";
  writeBack = vi.fn(async () => ({ ok: true }));

  constructor() {
    super({} as RelayFileClient, {} as ConnectionProvider);
  }

  async ingestWebhook(_workspaceId: string, _event: AdapterWebhook): Promise<IngestResult> {
    return { filesWritten: 0, filesUpdated: 0, filesDeleted: 0, paths: [], errors: [] };
  }

  computePath(objectType: string, objectId: string): string {
    return `/linear/${objectType}/${objectId}.json`;
  }

  computeSemantics(_objectType: string, _objectId: string, payload: Record<string, unknown>): FileSemantics {
    return { properties: { title: String(payload.title ?? "Issue") } };
  }
}

const bindings: RelayBindingRecord[] = [{
  provider: "linear",
  pathGlob: "/linear/issues/**",
  channel: "#issues",
  webhookId: "wh_1",
  webhookToken: "tok_1",
}];

describe("relayfile agents forwarder", () => {
  it("matches trailing recursive binding globs", () => {
    expect(matchBinding("/linear/issues/ENG-1.json", bindings)?.webhookId).toBe("wh_1");
    expect(matchBinding("/github/issues/1.json", bindings)).toBeUndefined();
  });

  it("forwards inbound writes to RelayCast with relayfile metadata", async () => {
    const trigger = vi.fn(async () => undefined);
    const handler = createInboundForwardHandler({
      adapter: new TestAdapter(),
      bindings,
      relayCastSdk: { webhooks: { trigger } },
    });

    await handler({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "update",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "sync",
      value: { title: "Issue" },
    });

    expect(trigger).toHaveBeenCalledWith("wh_1", expect.objectContaining({
      source: "linear",
      payload: { relayfile: { provider: "linear", path: "/linear/issues/ENG-1.json", revision: "rev_1" } },
    }), "tok_1");
  });

  it("skips delete operations and adapter skip views", async () => {
    const trigger = vi.fn(async () => undefined);
    const adapter = new TestAdapter();
    adapter.relayBinding = { present: () => ({ text: "", skip: true }) };
    const handler = createInboundForwardHandler({
      adapter,
      bindings,
      relayCastSdk: { webhooks: { trigger } },
    });

    await handler({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "delete",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "sync",
    });
    await handler({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "update",
      revision: "rev_2",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:01.000Z",
      source: "sync",
    });

    expect(trigger).not.toHaveBeenCalled();
  });

  it("skips writes produced by relayfile writeback actors", async () => {
    const trigger = vi.fn(async () => undefined);
    const handler = createInboundForwardHandler({
      adapter: new TestAdapter(),
      bindings,
      relayCastSdk: { webhooks: { trigger } },
    });

    await handler({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "update",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "agent",
      actor: { type: "system", id: "__relayfile_writeback__" },
      value: { title: "Echo" },
    });

    expect(trigger).not.toHaveBeenCalled();
  });

  it("skips inbound writes when the adapter presenter returns null", async () => {
    const trigger = vi.fn(async () => undefined);
    const adapter = new TestAdapter();
    adapter.relayBinding = { present: () => null };
    const handler = createInboundForwardHandler({
      adapter,
      bindings,
      relayCastSdk: { webhooks: { trigger } },
    });

    await handler({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "update",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "sync",
      value: { title: "Hidden" },
    });

    expect(trigger).not.toHaveBeenCalled();
  });

  it("writes outbound relay replies through the adapter", async () => {
    const adapter = new TestAdapter();
    adapter.relayBinding = { replyPathFor: (sourcePath) => `${sourcePath}/comments/draft.json` };

    await handleWritebackDelivery({
      message: {
        text: "Looks good",
        metadata: {
          relayfile: { provider: "linear", path: "/linear/issues/ENG-1.json", revision: "rev_1" },
        },
      },
    }, adapter, "ws_1", bindings);

    expect(adapter.writeBack).toHaveBeenCalledWith("ws_1", "/linear/issues/ENG-1.json/comments/draft.json", "Looks good");
  });

  it("skips outbound messages injected by inbound webhooks", async () => {
    const adapter = new TestAdapter();
    await handleWritebackDelivery({
      message: {
        text: "echo",
        metadata: {
          __relaycast_origin: "inbound_webhook",
          relayfile: { provider: "linear", path: "/linear/issues/ENG-1.json", revision: "rev_1" },
        },
      },
    }, adapter, "ws_1", bindings);

    expect(adapter.writeBack).not.toHaveBeenCalled();
  });

  it("skips outbound messages without relayfile metadata", async () => {
    const adapter = new TestAdapter();

    await handleWritebackDelivery({
      message: {
        text: "no target",
        metadata: {},
      },
    }, adapter, "ws_1", bindings);

    expect(adapter.writeBack).not.toHaveBeenCalled();
  });

  it("skips outbound writeback when the adapter returns no reply path", async () => {
    const adapter = new TestAdapter();
    adapter.relayBinding = { replyPathFor: () => null };

    await handleWritebackDelivery({
      message: {
        text: "read-only",
        metadata: {
          relayfile: { provider: "linear", path: "/linear/issues/ENG-1.json", revision: "rev_1" },
        },
      },
    }, adapter, "ws_1", bindings);

    expect(adapter.writeBack).not.toHaveBeenCalled();
  });
});
