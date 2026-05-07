import type { RelayFileClient } from "./client.js";
import type { EventOrigin, FilesystemEvent } from "./types.js";

/**
 * WebSocket auth source for {@link RelayFileSyncOptions.token}.
 *
 * The function form is preferred for production: it is re-invoked on every
 * (re)connect, so token rotation/refresh propagates without restarting the
 * sync. The plain string form is kept for backward compatibility and tests.
 */
export type RelayFileSyncTokenProvider = string | (() => string | Promise<string>);

export type RelayFileSyncState = "idle" | "connecting" | "open" | "polling" | "reconnecting" | "closed";

export interface RelayFileSyncPong {
  type: "pong";
  timestamp?: string;
}

export interface RelayFileSyncReconnectOptions {
  enabled?: boolean;
  minDelayMs?: number;
  maxDelayMs?: number;
}

export interface RelayFileSyncOptions {
  client: RelayFileClient;
  workspaceId: string;
  baseUrl?: string;
  /**
   * WebSocket auth token. Accepts either a literal string or a (sync/async)
   * factory. The factory form is re-invoked on every (re)connect so token
   * rotation propagates transparently.
   *
   * If omitted, sync resolves the token via `client.getToken()` on each
   * connect — i.e. the same JWT the REST methods are using. Callers should
   * normally NOT pass this and let it inherit from the client.
   */
  token?: RelayFileSyncTokenProvider;
  cursor?: string;
  preferPolling?: boolean;
  pollIntervalMs?: number;
  pingIntervalMs?: number;
  // How long to wait after sending a ping before declaring the socket "muted"
  // and forcing a reconnect. Defaults to 2x pingIntervalMs. Catches silent
  // socket death (NAT/LB idle drop, half-open TCP) where the OS never delivers
  // a close/error to the JS layer but frames have stopped arriving.
  pongTimeoutMs?: number;
  reconnect?: boolean | RelayFileSyncReconnectOptions;
  signal?: AbortSignal;
  webSocketFactory?: (url: string) => RelayFileSyncSocket;
  onEvent?: (event: FilesystemEvent) => void;
  /**
   * Notification hook invoked when the sync degrades to HTTP polling because
   * the WebSocket failed to open or was rejected by the server. Live events
   * will be delayed by `pollIntervalMs` while in this mode. Use this to
   * surface a UI banner or alert; the SDK also emits `console.warn` and an
   * `error` event regardless of whether this is wired.
   */
  onPollingFallback?: (info: { reason: string; cause?: unknown }) => void;
}

export interface RelayFileSyncSocket {
  addEventListener(type: "open", handler: (event: Event) => void): void;
  addEventListener(type: "message", handler: (event: MessageEvent) => void): void;
  addEventListener(type: "error", handler: (event: Event | ErrorEvent) => void): void;
  addEventListener(type: "close", handler: (event: CloseEvent) => void): void;
  close(code?: number, reason?: string): void;
  send(data: string): void;
}

type RelayFileSyncEventName = "event" | "error" | "state" | "open" | "close" | "pong";
type RelayFileSyncHandlerMap = {
  event: (event: FilesystemEvent) => void;
  error: (error: Event | Error) => void;
  state: (state: RelayFileSyncState) => void;
  open: (event: Event) => void;
  close: (event: CloseEvent) => void;
  pong: (event: RelayFileSyncPong) => void;
};

interface RelayFileSyncReconnectConfig {
  enabled: boolean;
  minDelayMs: number;
  maxDelayMs: number;
}

interface RelayFileSyncWireEvent {
  type: string;
  eventId?: string;
  path?: string;
  revision?: string;
  origin?: EventOrigin;
  provider?: string;
  correlationId?: string;
  timestamp?: string;
  ts?: string;
}

const DEFAULT_POLL_INTERVAL_MS = 5000;
const DEFAULT_PING_INTERVAL_MS = 30000;
const DEFAULT_RECONNECT_MIN_DELAY_MS = 250;
const DEFAULT_RECONNECT_MAX_DELAY_MS = 5000;
// Cap on the dedupe cache used in polling mode. Sized large enough that no
// realistic workspace burst can churn through it within one poll interval
// (the events API is itself capped at ~1000 per page), small enough to keep
// memory bounded across long-lived processes.
const POLLING_DEDUPE_CACHE_LIMIT = 2048;

