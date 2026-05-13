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
  ChangeEvent,
  ChangeStreamConnection,
  Expansion,
  EventSummary,
  ReplayOptions,
  ResourceAtEventResult,
  SubscribeOptions,
  Subscription,
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

function makeWorkspaceToken(workspaceId = "ws_acme", agentName = "agent-test"): string {
  const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString("base64url");
  return `${encode({ alg: "HS256", typ: "JWT" })}.${encode({
    workspace_id: workspaceId,
    agent_name: agentName,
    aud: ["relayfile"],
  })}.sig`;
}

function flushMicrotasks(): Promise<void> {
  return Promise.resolve().then(() => undefined).then(() => undefined);
}

async function waitForWebSocket(): Promise<ProactiveMockWebSocket> {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const socket = ProactiveMockWebSocket.instances[0];
    if (socket) {
      return socket;
    }
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("Timed out waiting for the proactive runtime websocket.");
}

async function waitForExpectation(check: () => void): Promise<void> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 20; attempt += 1) {
    try {
      check();
      return;
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  }
  throw lastError;
}

class ProactiveMockWebSocket {
  static instances: ProactiveMockWebSocket[] = [];

  readonly url: string;
  sent: string[] = [];
  private readonly listeners = new Map<string, Set<(event: any) => void>>();

  constructor(url: string) {
    this.url = url;
    ProactiveMockWebSocket.instances.push(this);
  }

  addEventListener(type: string, handler: (event: any) => void): void {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, new Set());
    }
    this.listeners.get(type)!.add(handler);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close = vi.fn();

  emit(type: string, event: any): void {
    for (const handler of this.listeners.get(type) ?? []) {
      handler(event);
    }
  }
}

function makeClient(
  fetchImpl: typeof fetch,
  opts?: { retry?: { maxRetries: number }; token?: string }
) {
  return new RelayFileClient({
    baseUrl: "https://relay.test",
    token: opts?.token ?? "tok_test",
    fetchImpl,
    retry: opts?.retry ?? { maxRetries: 0 },
  });
}

// ---------------------------------------------------------------------------
// Existing methods
// ---------------------------------------------------------------------------

