import { describe, expect, it } from "vitest";

import type {
  EnvelopeQueryOptions,
  EnvelopeRow,
  EventRow,
  FileRow,
  OperationRow,
  Paginated,
  PaginationOptions,
  StorageAdapter,
  WritebackItem,
} from "./storage.js";
import {
  applyWebhookEnvelope,
  ingestWebhook,
  normalizeEnvelopeEvent,
  normalizeEnvelopePath,
} from "./webhooks.js";

describe("webhook Slack path canonicalization", () => {
  it("canonicalizes Slack provider-relative envelope paths", () => {
    const event = normalizeEnvelopeEvent({
      provider: "slack",
      receivedAt: "2026-06-07T20:00:00.000Z",
      payload: {
        provider: "slack",
        event_type: "file.updated",
        path: "/channels/C123/messages/1711111111_000100/meta.json",
      },
    });

    expect(event?.path).toBe(
      "/slack/channels/C123/messages/1711111111_000100/meta.json",
    );
    expect(
      normalizeEnvelopePath({
        provider: "slack",
        payload: {
          provider: "slack",
          path: "/channels/C123/messages/1711111111_000100/meta.json",
        },
      }),
    ).toBe("/slack/channels/C123/messages/1711111111_000100/meta.json");
  });

  it("resolves raw Slack channel IDs to existing channelId__name aliases before writes and events", () => {
    const storage = new MemoryStorage([
      fileRow("/slack/channels/C123__engineering/meta.json", {
        content: '{"id":"C123","name":"engineering"}',
        provider: "slack",
      }),
    ]);

    const result = applyWebhookEnvelope(storage, {
      envelopeId: "env_1",
      workspaceId: "ws_core",
      provider: "slack",
      deliveryId: "delivery_1",
      receivedAt: "2026-06-07T20:00:00.000Z",
      payload: {
        provider: "slack",
        event_type: "file.updated",
        path: "/slack/channels/C123/messages/1711111111_000100/meta.json",
        content: '{"text":"hello"}',
        contentType: "application/json",
      },
      correlationId: "corr_1",
      status: "queued",
      attemptCount: 0,
      lastError: null,
    });

    const canonicalPath =
      "/slack/channels/C123__engineering/messages/1711111111_000100/meta.json";
    expect(result).toMatchObject({
      status: "processed",
      path: canonicalPath,
    });
    expect(storage.getFile(canonicalPath)?.content).toBe('{"text":"hello"}');
    expect(
      storage.getFile("/slack/channels/C123/messages/1711111111_000100/meta.json"),
    ).toBeNull();
    expect(storage.events.at(-1)?.path).toBe(canonicalPath);
    expect(storage.listFilesCalls).toBe(1);
  });

  it("does not scan storage when Slack channel alias resolution is unnecessary", () => {
    const storage = new MemoryStorage([
      fileRow("/slack/channels/C123__engineering/meta.json", {
        content: '{"id":"C123","name":"engineering"}',
        provider: "slack",
      }),
    ]);

    const aliasedResult = applyWebhookEnvelope(storage, {
      envelopeId: "env_aliased",
      workspaceId: "ws_core",
      provider: "slack",
      deliveryId: "delivery_aliased",
      receivedAt: "2026-06-07T20:00:00.000Z",
      payload: {
        provider: "slack",
        event_type: "file.updated",
        path: "/slack/channels/C123__engineering/messages/1711111111_000100/meta.json",
        content: '{"text":"hello"}',
        contentType: "application/json",
      },
      correlationId: "corr_aliased",
      status: "queued",
      attemptCount: 0,
      lastError: null,
    });
    const dmResult = applyWebhookEnvelope(storage, {
      envelopeId: "env_dm",
      workspaceId: "ws_core",
      provider: "slack",
      deliveryId: "delivery_dm",
      receivedAt: "2026-06-07T20:00:01.000Z",
      payload: {
        provider: "slack",
        event_type: "file.updated",
        path: "/slack/dms/D123/messages/1711111111_000100/meta.json",
        content: '{"text":"dm"}',
        contentType: "application/json",
      },
      correlationId: "corr_dm",
      status: "queued",
      attemptCount: 0,
      lastError: null,
    });

    expect(aliasedResult).toMatchObject({
      status: "processed",
      path: "/slack/channels/C123__engineering/messages/1711111111_000100/meta.json",
    });
    expect(dmResult).toMatchObject({
      status: "processed",
      path: "/slack/dms/D123/messages/1711111111_000100/meta.json",
    });
    expect(storage.listFilesCalls).toBe(0);
  });

  it("ignores Slack envelopes targeting paths outside the Slack provider root", () => {
    const storage = new MemoryStorage();

    const result = applyWebhookEnvelope(storage, {
      envelopeId: "env_1",
      workspaceId: "ws_core",
      provider: "slack",
      deliveryId: "delivery_1",
      receivedAt: "2026-06-07T20:00:00.000Z",
      payload: {
        provider: "slack",
        event_type: "file.updated",
        path: "/github/repos/acme/cloud/issues/1.json",
        content: '{"title":"wrong provider"}',
      },
      correlationId: "corr_1",
      status: "queued",
      attemptCount: 0,
      lastError: null,
    });

    expect(result).toEqual({
      status: "ignored",
      eventType: null,
      path: null,
      revision: null,
    });
    expect(storage.listFiles()).toEqual([]);
    expect(storage.events).toEqual([]);
  });

  it("coalesces Slack provider-relative and canonical paths by canonical path", () => {
    const storage = new MemoryStorage();
    let nextEnvelopeId = 1;

    const first = ingestWebhook(storage, {
      provider: "slack",
      eventType: "file.updated",
      path: "/channels/C123/messages/1711111111_000100/meta.json",
      deliveryId: "delivery_1",
      timestamp: "2026-06-07T20:00:00.000Z",
      correlationId: "corr_1",
    }, {
      generateEnvelopeId: () => `env_${nextEnvelopeId++}`,
      coalesceWindowMs: 10_000,
    });
    const second = ingestWebhook(storage, {
      provider: "slack",
      eventType: "file.updated",
      path: "/slack/channels/C123/messages/1711111111_000100/meta.json",
      deliveryId: "delivery_2",
      timestamp: "2026-06-07T20:00:01.000Z",
      correlationId: "corr_2",
    }, {
      generateEnvelopeId: () => `env_${nextEnvelopeId++}`,
      coalesceWindowMs: 10_000,
    });

    expect(first).toMatchObject({ status: "queued", envelopeId: "env_1" });
    expect(second).toMatchObject({ status: "queued", envelopeId: "env_1" });
    expect(Array.from(storage.envelopes)).toHaveLength(1);
    const envelope = storage.envelopes.get("env_1");
    expect(envelope?.deliveryIds).toEqual(["delivery_1", "delivery_2"]);
    expect(envelope?.payload.path).toBe(
      "/slack/channels/C123/messages/1711111111_000100/meta.json",
    );
  });

  it("rejects an envelope write whose path has an oversized segment instead of writing it", () => {
    const storage = new MemoryStorage();
    const oversizedSlug = "a".repeat(250);

    const queued = ingestWebhook(storage, {
      provider: "reddit",
      eventType: "file.created",
      path: `/reddit/subreddits/localllama/posts/${oversizedSlug}__1uvwn9q.json`,
      deliveryId: "delivery_1",
      timestamp: "2026-07-15T09:45:00.000Z",
      correlationId: "corr_1",
    }, {
      generateEnvelopeId: () => "env_1",
      coalesceWindowMs: 10_000,
    });
    expect(queued.status).toBe("queued");

    const envelope = storage.envelopes.get("env_1");
    if (!envelope) throw new Error("envelope not queued");

    const result = applyWebhookEnvelope(storage, envelope);

    expect(result.status).toBe("rejected");
    expect(result.reason).toBe("path_too_long");
    expect(storage.files.size).toBe(0);
  });
});

