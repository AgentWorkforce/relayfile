import { beforeEach, describe, expect, it, vi } from "vitest";

import type { RelayFileClient } from "./client.js";
import { onWrite, pathMatches, type OnWriteClient } from "./onWrite.js";

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

  close(code?: number, reason?: string): void {
    this.emit("close", { code: code ?? 1000, reason: reason ?? "closed" });
  }

  emit(type: string, event: any): void {
    for (const handler of this.listeners.get(type) ?? []) {
      handler(event);
    }
  }
}

function makeClient(): OnWriteClient {
  return {
    getEvents: vi.fn().mockResolvedValue({ events: [], nextCursor: null }),
    recordHandlerError: vi.fn().mockResolvedValue(undefined)
  } as unknown as OnWriteClient;
}

function emitFilesystemEvent(socket: MockWebSocket, path: string, type = "file.updated"): void {
  socket.emit("message", {
    data: JSON.stringify({
      eventId: `evt:${path}`,
      type,
      path,
      revision: "rev_1",
      timestamp: "2026-05-06T10:00:00Z",
      origin: "provider_sync"
    })
  });
}

async function flushPromises(): Promise<void> {
  for (let index = 0; index < 8; index += 1) {
    await Promise.resolve();
  }
}

describe("pathMatches", () => {
  it.each([
    ["/notion/pages/calls/*/transcript", "/notion/pages/calls/2026-05-08/transcript", true],
    ["/notion/pages/calls/*/transcript", "/notion/pages/calls/2026-05-08/notes/transcript", false],
    ["/linear/issues/**", "/linear/issues/PROJ-441/comments/c-1", true],
    ["/linear/issues/**", "/linear/issues", true],
    ["/github/repos/acme/api/pulls/*", "/github/repos/acme/api/pulls/42", true],
    ["/github/repos/acme/api/pulls/*", "/github/repos/acme/api/pulls/42/files", false]
  ])("matches %s against %s as %s", (pattern, path, expected) => {
    expect(pathMatches(pattern, path)).toBe(expected);
  });

  it("throws synchronously for invalid patterns", () => {
    expect(() => pathMatches("linear/issues/**", "/linear/issues/PROJ-1")).toThrow("start with");
    expect(() => pathMatches("/linear/**/comments", "/linear/PROJ-1/comments")).toThrow("trailing");
    expect(() => onWrite("/linear//issues/*", () => undefined, { client: makeClient(), workspaceId: "ws_acme" })).toThrow("empty");
  });
});