describe("RelayFileClient — existing methods", () => {
  describe("proactive runtime change APIs", () => {
    beforeEach(() => {
      ProactiveMockWebSocket.instances = [];
      Object.defineProperty(globalThis, "WebSocket", {
        configurable: true,
        writable: true,
        value: ProactiveMockWebSocket as unknown as typeof WebSocket,
      });
    });

    it("exports ChangeEvent and Subscription-compatible shapes", () => {
      const handle: Subscription = {
        async unsubscribe() {
          // no-op
        },
      };

      const event: ChangeEvent = {
        id: "evt_1",
        workspace: "ws_acme",
        agentId: "support-agent",
        type: "relayfile.changed",
        occurredAt: "2026-05-11T00:00:00.000Z",
        resource: {
          path: "/linear/issues/ENG-412.json",
          kind: "linear.issue",
          id: "ENG-412",
          provider: "linear",
        },
        summary: {
          title: "ENG-412",
          status: "In Progress",
          fieldsChanged: ["status"],
        },
        expand: async () =>
          ({
            level: "summary",
            path: "/linear/issues/ENG-412.json",
            summary: {
              title: "ENG-412",
              status: "In Progress",
              fieldsChanged: ["status"],
            },
          }) as Expansion,
        digest: "sha256:abc123",
      };

      const resource: ResourceAtEventResult = {
        path: event.resource.path,
        data: { id: "ENG-412" },
        digest: event.digest ?? "sha256:abc123",
      };
      const summary: EventSummary = event.summary;
      const replay: ReplayOptions = { replayOnStart: "last:25" };
      const subscribeOptions: SubscribeOptions = {
        coalesce: "fire-once",
        pathScope: ["/linear/issues/**"],
        drainMs: 5_000,
      };
      const connection: ChangeStreamConnection = {
        ready: Promise.resolve(),
        async unsubscribe() {
          // no-op
        },
      };

      expect(typeof handle.unsubscribe).toBe("function");
      expect(event.type).toBe("relayfile.changed");
      expect(resource.path).toBe("/linear/issues/ENG-412.json");
      expect(summary.title).toBe("ENG-412");
      expect(replay.replayOnStart).toBe("last:25");
      expect(subscribeOptions.coalesce).toBe("fire-once");
      expect(connection.ready).toBeInstanceOf(Promise);
    });

    it("subscribe multiplexes one workspace websocket and materializes matching change events", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/file?path=%2Flinear%2Fissues%2FENG-412.json")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-412.json",
              revision: "rev_2",
              contentType: "application/json",
              content: JSON.stringify({
                id: "ENG-412",
                provider: "linear",
                kind: "linear.issue",
                title: "ENG-412",
                status: "In Progress",
              }),
            }),
            text: async () => "",
          } as unknown as Response;
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });
      const issueHandler = vi.fn();
      const broadHandler = vi.fn();

      const issueHandle = client.subscribe(
        ["/linear/issues/**"],
        issueHandler,
        { coalesce: "none", pathScope: ["/linear/issues/**"] },
      );
      const broadHandle = client.subscribe(
        ["/linear/**"],
        broadHandler,
        { coalesce: "none" },
      );

      const socket = await waitForWebSocket();
      expect(ProactiveMockWebSocket.instances).toHaveLength(1);
      socket.emit("open", {});
      socket.emit("message", {
        data: JSON.stringify({
          eventId: "evt_linear_1",
          type: "file.updated",
          path: "/linear/issues/ENG-412.json",
          revision: "rev_2",
          timestamp: "2026-05-11T00:00:00.000Z",
        } satisfies FilesystemEvent),
      });

      await waitForExpectation(() => {
        expect(issueHandler).toHaveBeenCalledTimes(1);
        expect(broadHandler).toHaveBeenCalledTimes(1);
      });
      expect(issueHandler.mock.calls[0]?.[0]).toMatchObject({
        id: "evt_linear_1",
        workspace: "ws_acme",
        agentId: "support-agent",
        type: "relayfile.changed",
        resource: {
          path: "/linear/issues/ENG-412.json",
          kind: "linear.issue",
          id: "ENG-412",
          provider: "linear",
        },
        summary: {
          title: "ENG-412",
          status: "In Progress",
        },
      });

      await issueHandle.unsubscribe();
      await broadHandle.unsubscribe();
    });

    it("uses a bounded fallback digest when WebCrypto is unavailable", async () => {
      const originalCrypto = Object.getOwnPropertyDescriptor(globalThis, "crypto");
      Object.defineProperty(globalThis, "crypto", {
        configurable: true,
        value: {
          randomUUID: () => "00000000-0000-4000-8000-000000000000",
        },
      });
      try {
        const fileContent = "private file content that must not become the digest";
        const fetchImpl = vi.fn(async (url: string) => {
          if (url.includes("/fs/file?path=%2Fnotes%2Fprivate.txt")) {
            return {
              ok: true,
              status: 200,
              headers: new Headers({ "content-type": "application/json" }),
              json: async () => ({
                path: "/notes/private.txt",
                revision: "rev_1",
                contentType: "text/plain",
                content: fileContent,
              }),
              text: async () => "",
            } as unknown as Response;
          }
          throw new Error(`Unexpected fetch URL: ${url}`);
        });
        const client = makeClient(fetchImpl as unknown as typeof fetch, {
          token: makeWorkspaceToken("ws_acme", "support-agent"),
        });
        const handler = vi.fn();

        const handle = client.subscribe(["/notes/**"], handler, { coalesce: "none" });
        const socket = await waitForWebSocket();
        socket.emit("open", {});
        socket.emit("message", {
          data: JSON.stringify({
            eventId: "evt_note_1",
            type: "file.updated",
            path: "/notes/private.txt",
            revision: "rev_1",
            timestamp: "2026-05-11T00:00:00.000Z",
          } satisfies FilesystemEvent),
        });

        await waitForExpectation(() => {
          expect(handler).toHaveBeenCalledTimes(1);
        });
        const event = handler.mock.calls[0]?.[0] as ChangeEvent;
        expect(event.digest).toMatch(/^sha256-fallback:[0-9a-f]{8}$/);
        expect(event.digest).not.toContain(fileContent);

        await handle.unsubscribe();
      } finally {
        if (originalCrypto) {
          Object.defineProperty(globalThis, "crypto", originalCrypto);
        } else {
          delete (globalThis as { crypto?: Crypto }).crypto;
        }
      }
    });

    it("subscribe uses the acl token transport and coalesces rapid writes to one event", async () => {
      vi.useFakeTimers();
      try {
        const scopedToken = makeWorkspaceToken("ws_acme", "scoped-agent");
        const fetchImpl = vi.fn(async (url: string) => {
          if (url.includes("/fs/file?path=%2Flinear%2Fissues%2FENG-7.json")) {
            return {
              ok: true,
              status: 200,
              headers: new Headers({ "content-type": "application/json" }),
              json: async () => ({
                path: "/linear/issues/ENG-7.json",
                revision: "rev_2",
                contentType: "application/json",
                content: JSON.stringify({
                  id: "ENG-7",
                  provider: "linear",
                  kind: "linear.issue",
                  title: "ENG-7",
                  status: "Done",
                }),
              }),
              text: async () => "",
            } as unknown as Response;
          }
          throw new Error(`Unexpected fetch URL: ${url}`);
        });
        const client = makeClient(fetchImpl as unknown as typeof fetch, {
          token: makeWorkspaceToken("ws_acme", "default-agent"),
        });
        const handler = vi.fn();

        const handle = client.subscribe(
          ["/linear/issues/**"],
          handler,
          { aclToken: scopedToken, coalesce: "fire-once", coalesceMs: 50 },
        );

        await vi.advanceTimersByTimeAsync(0);
        const socket = ProactiveMockWebSocket.instances[0]!;
        expect(socket.url).toContain(`token=${scopedToken}`);
        socket.emit("open", {});
        socket.emit("message", {
          data: JSON.stringify({
            eventId: "evt_linear_1",
            type: "file.updated",
            path: "/linear/issues/ENG-7.json",
            revision: "rev_1",
            timestamp: "2026-05-11T00:00:00.000Z",
          } satisfies FilesystemEvent),
        });
        socket.emit("message", {
          data: JSON.stringify({
            eventId: "evt_linear_2",
            type: "file.updated",
            path: "/linear/issues/ENG-7.json",
            revision: "rev_2",
            timestamp: "2026-05-11T00:00:00.050Z",
          } satisfies FilesystemEvent),
        });

        await vi.advanceTimersByTimeAsync(60);
        vi.useRealTimers();
        await waitForExpectation(() => {
          expect(handler).toHaveBeenCalledTimes(1);
        });

        expect(handler.mock.calls[0]?.[0]).toMatchObject({
          id: "evt_linear_2",
          summary: {
            title: "ENG-7",
            status: "Done",
          },
        });
        expect(fetchImpl).toHaveBeenCalledTimes(1);

        await handle.unsubscribe();
      } finally {
        vi.useRealTimers();
      }
    });

    it("getResourceAtEvent reuses the retained cache before calling the API", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/file?path=%2Flinear%2Fissues%2FENG-9.json")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-9.json",
              revision: "rev_1",
              contentType: "application/json",
              content: JSON.stringify({
                id: "ENG-9",
                provider: "linear",
                kind: "linear.issue",
                title: "ENG-9",
              }),
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes/resource")) {
          throw new Error("resource endpoint should not be used when the cache is warm");
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });

      let receivedEvent: ChangeEvent | undefined;
      const handle = client.subscribe(
        ["/linear/issues/**"],
        (event) => {
          receivedEvent = event;
        },
        { coalesce: "none" },
      );

      const socket = await waitForWebSocket();
      socket.emit("open", {});
      socket.emit("message", {
        data: JSON.stringify({
          eventId: "evt_linear_cache",
          type: "file.updated",
          path: "/linear/issues/ENG-9.json",
          revision: "rev_1",
          timestamp: "2026-05-11T00:00:00.000Z",
        } satisfies FilesystemEvent),
      });

      await waitForExpectation(() => {
        expect(receivedEvent).toBeDefined();
      });
      const resource = await client.getResourceAtEvent(receivedEvent!.id);
      expect(resource).toMatchObject({
        path: "/linear/issues/ENG-9.json",
        data: { id: "ENG-9", provider: "linear", kind: "linear.issue", title: "ENG-9" },
      });
      expect(fetchImpl).toHaveBeenCalledTimes(1);

      await handle.unsubscribe();
    });

    it("open returns a ready handle and primes replay-on-start through the change log", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/changes?since=")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              events: [
                {
                  id: "evt_replay_1",
                  workspace: "ws_acme",
                  type: "relayfile.changed",
                  occurredAt: "2026-05-11T00:00:00.000Z",
                  resource: {
                    path: "/linear/issues/ENG-100.json",
                    kind: "linear.issue",
                    id: "ENG-100",
                    provider: "linear",
                  },
                  summary: {
                    title: "ENG-100",
                    status: "Todo",
                  },
                  digest: "sha256:replay",
                },
              ],
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes?last=1")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              events: [
                {
                  id: "evt_replay_1",
                  workspace: "ws_acme",
                  type: "relayfile.changed",
                  occurredAt: "2026-05-11T00:00:00.000Z",
                  resource: {
                    path: "/linear/issues/ENG-100.json",
                    kind: "linear.issue",
                    id: "ENG-100",
                    provider: "linear",
                  },
                  summary: {
                    title: "ENG-100",
                    status: "Todo",
                  },
                  digest: "sha256:replay",
                },
              ],
            }),
            text: async () => "",
          } as unknown as Response;
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });

      const connection = client.open({
        workspaceId: "ws_acme",
        replayOnStart: "since:2026-05-11T00:00:00.000Z",
      });

      const socket = await waitForWebSocket();
      socket.emit("open", {});
      await expect(connection.ready).resolves.toBeUndefined();

      const replayed = await client.listLastNChanges(1);
      expect(replayed.events[0]).toMatchObject({
        id: "evt_replay_1",
        resource: {
          path: "/linear/issues/ENG-100.json",
        },
      });
      expect(fetchImpl).toHaveBeenCalledTimes(2);

      await connection.unsubscribe();
    });

    it("listChangesSince fetches retained events and wires expand()", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/changes?since=")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              events: [
                {
                  id: "evt_history_1",
                  workspace: "ws_acme",
                  type: "relayfile.changed",
                  occurredAt: "2026-05-11T00:00:00.000Z",
                  resource: {
                    path: "/linear/issues/ENG-55.json",
                    kind: "linear.issue",
                    id: "ENG-55",
                    provider: "linear",
                  },
                  summary: {
                    title: "ENG-55",
                    status: "Done",
                  },
                  digest: "sha256:history",
                },
              ],
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes/resource?eventId=evt_history_1")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-55.json",
              data: { id: "ENG-55", title: "ENG-55" },
              digest: "sha256:history",
            }),
            text: async () => "",
          } as unknown as Response;
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });

      const result = await client.listChangesSince("2026-05-11T00:00:00.000Z");
      expect(result.events[0]).toMatchObject({
        id: "evt_history_1",
        summary: {
          title: "ENG-55",
          status: "Done",
        },
      });
      await expect(result.events[0]!.expand("full")).resolves.toMatchObject({
        level: "full",
        path: "/linear/issues/ENG-55.json",
        data: { id: "ENG-55", title: "ENG-55" },
      });
      await expect(result.events[0]!.expand("diff")).rejects.toMatchObject({
        name: "M2NotImplementedError",
        code: "M2_NOT_IMPLEMENTED",
      });
    });

    it("getResourceAtEvent falls back to the retained change-log endpoint when the cache is cold", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/changes/resource?eventId=evt_rest_1")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-77.json",
              data: { id: "ENG-77", title: "ENG-77" },
              digest: "sha256:rest",
            }),
            text: async () => "",
          } as unknown as Response;
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });

      await expect(client.getResourceAtEvent("evt_rest_1")).resolves.toMatchObject({
        path: "/linear/issues/ENG-77.json",
        data: { id: "ENG-77", title: "ENG-77" },
        digest: "sha256:rest",
      });
    });

    it("listLastNChanges preserves hydrated resources when replay refreshes the same event id", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/file?path=%2Flinear%2Fissues%2FENG-9.json")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-9.json",
              revision: "rev_1",
              contentType: "application/json",
              content: JSON.stringify({
                id: "ENG-9",
                provider: "linear",
                kind: "linear.issue",
                title: "ENG-9",
              }),
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes?last=1")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              events: [
                {
                  id: "evt_linear_cache",
                  workspace: "ws_acme",
                  type: "relayfile.changed",
                  occurredAt: "2026-05-11T00:00:00.000Z",
                  resource: {
                    path: "/linear/issues/ENG-9.json",
                    kind: "linear.issue",
                    id: "ENG-9",
                    provider: "linear",
                  },
                  summary: {
                    title: "ENG-9",
                  },
                  digest: "sha256:cached",
                },
              ],
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes/resource?eventId=evt_linear_cache")) {
          throw new Error("resource endpoint should not be used after replay refresh when the resource is already hydrated");
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = makeClient(fetchImpl as unknown as typeof fetch, {
        token: makeWorkspaceToken("ws_acme", "support-agent"),
      });

      const handle = client.subscribe(
        ["/linear/issues/**"],
        () => {
          // no-op
        },
        { coalesce: "none" },
      );

      const socket = await waitForWebSocket();
      socket.emit("open", {});
      socket.emit("message", {
        data: JSON.stringify({
          eventId: "evt_linear_cache",
          type: "file.updated",
          path: "/linear/issues/ENG-9.json",
          revision: "rev_1",
          timestamp: "2026-05-11T00:00:00.000Z",
        } satisfies FilesystemEvent),
      });

      await waitForExpectation(() => {
        expect(fetchImpl).toHaveBeenCalledTimes(1);
      });

      const replayed = await client.listLastNChanges(1);
      await expect(replayed.events[0]!.expand("full")).resolves.toMatchObject({
        level: "full",
        path: "/linear/issues/ENG-9.json",
        data: { id: "ENG-9", provider: "linear", kind: "linear.issue", title: "ENG-9" },
      });
      expect(fetchImpl).toHaveBeenCalledTimes(2);

      await handle.unsubscribe();
    });

    it("honors configurable local change-log retention before falling back to the retained endpoint", async () => {
      const fetchImpl = vi.fn(async (url: string) => {
        if (url.includes("/fs/file?path=%2Flinear%2Fissues%2FENG-91.json")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-91.json",
              revision: "rev_1",
              contentType: "application/json",
              content: JSON.stringify({
                id: "ENG-91",
                provider: "linear",
                kind: "linear.issue",
                title: "ENG-91",
              }),
            }),
            text: async () => "",
          } as unknown as Response;
        }
        if (url.includes("/fs/changes/resource?eventId=evt_retention_1")) {
          return {
            ok: true,
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            json: async () => ({
              path: "/linear/issues/ENG-91.json",
              data: { id: "ENG-91", title: "ENG-91", from: "retained-endpoint" },
              digest: "sha256:retained",
            }),
            text: async () => "",
          } as unknown as Response;
        }
        throw new Error(`Unexpected fetch URL: ${url}`);
      });
      const client = new RelayFileClient({
        baseUrl: "https://relay.test",
        token: makeWorkspaceToken("ws_acme", "support-agent"),
        fetchImpl: fetchImpl as unknown as typeof fetch,
        retry: { maxRetries: 0 },
        changeLog: { retentionMs: 0, maxEntries: 5 },
      });

      const handle = client.subscribe(
        ["/linear/issues/**"],
        () => {
          // no-op
        },
        { coalesce: "none" },
      );

      const socket = await waitForWebSocket();
      socket.emit("open", {});
      socket.emit("message", {
        data: JSON.stringify({
          eventId: "evt_retention_1",
          type: "file.updated",
          path: "/linear/issues/ENG-91.json",
          revision: "rev_1",
          timestamp: "2026-05-11T00:00:00.000Z",
        } satisfies FilesystemEvent),
      });

      await flushMicrotasks();
      await flushMicrotasks();
      expect(fetchImpl).toHaveBeenCalledTimes(1);

      await expect(client.getResourceAtEvent("evt_retention_1")).resolves.toMatchObject({
        path: "/linear/issues/ENG-91.json",
        data: { id: "ENG-91", title: "ENG-91", from: "retained-endpoint" },
        digest: "sha256:retained",
      });
      expect(fetchImpl).toHaveBeenCalledTimes(2);

      await handle.unsubscribe();
    });
  });

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

    it("forwards contentIdentity in the body when provided", async () => {
      const payload: WriteQueuedResponse = {
        opId: "op_1",
        status: "queued",
        targetRevision: "rev_4",
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.writeFile({
        workspaceId: "ws_acme",
        path: "/github/push/abc123.json",
        baseRevision: "rev_3",
        content: "{}",
        contentIdentity: { kind: "github.push", key: "abc123" },
      });
      const body = JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string);
      expect(body.contentIdentity).toEqual({ kind: "github.push", key: "abc123" });
    });

    it("omits contentIdentity from the body when not provided", async () => {
      const payload: WriteQueuedResponse = {
        opId: "op_1",
        status: "queued",
        targetRevision: "rev_4",
      };
      const f = mockFetch(payload);
      const client = makeClient(f);
      await client.writeFile({
        workspaceId: "ws_acme",
        path: "/x.json",
        baseRevision: "rev_3",
        content: "{}",
      });
      const body = JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string);
      expect(body.contentIdentity).toBeUndefined();
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
    it("returns the bulk write response matching the server contract", async () => {
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

  describe("forks", () => {
    it("createFork posts proposalId and ttlSeconds and returns a handle", async () => {
      const payload = {
        forkId: "fork_123",
        proposalId: "proposal_1",
        workspaceId: "ws_acme",
        expiresAt: "2026-04-28T00:00:00.000Z",
        parentRevision: "rev_1",
      };
      const f = mockFetch(payload);
      const client = makeClient(f);

      const res = await client.createFork({
        workspaceId: "ws_acme",
        proposalId: "proposal_1",
        ttlSeconds: 3600,
      });

      expect(res).toEqual(payload);
      const url = f.mock.calls[0]![0] as string;
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(url).toBe("https://relay.test/v1/workspaces/ws_acme/forks");
      expect(init.method).toBe("POST");
      expect(JSON.parse(init.body as string)).toEqual({
        proposalId: "proposal_1",
        ttlSeconds: 3600,
      });
    });

    it("createFork omits ttlSeconds when unset", async () => {
      const f = mockFetch({
        forkId: "fork_123",
        proposalId: "proposal_1",
        workspaceId: "ws_acme",
        expiresAt: "2026-04-28T00:00:00.000Z",
        parentRevision: "rev_1",
      });
      const client = makeClient(f);

      await client.createFork({
        workspaceId: "ws_acme",
        proposalId: "proposal_1",
      });

      const init = f.mock.calls[0]![1] as RequestInit;
      expect(JSON.parse(init.body as string)).toEqual({
        proposalId: "proposal_1",
      });
    });

    it("discardFork deletes the fork and resolves on 204", async () => {
      const f = mockFetch({}, 204);
      const client = makeClient(f);

      await expect(client.discardFork({
        workspaceId: "ws_acme",
        forkId: "fork_123",
      })).resolves.toBeUndefined();

      const url = f.mock.calls[0]![0] as string;
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(url).toBe("https://relay.test/v1/workspaces/ws_acme/forks/fork_123");
      expect(init.method).toBe("DELETE");
    });

    it("commitFork posts and returns commit counts", async () => {
      const payload = { revision: "rev_2", writtenCount: 2, deletedCount: 1 };
      const f = mockFetch(payload);
      const client = makeClient(f);

      const res = await client.commitFork({
        workspaceId: "ws_acme",
        forkId: "fork_123",
      });

      expect(res).toEqual(payload);
      const url = f.mock.calls[0]![0] as string;
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(url).toBe("https://relay.test/v1/workspaces/ws_acme/forks/fork_123/commit");
      expect(init.method).toBe("POST");
      expect(init.body).toBeUndefined();
    });

    it("writeFile with forkId appends forkId and leaves body unchanged", async () => {
      const f = mockFetch({ opId: "op_1", status: "queued", targetRevision: "fork:fork_123:1" });
      const client = makeClient(f);

      await client.writeFile({
        workspaceId: "ws_acme",
        path: "/docs/a.md",
        baseRevision: "0",
        content: "# a",
        contentType: "text/markdown",
        forkId: "fork_123",
      });

      const url = f.mock.calls[0]![0] as string;
      const init = f.mock.calls[0]![1] as RequestInit;
      expect(url).toContain("path=%2Fdocs%2Fa.md");
      expect(url).toContain("forkId=fork_123");
      expect(JSON.parse(init.body as string)).toEqual({
        contentType: "text/markdown",
        content: "# a",
      });
    });

    it("readFile with forkId appends forkId", async () => {
      const f = mockFetch({
        path: "/docs/a.md",
        revision: "fork:fork_123:1",
        contentType: "text/markdown",
        content: "# a",
      });
      const client = makeClient(f);

      await client.readFile({
        workspaceId: "ws_acme",
        path: "/docs/a.md",
        forkId: "fork_123",
      });

      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("path=%2Fdocs%2Fa.md");
      expect(url).toContain("forkId=fork_123");
    });

    it("bulkWrite with forkId appends forkId", async () => {
      const f = mockFetch({ written: 1, errorCount: 0, errors: [], correlationId: "corr_1" });
      const client = makeClient(f);

      await client.bulkWrite({
        workspaceId: "ws_acme",
        forkId: "fork_123",
        files: [{ path: "/docs/a.md", content: "# a" }],
      });

      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("/v1/workspaces/ws_acme/fs/bulk?");
      expect(url).toContain("forkId=fork_123");
    });

    it("listTree with forkId appends forkId", async () => {
      const f = mockFetch({ path: "/", entries: [], nextCursor: null });
      const client = makeClient(f);

      await client.listTree("ws_acme", { path: "/docs", forkId: "fork_123" });

      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("path=%2Fdocs");
      expect(url).toContain("forkId=fork_123");
    });

    it("queryFiles with forkId appends forkId", async () => {
      const f = mockFetch({ items: [], nextCursor: null });
      const client = makeClient(f);

      await client.queryFiles("ws_acme", { path: "/docs", forkId: "fork_123" });

      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("path=%2Fdocs");
      expect(url).toContain("forkId=fork_123");
    });

    it("deleteFile with forkId appends forkId", async () => {
      const f = mockFetch({ opId: "op_1", status: "queued", targetRevision: "fork:fork_123:2" });
      const client = makeClient(f);

      await client.deleteFile({
        workspaceId: "ws_acme",
        path: "/docs/a.md",
        baseRevision: "fork:fork_123:1",
        forkId: "fork_123",
      });

      const url = f.mock.calls[0]![0] as string;
      expect(url).toContain("path=%2Fdocs%2Fa.md");
      expect(url).toContain("forkId=fork_123");
    });

    it("writeFile without forkId does not include forkId", async () => {
      const f = mockFetch({ opId: "op_1", status: "queued", targetRevision: "rev_1" });
      const client = makeClient(f);

      await client.writeFile({
        workspaceId: "ws_acme",
        path: "/docs/a.md",
        baseRevision: "0",
        content: "# a",
      });

      const url = f.mock.calls[0]![0] as string;
      expect(url).not.toContain("forkId=");
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

  it("preserves parent_moved details on commitFork errors", async () => {
    const body = {
      code: "parent_moved",
      message: "parent moved",
      correlationId: "c_parent",
      currentRevision: "rev_new",
    };
    const f = mockFetch(body, 409);
    const client = makeClient(f);
    try {
      await client.commitFork({ workspaceId: "ws_1", forkId: "fork_1" });
      expect.unreachable("Should have thrown");
    } catch (error) {
      expect(error).toBeInstanceOf(RelayFileApiError);
      const apiError = error as RelayFileApiError;
      expect(apiError.code).toBe("parent_moved");
      expect(apiError.details?.currentRevision).toBe("rev_new");
    }
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

  it("allows DLQ-prefixed paths through the normal file APIs", async () => {
    const writeFetch = mockFetch({
      opId: "op_dlq",
      status: "queued",
      targetRevision: "rev_dlq",
    } satisfies WriteQueuedResponse);
    const client = makeClient(writeFetch);

    await client.writeFile({
      workspaceId: "ws_acme",
      path: "/_dlq/evt_dlq.json",
      content: JSON.stringify({ eventId: "evt_dlq" }),
      contentType: "application/json",
    });

    expect((writeFetch.mock.calls[0]![0] as string)).toContain(
      "/v1/workspaces/ws_acme/fs/file?path=%2F_dlq%2Fevt_dlq.json",
    );
    expect(JSON.parse((writeFetch.mock.calls[0]![1] as RequestInit).body as string)).toMatchObject({
      contentType: "application/json",
      content: JSON.stringify({ eventId: "evt_dlq" }),
    });
  });
});