function warnPollingFallback(reason: string, cause?: unknown): void {
  // Always-on warning (NOT gated by RELAYFILE_SDK_DEBUG) because silent
  // degradation to polling has historically masked real auth/connectivity
  // bugs for hours at a time. Customers running the SDK in a Node service
  // see the warning in their normal logs without any opt-in.
  if (typeof console === "undefined" || typeof console.warn !== "function") {
    return;
  }
  const detail = cause instanceof Error ? cause.message : cause !== undefined ? String(cause) : "";
  const suffix = detail ? ` (${reason}: ${detail})` : ` (${reason})`;
  console.warn(
    `[relayfile-sdk] WebSocket connect failed; falling back to HTTP polling. Live events will be delayed.${suffix}`
  );
}

const debugEnabled = ((): boolean => {
  try {
    const value = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.RELAYFILE_SDK_DEBUG;
    return value === "1" || value === "true";
  } catch {
    return false;
  }
})();

function debugLog(...args: unknown[]): void {
  if (!debugEnabled) {
    return;
  }
  if (typeof console !== "undefined" && typeof console.error === "function") {
    console.error("[relayfile-sdk:sync]", ...args);
  }
}

function normalizeReconnectOptions(
  reconnect: RelayFileSyncOptions["reconnect"]
): RelayFileSyncReconnectConfig {
  if (reconnect === false) {
    return {
      enabled: false,
      minDelayMs: DEFAULT_RECONNECT_MIN_DELAY_MS,
      maxDelayMs: DEFAULT_RECONNECT_MAX_DELAY_MS
    };
  }

  if (reconnect === true || reconnect === undefined) {
    return {
      enabled: true,
      minDelayMs: DEFAULT_RECONNECT_MIN_DELAY_MS,
      maxDelayMs: DEFAULT_RECONNECT_MAX_DELAY_MS
    };
  }

  const minDelayMs = Math.max(1, Math.floor(reconnect.minDelayMs ?? DEFAULT_RECONNECT_MIN_DELAY_MS));
  const maxDelayMs = Math.max(minDelayMs, Math.floor(reconnect.maxDelayMs ?? DEFAULT_RECONNECT_MAX_DELAY_MS));
  return {
    enabled: reconnect.enabled ?? true,
    minDelayMs,
    maxDelayMs
  };
}

function createAbortError(): Error {
  const err = new Error("The operation was aborted.");
  err.name = "AbortError";
  return err;
}

function buildEventId(message: RelayFileSyncWireEvent): string {
  return [
    "ws",
    message.type,
    message.path ?? "",
    message.revision ?? "",
    message.timestamp ?? message.ts ?? ""
  ].join(":");
}

function normalizeFilesystemEvent(message: RelayFileSyncWireEvent): FilesystemEvent {
  return {
    eventId: message.eventId ?? buildEventId(message),
    type: message.type as FilesystemEvent["type"],
    path: message.path ?? "",
    revision: message.revision ?? "",
    origin: message.origin,
    provider: message.provider,
    correlationId: message.correlationId,
    timestamp: message.timestamp ?? message.ts ?? new Date().toISOString()
  };
}

function normalizeError(event: Event | ErrorEvent): Event | Error {
  return event instanceof ErrorEvent && event.error instanceof Error ? event.error : event;
}

export class RelayFileSync {
  private readonly client: RelayFileClient;
  private readonly workspaceId: string;
  private readonly baseUrl?: string;
  // Resolved on every connect attempt (string form is wrapped into a constant
  // factory at construction time). `undefined` means "fall back to
  // client.getToken()" — same JWT the REST methods use.
  private readonly tokenProvider?: () => string | Promise<string>;
  private readonly pollIntervalMs: number;
  private readonly pingIntervalMs: number;
  private readonly pongTimeoutMs: number;
  private readonly reconnect: RelayFileSyncReconnectConfig;
  private readonly preferPolling: boolean;
  private readonly signal?: AbortSignal;
  private readonly webSocketFactory: (url: string) => RelayFileSyncSocket;
  private readonly onPollingFallback?: (info: { reason: string; cause?: unknown }) => void;
  private readonly handlers: {
    [K in RelayFileSyncEventName]: Set<RelayFileSyncHandlerMap[K]>;
  } = {
    event: new Set(),
    error: new Set(),
    state: new Set(),
    open: new Set(),
    close: new Set(),
    pong: new Set()
  };