describe("onWrite", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("fans out matching events and ignores non-matching paths", async () => {
    const client = makeClient();
    const sockets: MockWebSocket[] = [];
    const calls: string[] = [];

    const unsubscribeTranscript = onWrite(
      "/notion/pages/calls/*/transcript",
      (event) => calls.push(`transcript:${event.path}:${event.operation}:${event.source}`),
      {
        client,
        workspaceId: "ws_acme",
        token: "tok_test",
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      }
    );
    const unsubscribeLinear = onWrite(
      "/linear/issues/**",
      (event) => calls.push(`linear:${event.path}`),
      { client, workspaceId: "ws_acme", token: "tok_test" }
    );
    const unsubscribePull = onWrite(
      "/github/repos/acme/api/pulls/*",
      (event) => calls.push(`pull:${event.path}`),
      { client, workspaceId: "ws_acme", token: "tok_test" }
    );

    expect(sockets).toHaveLength(1);
    expect(sockets[0]!.url).toBe("wss://api.relayfile.dev/v1/workspaces/ws_acme/fs/ws?token=tok_test");

    sockets[0]!.emit("open", {});
    emitFilesystemEvent(sockets[0]!, "/notion/pages/calls/call-1/transcript");
    emitFilesystemEvent(sockets[0]!, "/notion/pages/calls/call-1/notes/transcript");
    emitFilesystemEvent(sockets[0]!, "/linear/issues/PROJ-441/comments/c-1");
    emitFilesystemEvent(sockets[0]!, "/github/repos/acme/api/pulls/42", "file.created");

    await flushPromises();

    expect(calls).toEqual([
      "transcript:/notion/pages/calls/call-1/transcript:update:sync",
      "linear:/linear/issues/PROJ-441/comments/c-1",
      "pull:/github/repos/acme/api/pulls/42"
    ]);

    unsubscribeTranscript();
    unsubscribeLinear();
    unsubscribePull();
  });

  it("isolates handler errors and records them", async () => {
    const client = makeClient();
    const sockets: MockWebSocket[] = [];
    const calls: string[] = [];

    onWrite(
      "/linear/issues/**",
      () => {
        throw new Error("boom");
      },
      {
        client,
        workspaceId: "ws_acme",
        token: "tok_test",
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      }
    );
    onWrite("/linear/issues/**", (event) => calls.push(event.path), {
      client,
      workspaceId: "ws_acme",
      token: "tok_test"
    });

    emitFilesystemEvent(sockets[0]!, "/linear/issues/PROJ-1");
    await flushPromises();

    expect(calls).toEqual(["/linear/issues/PROJ-1"]);
    expect(client.recordHandlerError).toHaveBeenCalledWith(
      expect.objectContaining({
        pattern: "/linear/issues/**",
        path: "/linear/issues/PROJ-1",
        retryable: false
      })
    );
  });

  it("stamps the subscribed workspaceId onto emitted events", async () => {
    // Regression: RelayFileSync strips workspaceId from the FilesystemEvent
    // shape, so toWriteEvent used to fall back to "" and break handlers that
    // route by workspace. The dispatcher must thread the registered workspace
    // through to the WriteEvent it hands to user code.
    const client = makeClient();
    const sockets: MockWebSocket[] = [];
    const received: string[] = [];

    onWrite(
      "/notion/pages/calls/*/transcript",
      (event) => received.push(event.workspaceId),
      {
        client,
        workspaceId: "ws_acme",
        token: "tok_test",
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      }
    );

    emitFilesystemEvent(sockets[0]!, "/notion/pages/calls/call-1/transcript");
    await flushPromises();

    expect(received).toEqual(["ws_acme"]);
  });

  it("rejects a second registration on the same client with a different workspaceId", () => {
    const client = makeClient();
    const sockets: MockWebSocket[] = [];

    onWrite(
      "/notion/pages/calls/*/transcript",
      () => undefined,
      {
        client,
        workspaceId: "ws_acme",
        token: "tok_test",
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      }
    );

    expect(() =>
      onWrite("/linear/issues/**", () => undefined, {
        client,
        workspaceId: "ws_other",
        token: "tok_test"
      })
    ).toThrow(/same workspaceId/);

    // The original socket remains the only one — we did not silently attach a
    // ws_other registration to the ws_acme feed.
    expect(sockets).toHaveLength(1);
  });

  it("isolates dispatch when the customer recordHandlerError implementation rejects", async () => {
    const client = {
      getEvents: vi.fn().mockResolvedValue({ events: [], nextCursor: null }),
      recordHandlerError: vi.fn().mockRejectedValue(new Error("telemetry exploded"))
    } as unknown as OnWriteClient;
    const sockets: MockWebSocket[] = [];
    const survivedCalls: string[] = [];
    const consoleErrors: unknown[][] = [];
    const errorSpy = vi.spyOn(console, "error").mockImplementation((...args) => {
      consoleErrors.push(args);
    });

    onWrite(
      "/linear/issues/**",
      () => {
        throw new Error("user handler boom");
      },
      {
        client,
        workspaceId: "ws_acme",
        token: "tok_test",
        webSocketFactory: (url) => {
          const socket = new MockWebSocket(url);
          sockets.push(socket);
          return socket;
        }
      }
    );
    onWrite(
      "/linear/issues/**",
      (event) => survivedCalls.push(event.path),
      { client, workspaceId: "ws_acme", token: "tok_test" }
    );

    emitFilesystemEvent(sockets[0]!, "/linear/issues/PROJ-1");
    await flushPromises();

    // The reporter rejected, but the second handler still ran for the same path.
    expect(survivedCalls).toEqual(["/linear/issues/PROJ-1"]);
    // We logged both the reporter failure and the original handler error.
    const flat = consoleErrors.flat().map(String).join(" | ");
    expect(flat).toMatch(/reporter failed/);
    expect(flat).toMatch(/handler error/);
    errorSpy.mockRestore();
  });

  it("reconnects with the 1s then 2s backoff schedule", async () => {
    vi.useFakeTimers();
    const client = makeClient();
    const sockets: MockWebSocket[] = [];

    onWrite("/github/repos/acme/api/pulls/*", () => undefined, {
      client,
      workspaceId: "ws_acme",
      token: "tok_test",
      webSocketFactory: (url) => {
        const socket = new MockWebSocket(url);
        sockets.push(socket);
        return socket;
      }
    });

    sockets[0]!.emit("close", { code: 1006, reason: "dropped" });
    await vi.advanceTimersByTimeAsync(999);
    expect(sockets).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(sockets).toHaveLength(2);

    sockets[1]!.emit("close", { code: 1006, reason: "dropped again" });
    await vi.advanceTimersByTimeAsync(1999);
    expect(sockets).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(sockets).toHaveLength(3);

    vi.useRealTimers();
  });
});