function fileRow(path: string, overrides: Partial<FileRow> = {}): FileRow {
  return {
    path,
    revision: "rev_1",
    contentType: "application/json",
    content: "{}",
    encoding: "utf-8",
    provider: "",
    lastEditedAt: "2026-06-07T20:00:00.000Z",
    semantics: {},
    ...overrides,
  };
}

class MemoryStorage implements StorageAdapter {
  files = new Map<string, FileRow>();
  events: EventRow[] = [];
  envelopes = new Map<string, EnvelopeRow>();
  deliveryAliases = new Map<string, string>();
  revisionCounter = 1;
  eventCounter = 1;
  listFilesCalls = 0;

  constructor(files: FileRow[] = []) {
    for (const file of files) {
      this.files.set(file.path, file);
    }
  }

  getFile(path: string): FileRow | null {
    return this.files.get(path) ?? null;
  }

  listFiles(): FileRow[] {
    this.listFilesCalls += 1;
    return Array.from(this.files.values());
  }

  putFile(file: FileRow): void {
    this.files.set(file.path, file);
  }

  deleteFile(path: string): void {
    this.files.delete(path);
  }

  appendEvent(event: EventRow): void {
    this.events.push(event);
  }

  listEvents(_options: PaginationOptions & { provider?: string }): Paginated<EventRow> {
    return { items: this.events, nextCursor: null };
  }

  getRecentEvents(limit: number): EventRow[] {
    return this.events.slice(-limit);
  }

  getOperation(_opId: string): OperationRow | null {
    return null;
  }

  putOperation(_op: OperationRow): void {
    return undefined;
  }

  listOperations(_options: PaginationOptions): Paginated<OperationRow> {
    return { items: [], nextCursor: null };
  }

  nextRevision(): string {
    return `rev_${this.revisionCounter++}`;
  }

  nextOperationId(): string {
    return "op_1";
  }

  nextEventId(): string {
    return `evt_${this.eventCounter++}`;
  }

  enqueueWriteback(_item: WritebackItem): void {
    return undefined;
  }

  getPendingWritebacks(): WritebackItem[] {
    return [];
  }

  getEnvelopeByDelivery(
    workspaceId: string,
    provider: string,
    deliveryId: string,
  ): EnvelopeRow | null {
    const envelopeId = this.deliveryAliases.get(
      `${workspaceId}|${provider}|${deliveryId}`,
    );
    return envelopeId ? this.envelopes.get(envelopeId) ?? null : null;
  }

  putEnvelope(envelope: EnvelopeRow): void {
    this.envelopes.set(envelope.envelopeId, envelope);
  }

  putEnvelopeDeliveryAlias(
    workspaceId: string,
    provider: string,
    deliveryId: string,
    envelopeId: string,
  ): void {
    this.deliveryAliases.set(`${workspaceId}|${provider}|${deliveryId}`, envelopeId);
  }

  listEnvelopes(options: EnvelopeQueryOptions): Paginated<EnvelopeRow> {
    return {
      items: Array.from(this.envelopes.values()).filter(
        (envelope) =>
          (!options.workspaceId || envelope.workspaceId === options.workspaceId) &&
          (!options.provider || envelope.provider === options.provider) &&
          (!options.status || envelope.status === options.status),
      ),
      nextCursor: null,
    };
  }

  getWorkspaceId(): string {
    return "ws_core";
  }
}
