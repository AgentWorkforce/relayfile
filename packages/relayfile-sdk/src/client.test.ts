import { describe, it, expect, vi, beforeEach } from "vitest";
import { RelayFileClient } from "./client.js";
import {
  RelayFileApiError,
  RevisionConflictError,
  QueueFullError,
  InvalidStateError,
  PayloadTooLargeError,
} from "./errors.js";
import type {
  BulkWriteResponse,
  TreeResponse,
  FileReadResponse,
  WriteQueuedResponse,
  FileQueryResponse,
  EventFeedResponse,
  ExportJsonResponse,
  FilesystemEvent,
  OperationStatusResponse,
  OperationFeedResponse,
  QueuedResponse,
  AckResponse,
  BackendStatusResponse,
  SyncStatusResponse,
  SyncIngressStatusResponse,
  DeadLetterFeedResponse,
  DeadLetterItem,
  AdminIngressStatusResponse,
  AdminSyncStatusResponse,
  IngestWebhookInput,
  WritebackItem,
  AckWritebackInput,
  AckWritebackResponse,
} from "./types.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function mockFetch(body: unknown, status = 200, headers: Record<string, string> = {}) {
  const headersObj = new Headers({ "content-type": "application/json", ...headers });
  return vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    headers: headersObj,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as unknown as Response);
}

function makeClient(fetchImpl: typeof fetch, opts?: { retry?: { maxRetries: number } }) {
  return new RelayFileClient({
    baseUrl: "https://relay.test",
    token: "tok_test",
    fetchImpl,
    retry: opts?.retry ?? { maxRetries: 0 },
  });
}

// ---------------------------------------------------------------------------
// Existing methods
// ---------------------------------------------------------------------------

