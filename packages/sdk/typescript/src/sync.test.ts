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

  it("falls back to polling when preferred and only delivers events that appear after the seed poll", async () => {
    // PR #98: polling now treats the FIRST poll as a history catch-up — it
    // seeds the dedupe cache without invoking handlers, then delivers any
    // new events from subsequent polls. This avoids replaying the entire
    // event log to the handler on startup. Events delivered post-seed are
    // emitted in chronological order and deduped by eventId.
    const getEvents = vi
      .fn()
      // First call (seed): a stale event the user has already processed.
      // Pre-fix this would have been re-emitted; post-fix it is silently
      // remembered.
      .mockResolvedValueOnce({
        events: [
          {
            eventId: "evt_seed",
            type: "file.created",
            path: "/docs/already-seen.md",
            revision: "rev_0",
            timestamp: "2026-03-26T00:00:00Z"
          }
        ],
        nextCursor: null
      })
      // Second call: a fresh event PLUS the seed (still in the latest
      // page). The fresh one should be delivered exactly once; the seed
      // should be deduped.
      .mockResolvedValue({
        events: [
          {
            eventId: "evt_seed",
            type: "file.created",
            path: "/docs/already-seen.md",
            revision: "rev_0",
            timestamp: "2026-03-26T00:00:00Z"
          },
          {
            eventId: "evt_new",
            type: "file.created",
            path: "/docs/new.md",
            revision: "rev_1",
            timestamp: "2026-03-26T00:00:01Z"
          }
        ],
        nextCursor: null
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
    await new Promise((resolve) => setTimeout(resolve, 30));
    await sync.stop();

    expect(getEvents).toHaveBeenCalled();
    // Seed page produced no handler invocations; only the new event made it.
    expect(events.map((event) => event.path)).toEqual(["/docs/new.md"]);
    expect(sync.getState()).toBe("closed");
  });

  it("does not pass nextCursor when polling — forward-progress, not backwards-pagination", async () => {
    // Bug 3: the events feed's nextCursor walks backwards through history.
    // The legacy poll loop chained nextCursor on every page and walked the
    // SDK off the live edge until it never returned. This regression test
    // asserts the SDK never sends a cursor — every poll requests the
    // latest events fresh and dedupes by eventId.
    const calls: any[] = [];
    const getEvents = vi.fn().mockImplementation((_workspaceId: string, options: any) => {
      calls.push(options);
      return Promise.resolve({ events: [], nextCursor: "cur_walks_backwards" });
    });

    const sync = new RelayFileSync({
      client: makeClient(getEvents),
      workspaceId: "ws_acme",
      preferPolling: true,
      pollIntervalMs: 5
    });

    sync.start();
    await new Promise((resolve) => setTimeout(resolve, 25));
    await sync.stop();

    expect(calls.length).toBeGreaterThanOrEqual(2);
    for (const call of calls) {
      expect(call.cursor).toBeUndefined();
    }
  });

  it("logs a console.warn when WS factory throws and the SDK degrades to polling", async () => {
    // Bug 2: when WS fails to open the SDK used to fall back to polling
    // silently; only the `error` event surfaced, and most callers never
    // subscribed. Now we always log on the always-on console.warn channel
    // (NOT gated by RELAYFILE_SDK_DEBUG) and fire the optional
    // onPollingFallback callback.
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const fallback = vi.fn();

    const sync = new RelayFileSync({
      client: makeClient(),
      workspaceId: "ws_acme",
      baseUrl: "https://relay.test",
      token: "ws_token",
      pollIntervalMs: 5,
      reconnect: false,
      webSocketFactory: () => {
        throw new Error("connect failed");
      },
      onPollingFallback: fallback
    });

    sync.start();
    await new Promise((resolve) => setTimeout(resolve, 10));
    await sync.stop();

    expect(warnSpy).toHaveBeenCalled();
    const message = warnSpy.mock.calls.flat().map(String).join(" | ");
    expect(message).toMatch(/\[relayfile-sdk\]/);
    expect(message).toMatch(/falling back to HTTP polling/);
    expect(fallback).toHaveBeenCalledWith(
      expect.objectContaining({ reason: "ws-factory-threw" })
    );
    warnSpy.mockRestore();
  });

  it("does NOT log the polling-fallback warn when the caller explicitly opted into polling", async () => {
    // The warn is for unintended degradation only. preferPolling=true is the
    // user telling us "this is intentional, don't shout about it".
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const sync = new RelayFileSync({
      client: makeClient(),
      workspaceId: "ws_acme",
      preferPolling: true,
      pollIntervalMs: 5
    });
    sync.start();
    await new Promise((resolve) => setTimeout(resolve, 10));
    await sync.stop();

    const messages = warnSpy.mock.calls.flat().map(String).join(" | ");
    expect(messages).not.toMatch(/falling back to HTTP polling/);
    warnSpy.mockRestore();
  });

  it("auto-derives the WS token from client.getToken when no token option is passed", async () => {
    // Bug 1: when onWrite/RelayFileSync was constructed without an explicit
    // `token`, the URL was built without one and the server rejected the
    // upgrade silently. The new behavior is to call `client.getToken()`
    // (the same JWT REST methods use) and put that on the URL.
    const sockets: MockWebSocket[] = [];
    const getToken = vi.fn().mockResolvedValue("client-derived-token");
    const client = {
      getEvents: vi.fn().mockResolvedValue({ events: [], nextCursor: null }),
      getToken
    } as unknown as RelayFileClient;

    const sync = new RelayFileSync({
      client,
      workspaceId: "ws_acme",
      baseUrl: "https://relay.test",
      // NB: token deliberately omitted.
      webSocketFactory: (url) => {
        const socket = new MockWebSocket(url);
        sockets.push(socket);
        return socket;
      }
    });

    sync.start();
    // Token resolution is a Promise, so the factory call is deferred to a
    // microtask. A single setTimeout(0) is enough to flush.
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(getToken).toHaveBeenCalled();
    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.url).toContain("token=client-derived-token");
    await sync.stop();
  });

  it("re-resolves the token on every reconnect (handles mid-session token rotation)", async () => {
    // Bug 4: the previous implementation captured `token` once at
    // construction. If the JWT rotated mid-session, the watchdog reconnected
    // with the SAME stale token and the server kept rejecting the upgrade —
    // an infinite reconnect loop. The function form of `token` must be
    // re-invoked on every reconnect attempt.
    const tokens = ["t1", "t2", "t3"];
    const sockets: MockWebSocket[] = [];
    const sync = new RelayFileSync({
      client: makeClient(),
      workspaceId: "ws_acme",
      baseUrl: "https://relay.test",
      token: () => tokens.shift() ?? "tEXHAUSTED",
      reconnect: { minDelayMs: 5, maxDelayMs: 5 },
      webSocketFactory: (url) => {
        const socket = new MockWebSocket(url);
        sockets.push(socket);
        return socket;
      }
    });

    sync.start();
    expect(sockets[0]!.url).toContain("token=t1");

    sockets[0]!.emit("close", { code: 4001, reason: "auth" });
    await new Promise((resolve) => setTimeout(resolve, 12));
    expect(sockets.length).toBeGreaterThanOrEqual(2);
    expect(sockets[1]!.url).toContain("token=t2");

    sockets[1]!.emit("close", { code: 4001, reason: "auth" });
    await new Promise((resolve) => setTimeout(resolve, 25));
    expect(sockets.length).toBeGreaterThanOrEqual(3);
    expect(sockets[2]!.url).toContain("token=t3");

    await sync.stop();
  });

  it("force-reconnects when no frames arrive within the pong timeout (silent socket death)", async () => {
    // Reproduces the "live event drop" failure mode: TCP/WS dies silently —
    // no `error`, no `close` — and the JS layer happily considers the socket
    // open. The watchdog should notice via `lastFrameAt` and force a fresh
    // connection so we resume receiving broadcasts.
    vi.useFakeTimers();
    try {
      const sockets: MockWebSocket[] = [];
      const sync = new RelayFileSync({
        client: makeClient(),
        workspaceId: "ws_acme",
        baseUrl: "https://relay.test",
        token: "ws_token",
        pingIntervalMs: 50,
        pongTimeoutMs: 100,
        reconnect: { minDelayMs: 5, maxDelayMs: 5 },
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      });

      sync.start();
      sockets[0]!.emit("open", {});

      // First tick (t=50): ping is sent; lastPingSentAt becomes non-zero,
      // lastFrameAt is still the open timestamp. No reconnect yet because
      // sinceFrame (~50) < pongTimeoutMs (100).
      await vi.advanceTimersByTimeAsync(50);
      expect(sockets[0]!.sent).toContain(JSON.stringify({ type: "ping" }));
      expect(sockets).toHaveLength(1);

      // Subsequent ticks with no inbound frames eventually push sinceFrame
      // past pongTimeoutMs and the watchdog forces a reconnect. Advance well
      // past the threshold + the reconnect delay so we observe the new socket.
      await vi.advanceTimersByTimeAsync(200);
      expect(sockets.length).toBeGreaterThanOrEqual(2);

      // Once the new socket is open and we feed it a frame, the watchdog
      // sees liveness and does NOT flap on the next tick.
      sockets[1]!.emit("open", {});
      sockets[1]!.emit("message", {
        data: JSON.stringify({ type: "pong", ts: "2026-05-07T00:00:00Z" })
      });
      const beforeStable = sockets.length;
      await vi.advanceTimersByTimeAsync(60);
      sockets[1]!.emit("message", {
        data: JSON.stringify({ type: "pong", ts: "2026-05-07T00:00:01Z" })
      });
      await vi.advanceTimersByTimeAsync(60);
      expect(sockets).toHaveLength(beforeStable);

      await sync.stop();
    } finally {
      vi.useRealTimers();
    }
  });

  // Pins CodeRabbit P1 on PR #93. The pongTimeoutMs option is documented as
  // "how long to wait after sending a ping" — that timeout MUST be measured
  // from `lastPingSentAt`, not from `lastFrameAt`. With ping=50, pong=200,
  // a ping at t=50 must be allowed to remain unanswered until at least
  // t=250 (50 + 200), not reconnect at t=200 (lastFrameAt + pongTimeoutMs).
  it("measures pongTimeoutMs from the unanswered ping, not from the last frame", async () => {
    vi.useFakeTimers();
    try {
      const sockets: MockWebSocket[] = [];
      const sync = new RelayFileSync({
        client: makeClient(),
        workspaceId: "ws_acme",
        baseUrl: "https://relay.test",
        token: "ws_token",
        pingIntervalMs: 50,
        pongTimeoutMs: 200,
        reconnect: { minDelayMs: 5, maxDelayMs: 5 },
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      });

      sync.start();
      sockets[0]!.emit("open", {});

      // First ping sent at ~t=50.
      await vi.advanceTimersByTimeAsync(50);
      expect(sockets[0]!.sent.length).toBe(1);

      // At t=200 (50ms after open + 150ms of silence) the LEGACY logic
      // would have measured `now - lastFrameAt = 200` against
      // `pongTimeoutMs = 200` and tripped. The fixed logic measures from
      // the ping at t=50: sincePing = 150 < 200 → still waiting, no
      // reconnect.
      await vi.advanceTimersByTimeAsync(150);
      expect(sockets).toHaveLength(1);

      // We also must NOT pile up additional pings while the first is
      // outstanding — sent.length should still be 1.
      expect(sockets[0]!.sent.length).toBe(1);

      // At t=300 (250ms after the ping at t=50) sincePing crosses
      // pongTimeoutMs and the watchdog trips. Add a few ms past that for
      // the reconnect timer (delay 5ms) to actually fire.
      await vi.advanceTimersByTimeAsync(120);
      expect(sockets.length).toBeGreaterThanOrEqual(2);

      await sync.stop();
    } finally {
      vi.useRealTimers();
    }
  });

  // Pins Codex P2 from PR #93: forceReconnect (watchdog or failed-ping path)
  // swaps in a fresh socket BEFORE the OS-layer close event for the doomed
  // socket actually fires. If the close handler treated that stale event as
  // authoritative it would clearPingTimer() — killing the new socket's
  // heartbeat — and possibly schedule another reconnect after the timer
  // had already fired. The handler must early-return when this.socket
  // !== socket.
  it("ignores stale close events for sockets already replaced by forceReconnect", async () => {
    vi.useFakeTimers();
    try {
      const sockets: MockWebSocket[] = [];
      const sync = new RelayFileSync({
        client: makeClient(),
        workspaceId: "ws_acme",
        baseUrl: "https://relay.test",
        token: "ws_token",
        pingIntervalMs: 50,
        pongTimeoutMs: 100,
        reconnect: { minDelayMs: 5, maxDelayMs: 5 },
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      });

      sync.start();
      sockets[0]!.emit("open", {});

      // Force the watchdog to swap in socket #2 by starving socket #0 of
      // frames past pongTimeoutMs.
      await vi.advanceTimersByTimeAsync(50);  // first ping sent on #0
      await vi.advanceTimersByTimeAsync(200); // watchdog trips → socket #1 created
      expect(sockets.length).toBeGreaterThanOrEqual(2);

      // New socket comes up and is healthy.
      sockets[1]!.emit("open", {});
      sockets[1]!.emit("message", {
        data: JSON.stringify({ type: "pong", ts: "2026-05-07T00:00:00Z" })
      });
      const stableCount = sockets.length;
      const sentOnNewBeforeStaleClose = sockets[1]!.sent.length;

      // Now the OLD socket finally emits its close — long after it was
      // replaced. Pre-fix, this would clearPingTimer() (killing the live
      // socket's heartbeat) and scheduleReconnect() (creating a duplicate
      // reconnect cycle). With the guard, both side effects must be skipped.
      sockets[0]!.emit("close", { code: 4000, reason: "ping-send-failed" });

      // The next ping interval should still tick on the live socket — proof
      // that clearPingTimer was NOT called by the stale-close handler.
      await vi.advanceTimersByTimeAsync(60);
      expect(sockets[1]!.sent.length).toBeGreaterThan(sentOnNewBeforeStaleClose);

      // No extra reconnect was scheduled — socket count is still stable.
      expect(sockets.length).toBe(stableCount);

      await sync.stop();
    } finally {
      vi.useRealTimers();
    }
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