  private state: RelayFileSyncState = "idle";
  private cursor?: string;
  // Forward-progress polling: deduplicate by eventId across polls. The events
  // endpoint's nextCursor walks BACKWARDS through history (older events) once
  // you start following it, so the legacy approach of "advance the cursor on
  // every page" walks the SDK off the live edge and never returns. We instead
  // re-query the latest events on every tick and skip ones we have already
  // delivered. See PR #98 for the empirical evidence (1624 stale events
  // delivered in 90s while no live events ever surfaced).
  private readonly polledEventIds: Set<string> = new Set();
  private readonly polledEventOrder: string[] = [];
  private firstPollComplete = false;
  private socket?: RelayFileSyncSocket;
  private started = false;
  private stopped = false;
  private pollingPromise?: Promise<void>;
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private pingTimer?: ReturnType<typeof setInterval>;
  // Tracks the last time *any* WebSocket frame was received (event, pong, or
  // otherwise). The watchdog uses this to detect silent socket death.
  private lastFrameAt = 0;
  // Tracks when the most recent ping was sent. We only enforce the pong
  // timeout once a ping has actually gone out; otherwise a quiet workspace
  // (no broadcasts and no pings yet) would falsely trip the watchdog.
  private lastPingSentAt = 0;
  private reconnectAttempts = 0;
  private readonly abortHandler?: () => void;

  constructor(options: RelayFileSyncOptions) {
    this.client = options.client;
    this.workspaceId = options.workspaceId;
    this.baseUrl = options.baseUrl?.replace(/\/+$/, "");
    // Normalize token into a factory. `undefined` is preserved so the open
    // path knows to fall back to `client.getToken()` (the recommended path —
    // the WebSocket then auto-uses the same JWT the REST API is using).
    if (options.token === undefined) {
      this.tokenProvider = undefined;
    } else if (typeof options.token === "function") {
      this.tokenProvider = options.token;
    } else {
      const literal = options.token;
      this.tokenProvider = () => literal;
    }
    this.cursor = options.cursor;
    this.onPollingFallback = options.onPollingFallback;
    this.pollIntervalMs = Math.max(1, Math.floor(options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS));
    this.pingIntervalMs = Math.max(1, Math.floor(options.pingIntervalMs ?? DEFAULT_PING_INTERVAL_MS));
    // Default the pong timeout to 2x ping interval so a single missed pong
    // does not cause a flap, but two consecutive misses force a reconnect.
    const defaultPongTimeoutMs = this.pingIntervalMs * 2;
    this.pongTimeoutMs = Math.max(1, Math.floor(options.pongTimeoutMs ?? defaultPongTimeoutMs));
    this.reconnect = normalizeReconnectOptions(options.reconnect);
    this.preferPolling = options.preferPolling ?? false;
    this.signal = options.signal;
    this.webSocketFactory =
      options.webSocketFactory ??
      ((url) => {
        if (typeof WebSocket !== "function") {
          throw new Error("WebSocket is not available in this environment.");
        }
        return new WebSocket(url) as unknown as RelayFileSyncSocket;
      });

    if (options.onEvent) {
      this.handlers.event.add(options.onEvent);
    }

    if (this.signal) {
      this.abortHandler = () => {
        this.stop();
      };
      if (this.signal.aborted) {
        this.stopped = true;
        this.state = "closed";
      } else {
        this.signal.addEventListener("abort", this.abortHandler, { once: true });
      }
    }
  }

  static connect(options: RelayFileSyncOptions): RelayFileSync {
    const sync = new RelayFileSync(options);
    sync.start();
    return sync;
  }

  getState(): RelayFileSyncState {
    return this.state;
  }

  start(): void {
    if (this.started || this.stopped) {
      return;
    }
    this.started = true;
    if (this.shouldUsePolling()) {
      // Caller explicitly opted into polling, or there's no baseUrl to
      // upgrade to wss. NOT a fallback — no warn here.
      this.startPolling("explicit");
      return;
    }
    this.openWebSocket(false);
  }