describe("RelayFileClient — existing methods", () => {
  // ---- listTree ----
  describe("listTree", () => {
    it("returns tree entries for a workspace", async () => {
      const payload: TreeResponse = {
        path: "/",
        entries: [
          { path: "/zendesk", type: "dir", revision: "rev_1" },
          { path: "/readme.md", type: "file", revision: "rev_2" },
        ],
        nextCursor: null,
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.listTree("ws_acme");
      expect(res.entries).toHaveLength(2);
      expect(res.entries[0]!.path).toBe("/zendesk");
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("/v1/workspaces/ws_acme/fs/tree");
    });

    it("passes depth and cursor as query params", async () => {
      const f = mockFetch({ path: "/", entries: [], nextCursor: null });
      const client = makeClient(f);
      await client.listTree("ws_acme", { path: "/zendesk", depth: 2, cursor: "abc" });
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("depth=2");
      expect(url).toContain("cursor=abc");
      expect(url).toContain("path=%2Fzendesk");
    });
  });

  // ---- readFile ----
  describe("readFile", () => {
    it("reads a file by path", async () => {
      const payload: FileReadResponse = {
        path: "/zendesk/tickets/48291.json",
        revision: "rev_3",
        contentType: "application/json",
        content: '{"id":48291}',
        provider: "zendesk",
        semantics: { properties: { "provider.object_type": "ticket" } },
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.readFile("ws_acme", "/zendesk/tickets/48291.json");
      expect(res.content).toBe('{"id":48291}');
      expect(res.revision).toBe("rev_3");
    });
  });

  // ---- writeFile ----
  describe("writeFile", () => {
    it("sends PUT with If-Match and body", async () => {
      const payload: WriteQueuedResponse = {
        opId: "op_1",
        status: "queued",
        targetRevision: "rev_4",
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.writeFile({
        workspaceId: "ws_acme",
        path: "/zendesk/tickets/48291.json",
        baseRevision: "rev_3",
        content: '{"status":"solved"}',
        contentType: "application/json",
      });
      expect(res.opId).toBe("op_1");
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(init.method).toBe("PUT");
      expect((init.headers as Record<string, string>)["If-Match"]).toBe("rev_3");
    });
  });

  // ---- deleteFile ----
  describe("deleteFile", () => {
    it("sends DELETE with If-Match", async () => {
      const payload: WriteQueuedResponse = {
        opId: "op_2",
        status: "queued",
        targetRevision: "rev_5",
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.deleteFile({
        workspaceId: "ws_acme",
        path: "/old/file.json",
        baseRevision: "rev_4",
      });
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(init.method).toBe("DELETE");
    });
  });

  // ---- queryFiles ----
  describe("queryFiles", () => {
    it("passes provider and property filters", async () => {
      const payload: FileQueryResponse = { items: [], nextCursor: null };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.queryFiles("ws_acme", {
        provider: "zendesk",
        properties: { "provider.status": "open" },
      });
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("provider=zendesk");
      expect(url).toContain("property.provider.status=open");
    });
  });

  // ---- getEvents ----
  describe("getEvents", () => {
    it("fetches events with provider filter", async () => {
      const payload: EventFeedResponse = { events: [], nextCursor: null };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.getEvents("ws_acme", { provider: "github", limit: 10 });
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("provider=github");
      expect(url).toContain("limit=10");
    });
  });

  // ---- bulkWrite ----
  describe("bulkWrite", () => {
    it("maps the bulk write response to imported/errors", async () => {
      const f = mockFetch({
        written: 2,
        errorCount: 1,
        errors: [
          {
            path: "/restricted.md",
            code: "forbidden",
            message: "file access denied by permission policy",
          },
        ],
        correlationId: "corr_1",
      });
      const client = makeClient(f);

      const res: BulkWriteResponse = await client.bulkWrite({
        workspaceId: "ws_acme",
        files: [
          { path: "/a.md", content: "a" },
          { path: "/b.md", content: "b", encoding: "utf-8" },
        ],
      });

      expect(res).toEqual({
        imported: 2,
        errors: [
          {
            path: "/restricted.md",
            error: "file access denied by permission policy",
          },
        ],
      });

      const init = f.mock.calls[0]![1] as RequestInit;
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({
        files: [
          { path: "/a.md", content: "a" },
          { path: "/b.md", content: "b", encoding: "utf-8" },
        ],
      });
    });
  });

  // ---- exportWorkspace ----
  describe("exportWorkspace", () => {
    it("wraps JSON export arrays in a files object", async () => {
      const f = mockFetch([
        {
          path: "/docs/readme.md",
          revision: "rev_1",
          contentType: "text/markdown",
          content: "# Hello",
          encoding: "utf-8",
        },
      ] satisfies FileReadResponse[]);
      const client = makeClient(f);

      const res = await client.exportWorkspace({
        workspaceId: "ws_acme",
        format: "json",
      }) as ExportJsonResponse;

      expect(res.files).toHaveLength(1);
      expect(res.files[0]!.path).toBe("/docs/readme.md");
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("/v1/workspaces/ws_acme/fs/export?format=json");
    });

    it("returns a Blob for non-JSON exports", async () => {
      const f = vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "application/gzip" }),
        blob: () => Promise.resolve(new Blob(["archive"], { type: "application/gzip" })),
        arrayBuffer: () => Promise.resolve(new TextEncoder().encode("archive").buffer),
        text: () => Promise.resolve("archive"),
      } as unknown as Response);
      const client = makeClient(f);

      const res = await client.exportWorkspace({
        workspaceId: "ws_acme",
        format: "tar",
      });

      expect(res).toBeInstanceOf(Blob);
    });
  });

  // ---- connectWebSocket ----
  describe("connectWebSocket", () => {
    class MockWebSocket {
      static instances: MockWebSocket[] = [];

      readonly url: string;
      private readonly listeners = new Map<string, Set<(event: any) => void>>();

      constructor(url: string) {
        this.url = url;
        MockWebSocket.instances.push(this);
      }

      addEventListener(type: string, handler: (event: any) => void): void {
        if (!this.listeners.has(type)) {
          this.listeners.set(type, new Set());
        }
        this.listeners.get(type)!.add(handler);
      }

      close = vi.fn();

      emit(type: string, event: any): void {
        for (const handler of this.listeners.get(type) ?? []) {
          handler(event);
        }
      }
    }

    beforeEach(() => {
      MockWebSocket.instances = [];
      Object.defineProperty(globalThis, "WebSocket", {
        configurable: true,
        writable: true,
        value: MockWebSocket as unknown as typeof WebSocket,
      });
    });

    it("connects using the workspace WebSocket endpoint and token query param", () => {
      const client = makeClient(mockFetch({ path: "/", entries: [], nextCursor: null }));

      client.connectWebSocket("ws_acme", { token: "ws_token" });

      expect(MockWebSocket.instances).toHaveLength(1);
      expect(MockWebSocket.instances[0]!.url).toBe("wss://relay.test/v1/workspaces/ws_acme/fs/ws?token=ws_token");
    });

    it("emits parsed filesystem events to the event handler", () => {
      const client = makeClient(mockFetch({ path: "/", entries: [], nextCursor: null }));
      const onEvent = vi.fn();

      client.connectWebSocket("ws_acme", { token: "ws_token", onEvent });
      const socket = MockWebSocket.instances[0]!;

      socket.emit("message", {
        data: JSON.stringify({
          eventId: "evt_1",
          type: "file.created",
          path: "/docs/readme.md",
          revision: "rev_1",
          timestamp: "2026-03-25T00:00:00Z",
        } satisfies FilesystemEvent),
      });

      expect(onEvent).toHaveBeenCalledWith({
        eventId: "evt_1",
        type: "file.created",
        path: "/docs/readme.md",
        revision: "rev_1",
        timestamp: "2026-03-25T00:00:00Z",
      });
    });
  });

  // ---- getOp / listOps / replayOp ----
  describe("operations", () => {
    it("getOp fetches single operation", async () => {
      const payload: OperationStatusResponse = {
        opId: "op_1",
        status: "succeeded",
        attemptCount: 1,
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.getOp("ws_acme", "op_1");
      expect(res.status).toBe("succeeded");
    });

    it("listOps filters by status", async () => {
      const payload: OperationFeedResponse = { items: [], nextCursor: null };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.listOps("ws_acme", { status: "failed" });
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("status=failed");
    });

    it("replayOp posts to replay endpoint", async () => {
      const payload: QueuedResponse = { status: "queued", id: "op_1" };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.replayOp("ws_acme", "op_1");
      expect(res.status).toBe("queued");
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(init.method).toBe("POST");
    });
  });

  // ---- sync endpoints ----
  describe("sync", () => {
    it("getSyncStatus returns provider statuses", async () => {
      const payload: SyncStatusResponse = {
        workspaceId: "ws_acme",
        providers: [{ provider: "zendesk", status: "healthy" }],
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.getSyncStatus("ws_acme");
      expect(res.providers).toHaveLength(1);
    });

    it("triggerSyncRefresh sends provider and reason", async () => {
      const payload: QueuedResponse = { status: "queued", id: "ref_1" };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.triggerSyncRefresh("ws_acme", "zendesk", "manual");
      const body = JSON.parse(
        (f.mock.calls[0]![1] as RequestInit).body as string
      );
      expect(body.provider).toBe("zendesk");
      expect(body.reason).toBe("manual");
    });
  });

  // ---- admin endpoints ----
  describe("admin", () => {
    it("getBackendStatus returns backend info", async () => {
      const payload: BackendStatusResponse = {
        backendProfile: "memory",
        stateBackend: "memory://",
        envelopeQueue: "memory://",
        envelopeQueueDepth: 0,
        envelopeQueueCapacity: 1000,
        writebackQueue: "memory://",
        writebackQueueDepth: 0,
        writebackQueueCapacity: 100,
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.getBackendStatus();
      expect(res.backendProfile).toBe("memory");
    });

    it("replayAdminEnvelope posts to admin replay", async () => {
      const payload: QueuedResponse = { status: "queued", id: "env_1" };
      const f = mockFetch(payload);
      const client = makeClient(f);
      const res = await client.replayAdminEnvelope("env_1");
      expect(res.status).toBe("queued");
      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("/v1/admin/replay/envelope/env_1");
    });
  });
});

// ---------------------------------------------------------------------------
// Auth & headers
// ---------------------------------------------------------------------------

describe("RelayFileClient — auth & headers", () => {
  it("sends Bearer token from string", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = makeClient(f);
    await client.listTree("ws_1");
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["Authorization"]).toBe("Bearer tok_test");
  });

  it("resolves token from async function", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = new RelayFileClient({
      baseUrl: "https://relay.test",
      token: async () => "dynamic_tok",
      fetchImpl: f,
      retry: { maxRetries: 0 },
    });
    await client.listTree("ws_1");
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["Authorization"]).toBe("Bearer dynamic_tok");
  });

  it("sets X-Correlation-Id header", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = makeClient(f);
    await client.listTree("ws_1", { correlationId: "corr_custom" });
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["X-Correlation-Id"]).toBe("corr_custom");
  });

  it("auto-generates correlation id when not provided", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = makeClient(f);
    await client.listTree("ws_1");
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["X-Correlation-Id"]).toMatch(/^rf_/);
  });

  it("sends custom User-Agent when configured", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = new RelayFileClient({
      baseUrl: "https://relay.test",
      token: "tok",
      fetchImpl: f,
      userAgent: "my-agent/1.0",
      retry: { maxRetries: 0 },
    });
    await client.listTree("ws_1");
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["User-Agent"]).toBe("my-agent/1.0");
  });
});

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

