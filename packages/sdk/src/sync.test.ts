import { beforeEach, describe, expect, it, vi } from "vitest";

import type { RelayFileClient } from "./client.js";
import { RelayFileSync } from "./sync.js";
import type { FilesystemEvent } from "./types.js";

class MockWebSocket {
  readonly url: string;
  sent: string[] = [];
  private readonly listeners = new Map<string, Set<(event: any) => void>>();

  constructor(url: string) {
    this.url = url;
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

  close(): void {
    this.emit("close", { code: 1000, reason: "closed" });
  }

  emit(type: string, event: any): void {
    for (const handler of this.listeners.get(type) ?? []) {
      handler(event);
    }
  }
}

function makeClient(getEventsImpl?: any): RelayFileClient {
  return {
    getEvents: getEventsImpl ?? vi.fn().mockResolvedValue({ events: [], nextCursor: null })
  } as unknown as RelayFileClient;
}

describe("RelayFileSync", () => {
  beforeEach(() => {
    vi.restoreAllMocks?.();
  });

  it("streams WebSocket filesystem events and pong replies", async () => {
    const sockets: MockWebSocket[] = [];
    const sync = new RelayFileSync({
      client: makeClient(),
      workspaceId: "ws_acme",
      baseUrl: "https://relay.test",
      token: "ws_token",
      pingIntervalMs: 5,
      webSocketFactory: (url) => {
        const socket = new MockWebSocket(url);
        sockets.push(socket);
        return socket;
      }
    });

    const events: FilesystemEvent[] = [];
    const pongs: Array<string | undefined> = [];
    sync.on("event", (event) => events.push(event));
    sync.on("pong", (pong) => pongs.push(pong.timestamp));

    sync.start();
    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.url).toBe("wss://relay.test/v1/workspaces/ws_acme/fs/ws?token=ws_token");

    sockets[0]!.emit("open", {});
    sockets[0]!.emit("message", {
      data: JSON.stringify({
        type: "file.updated",
        path: "/docs/readme.md",
        revision: "rev_2",
        timestamp: "2026-03-26T00:00:00Z"
      })
    });
    sockets[0]!.emit("message", {
      data: JSON.stringify({
        type: "pong",
        ts: "2026-03-26T00:00:01Z"
      })
    });

    await new Promise((resolve) => setTimeout(resolve, 10));

    expect(events).toEqual([
      {
        eventId: "ws:file.updated:/docs/readme.md:rev_2:2026-03-26T00:00:00Z",
        type: "file.updated",
        path: "/docs/readme.md",
        revision: "rev_2",
        timestamp: "2026-03-26T00:00:00Z"
      }
    ]);
    expect(pongs).toEqual(["2026-03-26T00:00:01Z"]);
    expect(sockets[0]!.sent).toContain(JSON.stringify({ type: "ping" }));

    await sync.stop();
  });

  it("falls back to polling when preferred", async () => {
    const getEvents = vi
      .fn()
      .mockResolvedValueOnce({
        events: [
          {
            eventId: "evt_1",
            type: "file.created",
            path: "/docs/new.md",
            revision: "rev_1",
            timestamp: "2026-03-26T00:00:00Z"
          }
        ],
        nextCursor: "cur_1"
      })
      .mockResolvedValue({
        events: [],
        nextCursor: "cur_1"
      });

    const sync = new RelayFileSync({
      client: makeClient(getEvents),
      workspaceId: "ws_acme",
      preferPolling: true,
      pollIntervalMs: 5
    });

    const events: FilesystemEvent[] = [];
    sync.on("event", (event) => events.push(event));

    sync.start();
    await new Promise((resolve) => setTimeout(resolve, 12));
    await sync.stop();

    expect(getEvents).toHaveBeenCalled();
    expect(events[0]!.path).toBe("/docs/new.md");
    expect(sync.getState()).toBe("closed");
  });

  it("reconnects after an unexpected close", async () => {
    const sockets: MockWebSocket[] = [];
    const sync = new RelayFileSync({
      client: makeClient(),
      workspaceId: "ws_acme",
      baseUrl: "https://relay.test",
      token: "ws_token",
      reconnect: { minDelayMs: 5, maxDelayMs: 5 },
      webSocketFactory: (url) => {
        const socket = new MockWebSocket(url);
        sockets.push(socket);
        return socket;
      }
    });

    sync.start();
    expect(sockets).toHaveLength(1);

    sockets[0]!.emit("open", {});
    sockets[0]!.emit("close", { code: 1006, reason: "dropped" });

    await new Promise((resolve) => setTimeout(resolve, 12));

    expect(sockets).toHaveLength(2);

    await sync.stop();
  });
});