  async stop(): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.stopped = true;
    this.clearReconnectTimer();
    this.clearPingTimer();
    const socket = this.socket;
    this.socket = undefined;
    if (socket) {
      socket.close(1000, "client stopped");
    }
    if (this.abortHandler && this.signal) {
      this.signal.removeEventListener("abort", this.abortHandler);
    }
    if (this.pollingPromise) {
      await this.pollingPromise.catch(() => undefined);
    }
    this.setState("closed");
  }

  on<TEventName extends RelayFileSyncEventName>(
    event: TEventName,
    handler: RelayFileSyncHandlerMap[TEventName]
  ): () => void {
    this.handlers[event].add(handler);
    return () => {
      this.handlers[event].delete(handler);
    };
  }

  private shouldUsePolling(): boolean {
    // Token availability is no longer required up-front — the WS opener
    // resolves it on demand from either `options.token` (a literal/factory)
    // or `client.getToken()`. We only force polling when the caller asked
    // for it or there is no baseUrl to derive a wss URL from.
    return this.preferPolling || !this.baseUrl;
  }

  // Resolve a token for the WS upgrade. Returns either a string directly
  // (sync fast-path; preserves the synchronous "start() → factory called"
  // contract that pre-existed Bug 1's fix) or a Promise (async slow-path;
  // factory is invoked on the next microtask). `undefined` means "no token
  // available — the server may still accept the upgrade if it's configured
  // for unauthenticated mode in tests/local-dev".
  private resolveWsTokenMaybeSync(): string | undefined | Promise<string | undefined> {
    if (this.tokenProvider) {
      const result = this.tokenProvider();
      // Most production providers return synchronously (JWT pulled from a
      // mutable cell that a refresh task updates in the background), so the
      // sync path is the common case.
      return result as string | Promise<string>;
    }
    // Auto-derive from the client's tokenProvider — the same JWT the REST
    // surface is using. Bug 1 fix: callers no longer have to thread
    // `token: await client.tokenProvider()` through every onWrite() call.
    // client.getToken is always async (returns a Promise), so we land on
    // the slow path here. That's fine: the WS open is async anyway.
    const client = this.client as RelayFileClient & { getToken?: () => Promise<string> };
    if (typeof client.getToken === "function") {
      return client.getToken();
    }
    return undefined;
  }

  private openWebSocket(isReconnect: boolean): void {
    if (this.stopped) {
      return;
    }

    this.setState(isReconnect ? "reconnecting" : "connecting");

    // Resolve a fresh token on every (re)connect attempt. This is what
    // makes mid-session token rotation transparent: the watchdog/close
    // handler reconnects, we re-call the factory, and the new socket comes
    // up with the new JWT. (Bug 4: pre-fix, the token was captured once at
    // construction and a 4001/auth close on rotation triggered an infinite
    // reconnect loop with the stale token.)
    let resolved: string | undefined | Promise<string | undefined>;
    try {
      resolved = this.resolveWsTokenMaybeSync();
    } catch (error) {
      this.emit("error", error instanceof Error ? error : new Error("Failed to resolve WebSocket auth token."));
      this.startPolling("token-resolution-failed", error);
      return;
    }

    if (resolved && typeof (resolved as Promise<string>).then === "function") {
      // Async path. The factory call gets deferred to the next microtask.
      (resolved as Promise<string | undefined>).then(
        (token) => this.openWebSocketWithToken(token),
        (error) => {
          this.emit("error", error instanceof Error ? error : new Error("Failed to resolve WebSocket auth token."));
          this.startPolling("token-resolution-failed", error);
        }
      );
      return;
    }

    this.openWebSocketWithToken(resolved as string | undefined);
  }

  private openWebSocketWithToken(token: string | undefined): void {
    if (this.stopped) {
      return;
    }

    const url = new URL(`${this.baseUrl}/v1/workspaces/${encodeURIComponent(this.workspaceId)}/fs/ws`);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    // Pass token in query string — the server authenticates during the HTTP
    // upgrade handshake via r.URL.Query().Get("token").
    if (token) {
      url.searchParams.set("token", token);
    }

    let socket: RelayFileSyncSocket;
    try {
      socket = this.webSocketFactory(url.toString());
    } catch (error) {
      this.emit("error", error instanceof Error ? error : new Error("Failed to create WebSocket connection."));
      this.startPolling("ws-factory-threw", error);
      return;
    }

    this.socket = socket;

    socket.addEventListener("open", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      this.reconnectAttempts = 0;
      // Reset frame/ping bookkeeping for the freshly opened socket so the
      // watchdog has a clean baseline. lastPingSentAt=0 disables the pong
      // timeout until we actually send our first ping.
      this.lastFrameAt = Date.now();
      this.lastPingSentAt = 0;
      this.setState("open");
      this.startPingLoop(socket);
      debugLog("ws open", { workspaceId: this.workspaceId });
      this.emit("open", event);
    });

    socket.addEventListener("message", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      // Stamp lastFrameAt for *any* inbound frame so the watchdog sees both
      // application events and pongs as proof of life.
      this.lastFrameAt = Date.now();
      this.handleSocketMessage(event);
    });

    socket.addEventListener("error", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      debugLog("ws error", event);
      this.emit("error", normalizeError(event));
    });

    socket.addEventListener("close", (event) => {
      // forceReconnect (called from the watchdog or a failed ping send) nulls
      // out this.socket and opens a replacement before the OS-layer close
      // event for the old socket actually fires. If this handler treated a
      // stale close as authoritative it would (a) clearPingTimer() and kill
      // the new socket's heartbeat and (b) potentially scheduleReconnect()
      // a second time (the timer guard would skip it most of the time, but
      // not after the timer has already fired). Either way: don't touch
      // shared state when the socket we're attached to is no longer current.
      if (this.socket !== socket) {
        debugLog("ws close (stale, ignored)", {
          code: event?.code,
          reason: event?.reason,
        });
        return;
      }
      this.socket = undefined;
      this.clearPingTimer();
      debugLog("ws close", { code: event?.code, reason: event?.reason, stopped: this.stopped });
      this.emit("close", event);
      if (this.stopped) {
        this.setState("closed");
        return;
      }
      if (!this.reconnect.enabled) {
        this.startPolling("ws-closed-no-reconnect", { code: event?.code, reason: event?.reason });
        return;
      }
      this.scheduleReconnect();
    });
  }

  private startPolling(reason: string = "fallback", cause?: unknown): void {
    if (this.pollingPromise || this.stopped) {
      return;
    }
    // Always-on warning + structured callback whenever we drop into polling
    // *involuntarily*. `explicit` means the caller asked for it (preferPolling
    // or no baseUrl) and we stay quiet — anything else is a real degradation
    // signal that previously took hours to detect.
    if (reason !== "explicit") {
      warnPollingFallback(reason, cause);
      try {
        this.onPollingFallback?.({ reason, cause });
      } catch (error) {
        debugLog("onPollingFallback handler threw", error);
      }
    }
    this.setState("polling");
    this.pollingPromise = this.pollLoop().finally(() => {
      this.pollingPromise = undefined;
      if (!this.stopped && !this.shouldUsePolling()) {
        this.scheduleReconnect();
      }
    });
  }

  private async pollLoop(): Promise<void> {
    let retryAttempt = 0;
    while (!this.stopped) {
      if (this.signal?.aborted) {
        throw createAbortError();
      }
      try {
        // Forward-progress polling: do NOT pass a cursor. The events feed's
        // nextCursor walks backwards through history, so chaining cursors
        // marches the SDK away from the live edge until it never returns
        // to "now" (verified empirically: 1624 events delivered in 90s,
        // all rev <= 2960 in a workspace at rev_4729). Instead, we ask
        // for the latest page on every tick and dedupe by eventId. The
        // events API caps `limit` at 1000; matching that gives a wide
        // safety margin against bursts at the configured pollIntervalMs.
        const response = await this.client.getEvents(this.workspaceId, {
          limit: 1000,
          signal: this.signal
        });
        retryAttempt = 0;
        // First poll is treated as "history catch-up" — emit nothing,
        // just seed the dedupe cache. This avoids replaying every event in
        // the workspace to the handler at startup. Subsequent polls deliver
        // anything not in the cache, in chronological order.
        const events = response.events ?? [];
        if (!this.firstPollComplete) {
          for (const event of events) {
            this.rememberPolledEvent(event.eventId);
          }
          this.firstPollComplete = true;
        } else {
          // Sort newest-last so handlers see the natural revision order
          // regardless of which direction the API returns. Fall back to
          // input order when timestamps tie.
          const fresh = events
            .filter((event) => event.eventId && !this.polledEventIds.has(event.eventId))
            .slice()
            .sort((a, b) => {
              const ta = Date.parse(a.timestamp);
              const tb = Date.parse(b.timestamp);
              if (Number.isFinite(ta) && Number.isFinite(tb) && ta !== tb) {
                return ta - tb;
              }
              return 0;
            });
          for (const event of fresh) {
            this.rememberPolledEvent(event.eventId);
            this.emit("event", event);
          }
        }
        await this.sleep(this.pollIntervalMs);
      } catch (error) {
        if (this.isAbortError(error)) {
          return;
        }
        retryAttempt += 1;
        this.emit("error", error instanceof Error ? error : new Error("Polling failed."));
        const delayMs = this.computeReconnectDelayMs(retryAttempt);
        await this.sleep(delayMs);
      }
    }
  }

  private rememberPolledEvent(eventId: string): void {
    if (!eventId || this.polledEventIds.has(eventId)) {
      return;
    }
    this.polledEventIds.add(eventId);
    this.polledEventOrder.push(eventId);
    while (this.polledEventOrder.length > POLLING_DEDUPE_CACHE_LIMIT) {
      const evicted = this.polledEventOrder.shift();
      if (evicted) {
        this.polledEventIds.delete(evicted);
      }
    }
  }

  private handleSocketMessage(event: MessageEvent): void {
    if (typeof event.data !== "string") {
      return;
    }

    let parsed: RelayFileSyncWireEvent;
    try {
      parsed = JSON.parse(event.data) as RelayFileSyncWireEvent;
    } catch (error) {
      this.emit("error", error instanceof Error ? error : new Error("Failed to parse WebSocket payload."));
      return;
    }

    if (typeof parsed !== "object" || parsed === null || typeof parsed.type !== "string") {
      this.emit("error", new Error("Invalid WebSocket payload: missing required 'type' field."));
      return;
    }
    if (parsed.path !== undefined && typeof parsed.path !== "string") {
      this.emit("error", new Error("Invalid WebSocket payload: 'path' must be a string."));
      return;
    }
    if (parsed.revision !== undefined && typeof parsed.revision !== "string") {
      this.emit("error", new Error("Invalid WebSocket payload: 'revision' must be a string."));
      return;
    }
    if (parsed.timestamp !== undefined && typeof parsed.timestamp !== "string") {
      this.emit("error", new Error("Invalid WebSocket payload: 'timestamp' must be a string."));
      return;
    }
    if (parsed.ts !== undefined && typeof parsed.ts !== "string") {
      this.emit("error", new Error("Invalid WebSocket payload: 'ts' must be a string."));
      return;
    }

    if (parsed.type === "pong") {
      debugLog("pong", { timestamp: parsed.timestamp ?? parsed.ts });
      this.emit("pong", {
        type: "pong",
        timestamp: parsed.timestamp ?? parsed.ts
      });
      return;
    }

    const normalized = normalizeFilesystemEvent(parsed);
    debugLog("event", { type: normalized.type, path: normalized.path, revision: normalized.revision });
    this.emit("event", normalized);
  }

  private startPingLoop(socket: RelayFileSyncSocket): void {
    this.clearPingTimer();
    // The "ping loop" doubles as the heartbeat watchdog. The contract for
    // pongTimeoutMs is "how long to wait, after sending a ping, before
    // assuming the socket is dead." So the timeout window must be measured
    // from `lastPingSentAt` — the moment we sent the unanswered ping — not
    // from the last inbound frame. (CodeRabbit P1 on PR #93: with
    // pingIntervalMs=30_000 + pongTimeoutMs=45_000, measuring from
    // lastFrameAt would reconnect at t=60s — only 30s after the first
    // ping went out, which violates the documented "wait 45s after a
    // ping" semantics.)
    //
    // An unanswered ping = `lastPingSentAt > lastFrameAt`. Any inbound
    // frame (event OR pong) advances lastFrameAt past lastPingSentAt and
    // proves the socket is alive. While a ping is unanswered we DO NOT
    // pile up another — that would just race the timeout window and make
    // tuning meaningless.
    //
    // Load-bearing for the silent socket-death failure mode where the
    // server keeps broadcasting frames the JS layer never receives (e.g.
    // NAT/LB idle drop, half-open TCP) and neither `error` nor `close`
    // ever fires.
    this.pingTimer = setInterval(() => {
      if (this.socket !== socket || this.stopped) {
        this.clearPingTimer();
        return;
      }

      const now = Date.now();
      const hasOutstandingPing = this.lastPingSentAt > this.lastFrameAt;

      if (hasOutstandingPing) {
        const sincePing = now - this.lastPingSentAt;
        if (sincePing > this.pongTimeoutMs) {
          debugLog("watchdog tripped — forcing reconnect", {
            workspaceId: this.workspaceId,
            sincePingMs: sincePing,
            pongTimeoutMs: this.pongTimeoutMs
          });
          this.forceReconnect(socket, "heartbeat-timeout");
          return;
        }
        // Within the timeout — still waiting for the pong/frame. Don't
        // pile up a second ping while one is outstanding.
        return;
      }

      try {
        socket.send(JSON.stringify({ type: "ping" }));
        this.lastPingSentAt = now;
      } catch (error) {
        debugLog("ping send failed", error);
        this.emit("error", error instanceof Error ? error : new Error("Failed to send WebSocket ping."));
        // A failed send strongly implies the socket is dead. Force a
        // reconnect rather than waiting for the next tick to notice.
        this.forceReconnect(socket, "ping-send-failed");
      }
    }, this.pingIntervalMs);
  }

  // Tear down a socket that the watchdog or a failed send has decided is
  // dead. We close it (so any later async events from the OS layer become
  // no-ops via the `this.socket !== socket` guards) and trigger the standard
  // reconnect path. Catches errors from `close()` because some socket
  // implementations throw when closing an already-broken connection.
  private forceReconnect(socket: RelayFileSyncSocket, reason: string): void {
    this.clearPingTimer();
    if (this.socket === socket) {
      this.socket = undefined;
    }
    try {
      socket.close(4000, reason);
    } catch (error) {
      debugLog("socket.close threw during forceReconnect", error);
    }
    if (this.stopped) {
      this.setState("closed");
      return;
    }
    if (!this.reconnect.enabled) {
      this.startPolling("forced-reconnect-no-retry", reason);
      return;
    }
    this.scheduleReconnect();
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.reconnectTimer) {
      return;
    }
    this.reconnectAttempts += 1;
    this.setState("reconnecting");
    const delayMs = this.computeReconnectDelayMs(this.reconnectAttempts);
    debugLog("scheduling reconnect", { attempt: this.reconnectAttempts, delayMs });
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (this.stopped) {
        return;
      }
      if (this.pollingPromise) {
        return;
      }
      this.openWebSocket(true);
    }, delayMs);
  }

  private computeReconnectDelayMs(attempt: number): number {
    const uncapped = this.reconnect.minDelayMs * Math.pow(2, Math.max(0, attempt - 1));
    return Math.min(this.reconnect.maxDelayMs, uncapped);
  }

  private async sleep(delayMs: number): Promise<void> {
    if (delayMs <= 0) {
      return;
    }
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.signal?.removeEventListener("abort", onAbort);
        resolve();
      }, delayMs);
      const onAbort = () => {
        clearTimeout(timer);
        reject(createAbortError());
      };
      this.signal?.addEventListener("abort", onAbort, { once: true });
    });
  }

  private isAbortError(error: unknown): boolean {
    return this.signal?.aborted || (error instanceof Error && error.name === "AbortError");
  }

  private setState(state: RelayFileSyncState): void {
    if (this.state === state) {
      return;
    }
    this.state = state;
    this.emit("state", state);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
  }

  private clearPingTimer(): void {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = undefined;
    }
  }

  private emit<TEventName extends RelayFileSyncEventName>(
    event: TEventName,
    payload: Parameters<RelayFileSyncHandlerMap[TEventName]>[0]
  ): void {
    const handlers = this.handlers[event] as Set<(payload: unknown) => void>;
    for (const handler of handlers) {
      handler(payload);
    }
  }
}