describe("RelayFileClient — error handling", () => {
  it("throws RevisionConflictError on 409 with revision fields", async () => {
    const body = {
      code: "revision_conflict",
      message: "Conflict",
      correlationId: "c1",
      expectedRevision: "rev_old",
      currentRevision: "rev_new",
      currentContentPreview: "preview...",
    };
    const f = mockFetch(body, 409);
    const client = makeClient(f);
    await expect(
      client.writeFile({
        workspaceId: "ws_1",
        path: "/f.json",
        baseRevision: "rev_old",
        content: "{}",
      })
    ).rejects.toThrow(RevisionConflictError);
  });

  it("throws InvalidStateError on 409 with invalid_state code", async () => {
    const body = { code: "invalid_state", message: "Bad state", correlationId: "c2" };
    const f = mockFetch(body, 409);
    const client = makeClient(f);
    await expect(client.replayOp("ws_1", "op_1")).rejects.toThrow(InvalidStateError);
  });

  it("throws QueueFullError on 429 with queue_full code", async () => {
    const body = { code: "queue_full", message: "Full", correlationId: "c3" };
    const f = mockFetch(body, 429, { "retry-after": "5" });
    const client = makeClient(f);
    try {
      await client.writeFile({
        workspaceId: "ws_1",
        path: "/f.json",
        baseRevision: "rev_1",
        content: "{}",
      });
      expect.unreachable("Should have thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(QueueFullError);
      expect((err as QueueFullError).retryAfterSeconds).toBe(5);
    }
  });

  it("throws PayloadTooLargeError on 413", async () => {
    const body = { code: "payload_too_large", message: "Too big" };
    const f = mockFetch(body, 413);
    const client = makeClient(f);
    await expect(
      client.writeFile({
        workspaceId: "ws_1",
        path: "/f.json",
        baseRevision: "rev_1",
        content: "x".repeat(10000),
      })
    ).rejects.toThrow(PayloadTooLargeError);
  });

  it("throws generic RelayFileApiError on other errors", async () => {
    const body = { code: "not_found", message: "Not found" };
    const f = mockFetch(body, 404);
    const client = makeClient(f);
    await expect(client.readFile("ws_1", "/nope.json")).rejects.toThrow(RelayFileApiError);
  });
});

// ---------------------------------------------------------------------------
// Retry behaviour
// ---------------------------------------------------------------------------

describe("RelayFileClient — retry", () => {
  it("retries on 500 and eventually succeeds", async () => {
    let calls = 0;
    const f = vi.fn().mockImplementation(() => {
      calls++;
      if (calls < 3) {
        return Promise.resolve({
          ok: false,
          status: 500,
          headers: new Headers({ "content-type": "application/json" }),
          json: () => Promise.resolve({ code: "server_error", message: "fail" }),
          text: () => Promise.resolve("fail"),
        });
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "application/json" }),
        json: () => Promise.resolve({ path: "/", entries: [], nextCursor: null }),
        text: () => Promise.resolve("{}"),
      });
    }) as unknown as typeof fetch;

    const client = new RelayFileClient({
      baseUrl: "https://relay.test",
      token: "tok",
      fetchImpl: f,
      retry: { maxRetries: 3, baseDelayMs: 1, maxDelayMs: 5 },
    });

    const res = await client.listTree("ws_1");
    expect(res.entries).toEqual([]);
    expect(calls).toBe(3);
  });

  it("retries on network error", async () => {
    let calls = 0;
    const f = vi.fn().mockImplementation(() => {
      calls++;
      if (calls < 2) {
        return Promise.reject(new Error("ECONNREFUSED"));
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "application/json" }),
        json: () => Promise.resolve({ path: "/", entries: [], nextCursor: null }),
        text: () => Promise.resolve("{}"),
      });
    }) as unknown as typeof fetch;

    const client = new RelayFileClient({
      baseUrl: "https://relay.test",
      token: "tok",
      fetchImpl: f,
      retry: { maxRetries: 2, baseDelayMs: 1, maxDelayMs: 5 },
    });

    const res = await client.listTree("ws_1");
    expect(res.entries).toEqual([]);
    expect(calls).toBe(2);
  });

  it("does not retry on abort", async () => {
    const controller = new AbortController();
    controller.abort();
    const f = vi.fn().mockRejectedValue((() => {
      const e = new Error("aborted");
      e.name = "AbortError";
      return e;
    })()) as unknown as typeof fetch;

    const client = new RelayFileClient({
      baseUrl: "https://relay.test",
      token: "tok",
      fetchImpl: f,
      retry: { maxRetries: 3, baseDelayMs: 1 },
    });

    await expect(client.listTree("ws_1", { signal: controller.signal })).rejects.toThrow("aborted");
    expect(f).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// New methods: ingestWebhook, listPendingWritebacks, ackWriteback
// ---------------------------------------------------------------------------

describe("RelayFileClient — new webhook/writeback methods", () => {
  // These tests validate the 3 missing SDK methods once implemented.
  // They test the expected API surface from sdk-improvements.md.

  it("ingestWebhook sends POST to webhooks/ingest", async () => {
    const payload: QueuedResponse = { status: "queued", id: "env_abc" };
    const f = mockFetch(payload);
    const client = makeClient(f);

    const res = await client.ingestWebhook({
      workspaceId: "ws_acme",
      provider: "zendesk",
      event_type: "file.updated",
      path: "/zendesk/tickets/48291.json",
      data: { content: '{"id":48291}' },
      delivery_id: "nango_evt_abc123",
    });
    expect(res.status).toBe("queued");
    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("/v1/workspaces/ws_acme/webhooks/ingest");
    const init = f.mock.calls[0]![1] as RequestInit;
    expect(init.method).toBe("POST");
    const body = JSON.parse(init.body as string);
    expect(body.provider).toBe("zendesk");
    expect(body.event_type).toBe("file.updated");
    expect(body.path).toBe("/zendesk/tickets/48291.json");
  });

  it("ingestWebhook sends optional headers and timestamp", async () => {
    const payload: QueuedResponse = { status: "queued", id: "env_def" };
    const f = mockFetch(payload);
    const client = makeClient(f);

    await client.ingestWebhook({
      workspaceId: "ws_acme",
      provider: "github",
      event_type: "file.created",
      path: "/github/repos/acme/api/issues/42.json",
      data: { number: 42 },
      timestamp: "2026-03-14T12:00:00Z",
      headers: { "X-GitHub-Event": "issues" },
    });
    const body = JSON.parse(
      (f.mock.calls[0]![1] as RequestInit).body as string
    );
    expect(body.timestamp).toBe("2026-03-14T12:00:00Z");
    expect(body.headers["X-GitHub-Event"]).toBe("issues");
  });

  it("listPendingWritebacks GETs writeback/pending", async () => {
    const payload: WritebackItem[] = [
      {
        id: "wb_1",
        workspaceId: "ws_acme",
        path: "/zendesk/tickets/48291.json",
        revision: "rev_5",
        correlationId: "corr_1",
      },
    ];
    const f = mockFetch(payload);
    const client = makeClient(f);

    const res = await client.listPendingWritebacks("ws_acme");
    expect(res).toHaveLength(1);
    expect(res[0]!.path).toBe("/zendesk/tickets/48291.json");
    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("/v1/workspaces/ws_acme/writeback/pending");
    const init = f.mock.calls[0]![1] as RequestInit;
    expect(init.method).toBe("GET");
  });

  it("listPendingWritebacks passes correlationId", async () => {
    const f = mockFetch([]);
    const client = makeClient(f);
    await client.listPendingWritebacks("ws_acme", "corr_custom");
    const headers = (f.mock.calls[0]![1] as RequestInit).headers as Record<string, string>;
    expect(headers["X-Correlation-Id"]).toBe("corr_custom");
  });

  it("ackWriteback POSTs to writeback/{itemId}/ack", async () => {
    const payload: AckWritebackResponse = {
      status: "acknowledged",
      id: "wb_1",
      success: true,
    };
    const f = mockFetch(payload);
    const client = makeClient(f);

    const res = await client.ackWriteback({
      workspaceId: "ws_acme",
      itemId: "wb_1",
      success: true,
    });
    expect(res.status).toBe("acknowledged");
    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("/v1/workspaces/ws_acme/writeback/wb_1/ack");
    const init = f.mock.calls[0]![1] as RequestInit;
    expect(init.method).toBe("POST");
  });

  it("ackWriteback with failure sends error message", async () => {
    const payload: AckWritebackResponse = {
      status: "acknowledged",
      id: "wb_2",
      success: false,
    };
    const f = mockFetch(payload);
    const client = makeClient(f);

    await client.ackWriteback({
      workspaceId: "ws_acme",
      itemId: "wb_2",
      success: false,
      error: "Provider returned 403",
    });
    const body = JSON.parse(
      (f.mock.calls[0]![1] as RequestInit).body as string
    );
    expect(body.success).toBe(false);
    expect(body.error).toBe("Provider returned 403");
  });
});

// ---------------------------------------------------------------------------
// NangoHelpers
// ---------------------------------------------------------------------------

describe("NangoHelpers", () => {
  it("ingestNangoWebhook computes canonical path and properties", async () => {
    const f = mockFetch({ status: "queued", id: "env_1" } satisfies QueuedResponse);
    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    await nango.ingestWebhook("ws_acme", {
      connectionId: "conn_zendesk_acme",
      integrationId: "zendesk-support",
      providerConfigKey: "zendesk",
      model: "tickets",
      objectId: "48291",
      eventType: "updated",
      payload: { id: 48291, status: "open" },
    });

    const body = JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string);
    expect(body.path).toBe("/zendesk/tickets/48291.json");
    expect(body.provider).toBe("zendesk");
    expect(body.event_type).toBe("updated");
    expect(body.headers["X-Nango-Connection-Id"]).toBe("conn_zendesk_acme");
    expect(body.headers["X-Nango-Integration-Id"]).toBe("zendesk-support");
    expect(body.headers["X-Nango-Provider-Config-Key"]).toBe("zendesk");
    // Verify semantic properties are embedded in the data
    expect(body.data.semantics.properties["nango.connection_id"]).toBe("conn_zendesk_acme");
    expect(body.data.semantics.properties["provider.object_type"]).toBe("tickets");
    expect(body.data.semantics.properties["provider.status"]).toBe("open");
  });

  it("ingestNangoWebhook uses fallback path for unknown provider", async () => {
    const f = mockFetch({ status: "queued", id: "env_2" } satisfies QueuedResponse);
    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    await nango.ingestWebhook("ws_acme", {
      connectionId: "conn_1",
      integrationId: "notion-sync",
      model: "pages",
      objectId: "page_1",
      eventType: "created",
      payload: { title: "My Page" },
    });

    const body = JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string);
    // "notion" extracted from "notion-sync" integrationId
    expect(body.path).toBe("/notion/pages/page_1.json");
    expect(body.provider).toBe("notion");
    // providerConfigKey not set — header should be absent
    expect(body.headers["X-Nango-Provider-Config-Key"]).toBeUndefined();
  });

  it("ingestNangoWebhook passes relations to semantics", async () => {
    const f = mockFetch({ status: "queued", id: "env_3" } satisfies QueuedResponse);
    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    await nango.ingestWebhook("ws_acme", {
      connectionId: "conn_1",
      integrationId: "zendesk-support",
      model: "tickets",
      objectId: "100",
      eventType: "updated",
      payload: { id: 100 },
      relations: ["/zendesk/users/42.json"],
    });

    const body = JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string);
    expect(body.data.semantics.relations).toEqual(["/zendesk/users/42.json"]);
  });

  it("getProviderFiles queries with provider and path prefix", async () => {
    const queryPayload: FileQueryResponse = {
      items: [
        { path: "/zendesk/tickets/1.json", revision: "rev_1", contentType: "application/json", size: 100 },
      ],
      nextCursor: null,
    };
    const f = mockFetch(queryPayload);
    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    const files = await nango.getProviderFiles("ws_acme", {
      provider: "zendesk",
      objectType: "tickets",
      status: "open",
    });

    expect(files).toHaveLength(1);
    expect(files[0]!.path).toBe("/zendesk/tickets/1.json");
    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("provider=zendesk");
    expect(url).toContain("path=%2Fzendesk%2Ftickets%2F");
    expect(url).toContain("property.provider.status=open");
    expect(url).toContain("property.provider.object_type=tickets");
  });

  it("getProviderFiles without objectType queries full provider prefix", async () => {
    const f = mockFetch({ items: [], nextCursor: null } satisfies FileQueryResponse);
    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    await nango.getProviderFiles("ws_acme", { provider: "github" });

    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("path=%2Fgithub%2F");
  });

  it("watchProviderEvents yields events and respects abort", async () => {
    let callCount = 0;
    const f = vi.fn().mockImplementation(() => {
      callCount++;
      const events = callCount === 1
        ? [{ eventId: "e1", type: "file.created", path: "/github/issues/1.json", revision: "r1", origin: "provider_sync", provider: "github", correlationId: "c1", timestamp: "2026-03-14T00:00:00Z" }]
        : [];
      return Promise.resolve({
        ok: true, status: 200,
        headers: new Headers({ "content-type": "application/json" }),
        json: () => Promise.resolve({ events, nextCursor: callCount === 1 ? "cur_1" : null }),
        text: () => Promise.resolve("{}"),
      });
    }) as unknown as typeof fetch;

    const client = makeClient(f);
    const { NangoHelpers } = await import("./nango.js");
    const nango = new NangoHelpers(client);

    const controller = new AbortController();
    const collected: unknown[] = [];
    for await (const event of nango.watchProviderEvents("ws_acme", {
      provider: "github",
      pollIntervalMs: 10,
      signal: controller.signal,
    })) {
      collected.push(event);
      controller.abort();
    }

    expect(collected).toHaveLength(1);
    expect((collected[0] as any).eventId).toBe("e1");
  });
});

// ---------------------------------------------------------------------------
// URL encoding & edge cases
// ---------------------------------------------------------------------------

describe("RelayFileClient — edge cases", () => {
  it("encodes workspace ID with special characters", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = makeClient(f);
    await client.listTree("ws/special id");
    const url = f.mock.calls[0]![0] as string;
    expect(url).toContain("ws%2Fspecial%20id");
  });

  it("strips trailing slashes from baseUrl", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = new RelayFileClient({
      baseUrl: "https://relay.test///",
      token: "tok",
      fetchImpl: f,
      retry: { maxRetries: 0 },
    });
    await client.listTree("ws_1");
    const url = f.mock.calls[0]![0] as string;
    expect(url.startsWith("https://relay.test/v1/")).toBe(true);
  });

  it("handles empty query params gracefully", async () => {
    const f = mockFetch({ path: "/", entries: [], nextCursor: null });
    const client = makeClient(f);
    await client.listTree("ws_1", {});
    const url = f.mock.calls[0]![0] as string;
    // Default path is /
    expect(url).toContain("path=%2F");
  });
});
