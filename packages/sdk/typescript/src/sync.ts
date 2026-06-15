import type { RelayFileClient } from "./client.js";
import type { EventOrigin, FilesystemEvent } from "./types.js";

/**
 * WebSocket auth source for {@link RelayFileSyncOptions.token}.
 *
 * The function form is preferred for production: it is re-invoked on every
 * (re)connect, so token rotation/refresh propagates without restarting the
 * sync. The plain string form is kept for backward compatibility and tests.
 */
export type RelayFileSyncTokenProvider = string | (() => string | undefined | Promise<string | undefined>);

export type RelayFileSyncState = "idle" | "connecting" | "open" | "polling" | "reconnecting" | "closed";
export type RelayFileSyncStart = "now" | "legacy";

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
  from?: RelayFileSyncStart;
  cursor?: string;
  paths?: string[];
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

function normalizeWebSocketPathFilters(paths: string[] | undefined): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const value of paths ?? []) {
    const path = typeof value === "string" ? value.trim() : "";
    if (!path) {
      continue;
    }
    const absolute = path.startsWith("/") ? path : `/${path}`;
    if (seen.has(absolute)) {
      continue;
    }
    seen.add(absolute);
    normalized.push(absolute);
  }
  return normalized;
}

function pathMatchesAnyFilter(filters: string[], path: string): boolean {
  if (filters.length === 0) {
    return true;
  }
  const pathSegments = normalizePathSegments(path);
  return filters.some((filter) => matchPathSegments(normalizePathSegments(filter), pathSegments));
}

function normalizePathSegments(path: string): string[] {
  const absolute = path.startsWith("/") ? path : `/${path}`;
  const trimmed = absolute.replace(/\/+$/, "");
  if (!trimmed) {
    return [];
  }
  return trimmed.split("/").filter(Boolean);
}

function matchPathSegments(pattern: string[], path: string[]): boolean {
  if (pattern.length > 0 && pattern[pattern.length - 1] === "**") {
    const prefix = pattern.slice(0, -1);
    return path.length >= prefix.length && prefix.every((segment, index) => segment === "*" || segment === path[index]);
  }
  if (pattern.length !== path.length) {
    return false;
  }
  return pattern.every((segment, index) => segment === "*" || segment === path[index]);
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
// When involuntary polling is active but the WebSocket transport is still
// viable (baseUrl present, reconnect enabled, not caller-forced), the poll
// loop yields after a backoff window so the SDK can re-probe the WS and climb
// back to push delivery. Backoff grows on repeated pre-open failures so we
// never tight-loop a handshake that keeps rejecting, yet always recover once
// the transient (cold DO, proxy blip, brief auth gap) clears. Without this a
// single pre-open failure latches the process into polling forever — fatal
// for long-running consumers like the factory daemon.
const DEFAULT_WS_REPROBE_MIN_DELAY_MS = 5000;
const DEFAULT_WS_REPROBE_MAX_DELAY_MS = 300000;
// Cap on the dedupe cache used in polling mode. Sized large enough that no
// realistic workspace burst can churn through it within one poll interval
// (the events API is itself capped at ~1000 per page), small enough to keep
// memory bounded across long-lived processes.
const POLLING_DEDUPE_CACHE_LIMIT = 2048;
// How long we wait after a WS `error` for the matching `close` to arrive
// before forcing a recovery. Some implementations (notably Node's built-in
// WebSocket on auth-rejected upgrades) emit `error` with no successor
// `close`; without this watchdog the dispatcher stalls indefinitely.
const ERROR_TO_CLOSE_GRACE_MS = 250;

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

// Exported for testing. `ErrorEvent` is a DOM/browser global that is not
// available on every Node 22 runtime (for example Node 22.22.1), so we
// duck-type the shape rather than referencing the global directly. Using
// `instanceof ErrorEvent` here would throw
// `ReferenceError: ErrorEvent is not defined` whenever the underlying
// WebSocket emits an error event under Node.
export function normalizeError(event: unknown): unknown {
  if (
    event !== null &&
    typeof event === "object" &&
    "error" in event &&
    (event as { error: unknown }).error instanceof Error
  ) {
    return (event as { error: Error }).error;
  }
  return event;
}

export class RelayFileSync {
  private readonly client: RelayFileClient;
  private readonly workspaceId: string;
  private readonly baseUrl?: string;
  // Resolved on every connect attempt (string form is wrapped into a constant
  // factory at construction time). `undefined` means "fall back to
  // client.getToken()" — same JWT the REST methods use.
  private readonly tokenProvider?: () => string | undefined | Promise<string | undefined>;
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
  private readonly from: RelayFileSyncStart;
  private readonly paths: string[];
  private readonly polledEventIds: Set<string> = new Set();
  private readonly polledEventOrder: string[] = [];
  private firstPollComplete = false;
  private socket?: RelayFileSyncSocket;
  private started = false;
  private stopped = false;
  private pollingPromise?: Promise<void>;
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private pingTimer?: ReturnType<typeof setInterval>;
  // See ERROR_TO_CLOSE_GRACE_MS: armed in the WS `error` handler, cleared
  // by the matching `close` (or by `stop()`); fires forceReconnect when
  // the OS layer fails to deliver a close after the error.
  private errorRecoveryTimer?: ReturnType<typeof setTimeout>;
  // Tracks whether this.socket has reached the OPEN state. Reset every
  // time we attach to a fresh socket; flipped true in the `open` handler.
  // The error watchdog checks this so that a pre-open failure (auth reject,
  // proxy RST during handshake, etc.) routes to polling rather than an
  // infinite reconnect loop against a server that won't accept the WS.
  private currentSocketHasOpened = false;
  // Tracks the last time *any* WebSocket frame was received (event, pong, or
  // otherwise). The watchdog uses this to detect silent socket death.
  private lastFrameAt = 0;
  // Tracks when the most recent ping was sent. We only enforce the pong
  // timeout once a ping has actually gone out; otherwise a quiet workspace
  // (no broadcasts and no pings yet) would falsely trip the watchdog.
  private lastPingSentAt = 0;
  private reconnectAttempts = 0;
  // Tracks how many times involuntary polling has yielded to re-probe the
  // WebSocket without a successful open in between. Drives re-probe backoff;
  // reset to 0 once a socket actually reaches OPEN.
  private pollingReprobeAttempts = 0;
  // Whether the *current* polling session should periodically yield to
  // re-probe the WS. False for explicit/caller-forced polling (preferPolling
  // or no baseUrl) — that polling is intentional and stays put.
  private pollingReprobeEnabled = false;
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
    this.from = options.from ?? "now";
    this.cursor = options.cursor;
    this.paths = normalizeWebSocketPathFilters(options.paths);
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
    this.clearErrorRecoveryTimer();
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
      // Most production providers return synchronously (JWT pulled from a
      // mutable cell that a refresh task updates in the background), so the
      // sync path is the common case.
      return this.tokenProvider();
    }
    // Auto-derive from the client's tokenProvider — the same JWT the REST
    // surface is using. Bug 1 fix: callers no longer have to thread
    // `token: await client.tokenProvider()` through every onWrite() call.
    // client.getToken is always async (returns a Promise), so we land on
    // the slow path here. That's fine: the WS open is async anyway.
    return this.client.getToken();
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
    if (this.cursor) {
      url.searchParams.set("cursor", this.cursor);
    } else if (this.from === "now") {
      url.searchParams.set("from", "now");
    }
    for (const path of this.paths) {
      url.searchParams.append("path", path);
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
    this.currentSocketHasOpened = false;

    socket.addEventListener("open", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      this.currentSocketHasOpened = true;
      this.reconnectAttempts = 0;
      // A real OPEN means the transport is healthy again — start the next
      // potential degradation from a fresh (short) re-probe backoff.
      this.pollingReprobeAttempts = 0;
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
      this.emit("error", normalizeError(event) as Error | Event);
      // Per WHATWG, an `error` should be followed by a `close` and the
      // close handler is the one that schedules reconnect / starts polling.
      // In practice though, some WebSocket implementations (notably Node's
      // built-in/undici WebSocket on auth-rejected upgrades, and some
      // proxies that abruptly RST after the upgrade) deliver `error` with
      // no successor `close`. The dispatcher would then sit silent forever
      // — exactly the failure mode in the cortical-demo bug report (WS
      // error fires, no close, no reconnect, no polling fallback).
      //
      // To recover, we arm a short grace timer here. If the close handler
      // for THIS socket runs before it fires (the well-behaved path), it
      // clears the timer. Otherwise the timer fires, sees the socket is
      // still current, and forces the same recovery path the close handler
      // would have taken (reconnect or polling). The grace window is
      // intentionally short — by the time `error` has fired, the socket
      // is already known-bad; we are only giving the OS layer a beat to
      // deliver its close.
      if (this.errorRecoveryTimer) {
        return;
      }
      this.errorRecoveryTimer = setTimeout(() => {
        this.errorRecoveryTimer = undefined;
        if (this.stopped || this.socket !== socket) {
          return;
        }
        // If the socket never reached OPEN, this is a handshake-stage
        // failure (auth rejection on upgrade, proxy RST, server cold
        // start). Reconnecting in a tight loop just retries the same
        // failing handshake. Drop straight into polling instead — that
        // path tolerates 401/403/timeout and surfaces the error to the
        // caller without burning the loop.
        const preOpen = !this.currentSocketHasOpened;
        debugLog("ws error with no successor close — forcing recovery", {
          workspaceId: this.workspaceId,
          preOpen
        });
        this.forceReconnect(socket, preOpen ? "ws-error-pre-open" : "ws-error-no-close", {
          preferPolling: preOpen
        });
      }, ERROR_TO_CLOSE_GRACE_MS);
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
      this.clearErrorRecoveryTimer();
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
    // Involuntary polling (anything but "explicit") should not be a one-way
    // door: if the WS transport is viable, the poll loop will yield after a
    // backoff window so the .finally() below can re-open the socket. Explicit
    // polling (preferPolling / no baseUrl) is intentional and stays put.
    this.pollingReprobeEnabled =
      reason !== "explicit" &&
      !!this.baseUrl &&
      this.reconnect.enabled &&
      !this.preferPolling;
    this.pollingPromise = this.pollLoop().finally(() => {
      this.pollingPromise = undefined;
      if (!this.stopped && !this.shouldUsePolling()) {
        this.scheduleReconnect();
      }
    });
  }

  private async pollLoop(): Promise<void> {
    let retryAttempt = 0;
    // When re-probing is enabled, fix a deadline at which this poll session
    // yields back to the WebSocket. Computed once per session from the running
    // attempt count so repeated failures back off (5s, 10s, 20s … capped 5m).
    // `undefined` => never yield (explicit polling, or WS not viable).
    const reprobeDeadlineAt = this.pollingReprobeEnabled
      ? Date.now() + this.computeReprobeDelayMs(this.pollingReprobeAttempts)
      : undefined;
    while (!this.stopped) {
      if (this.signal?.aborted) {
        throw createAbortError();
      }
      // Backoff window elapsed: exit the loop so startPolling()'s .finally
      // runs scheduleReconnect() → openWebSocket(). The cursor advanced by
      // this session carries into the WS URL, so the re-opened socket resumes
      // exactly where polling left off (no gap, no replay). If the re-open
      // fails pre-open again, forceReconnect → startPolling restarts this loop
      // with an incremented attempt and a longer window.
      if (reprobeDeadlineAt !== undefined && Date.now() >= reprobeDeadlineAt) {
        this.pollingReprobeAttempts += 1;
        debugLog("polling yielding to WS re-probe", {
          workspaceId: this.workspaceId,
          attempt: this.pollingReprobeAttempts,
        });
        return;
      }
      try {
        // The current server implementation paginates events from oldest to
        // newest. Empty cursor starts at index 0, and nextCursor advances
        // forward through history. To avoid replaying the whole event log on
        // startup while still preserving live forward progress, the first poll
        // drains to the tip and seeds `this.cursor` without emitting. Later
        // polls resume from that live cursor and emit only newly appended
        // events.
        let cursor = this.cursor;
        let latestCursor = cursor;
        const pending: FilesystemEvent[] = [];
        for (;;) {
          const response = await this.client.getEvents(this.workspaceId, {
            cursor,
            limit: 1000,
            signal: this.signal
          });
          const events = response.events ?? [];
          if (!this.firstPollComplete) {
            for (const event of events) {
              this.rememberPolledEvent(event.eventId);
            }
          } else {
            for (const event of events) {
              if (!event.eventId || this.polledEventIds.has(event.eventId)) {
                continue;
              }
              this.rememberPolledEvent(event.eventId);
              pending.push(event);
            }
          }
          const nextCursor = response.nextCursor || null;
          if (events.length > 0) {
            latestCursor = events[events.length - 1]?.eventId ?? latestCursor;
          }
          if (nextCursor) {
            latestCursor = nextCursor;
          }
          if (!nextCursor || nextCursor === cursor) {
            break;
          }
          cursor = nextCursor;
        }
        retryAttempt = 0;
        this.cursor = latestCursor;
        if (!this.firstPollComplete) {
          this.firstPollComplete = true;
        } else {
          for (const event of pending) {
            this.emitFilesystemEvent(event);
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

  private rememberPolledEvent(eventId: string | undefined): void {
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
    if (parsed.eventId) {
      this.cursor = parsed.eventId;
    }
    debugLog("event", { type: normalized.type, path: normalized.path, revision: normalized.revision });
    this.emitFilesystemEvent(normalized);
  }

  private emitFilesystemEvent(event: FilesystemEvent): void {
    if (!pathMatchesAnyFilter(this.paths, event.path)) {
      return;
    }
    this.emit("event", event);
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
  //
  // `options.preferPolling` lets the caller force the polling fallback even
  // when reconnect is enabled. Used by the error watchdog when the failed
  // socket never reached OPEN (handshake-stage failure) — retrying the WS
  // upgrade in a loop won't help when the server is rejecting at the upgrade.
  private forceReconnect(
    socket: RelayFileSyncSocket,
    reason: string,
    options?: { preferPolling?: boolean },
  ): void {
    this.clearPingTimer();
    this.clearErrorRecoveryTimer();
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
    if (!this.reconnect.enabled || options?.preferPolling) {
      this.startPolling(
        options?.preferPolling ? "forced-polling-pre-open" : "forced-reconnect-no-retry",
        reason,
      );
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

  // How long involuntary polling runs before yielding to re-probe the WS.
  // attempt 0 → 5s, 1 → 10s, 2 → 20s … capped at 5m. The first re-probe after
  // a fresh degradation is fast (transients usually clear within seconds); a
  // persistently rejecting handshake backs off so we never hammer it.
  private computeReprobeDelayMs(attempt: number): number {
    const uncapped = DEFAULT_WS_REPROBE_MIN_DELAY_MS * Math.pow(2, Math.max(0, attempt));
    return Math.min(DEFAULT_WS_REPROBE_MAX_DELAY_MS, uncapped);
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

  private clearErrorRecoveryTimer(): void {
    if (this.errorRecoveryTimer) {
      clearTimeout(this.errorRecoveryTimer);
      this.errorRecoveryTimer = undefined;
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
