import type { RelayFileClient } from "./client.js";
import type { EventOrigin, FilesystemEvent } from "./types.js";

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
  token?: string;
  cursor?: string;
  preferPolling?: boolean;
  pollIntervalMs?: number;
  pingIntervalMs?: number;
  reconnect?: boolean | RelayFileSyncReconnectOptions;
  signal?: AbortSignal;
  webSocketFactory?: (url: string) => RelayFileSyncSocket;
  onEvent?: (event: FilesystemEvent) => void;
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
  private readonly token?: string;
  private readonly pollIntervalMs: number;
  private readonly pingIntervalMs: number;
  private readonly reconnect: RelayFileSyncReconnectConfig;
  private readonly preferPolling: boolean;
  private readonly signal?: AbortSignal;
  private readonly webSocketFactory: (url: string) => RelayFileSyncSocket;
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
  private socket?: RelayFileSyncSocket;
  private started = false;
  private stopped = false;
  private pollingPromise?: Promise<void>;
  private reconnectTimer?: ReturnType<typeof setTimeout>;
  private pingTimer?: ReturnType<typeof setInterval>;
  private reconnectAttempts = 0;
  private readonly abortHandler?: () => void;

  constructor(options: RelayFileSyncOptions) {
    this.client = options.client;
    this.workspaceId = options.workspaceId;
    this.baseUrl = options.baseUrl?.replace(/\/+$/, "");
    this.token = options.token;
    this.cursor = options.cursor;
    this.pollIntervalMs = Math.max(1, Math.floor(options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS));
    this.pingIntervalMs = Math.max(1, Math.floor(options.pingIntervalMs ?? DEFAULT_PING_INTERVAL_MS));
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
      this.startPolling();
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
    return this.preferPolling || !this.baseUrl || !this.token;
  }

  private openWebSocket(isReconnect: boolean): void {
    if (this.stopped) {
      return;
    }

    const url = new URL(`${this.baseUrl}/v1/workspaces/${encodeURIComponent(this.workspaceId)}/fs/ws`);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("token", this.token!);

    this.setState(isReconnect ? "reconnecting" : "connecting");

    let socket: RelayFileSyncSocket;
    try {
      socket = this.webSocketFactory(url.toString());
    } catch (error) {
      this.emit("error", error instanceof Error ? error : new Error("Failed to create WebSocket connection."));
      this.startPolling();
      return;
    }

    this.socket = socket;

    socket.addEventListener("open", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      this.reconnectAttempts = 0;
      this.setState("open");
      this.startPingLoop(socket);
      this.emit("open", event);
    });

    socket.addEventListener("message", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      this.handleSocketMessage(event);
    });

    socket.addEventListener("error", (event) => {
      if (this.socket !== socket || this.stopped) {
        return;
      }
      this.emit("error", normalizeError(event));
    });

    socket.addEventListener("close", (event) => {
      if (this.socket === socket) {
        this.socket = undefined;
      }
      this.clearPingTimer();
      this.emit("close", event);
      if (this.stopped) {
        this.setState("closed");
        return;
      }
      if (!this.reconnect.enabled) {
        this.startPolling();
        return;
      }
      this.scheduleReconnect();
    });
  }

  private startPolling(): void {
    if (this.pollingPromise || this.stopped) {
      return;
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
        const response = await this.client.getEvents(this.workspaceId, {
          cursor: this.cursor,
          signal: this.signal
        });
        retryAttempt = 0;
        for (const event of response.events) {
          this.emit("event", event);
        }
        if (response.nextCursor) {
          this.cursor = response.nextCursor;
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
      this.emit("pong", {
        type: "pong",
        timestamp: parsed.timestamp ?? parsed.ts
      });
      return;
    }

    this.emit("event", normalizeFilesystemEvent(parsed));
  }

  private startPingLoop(socket: RelayFileSyncSocket): void {
    this.clearPingTimer();
    this.pingTimer = setInterval(() => {
      if (this.socket !== socket || this.stopped) {
        this.clearPingTimer();
        return;
      }
      try {
        socket.send(JSON.stringify({ type: "ping" }));
      } catch (error) {
        this.emit("error", error instanceof Error ? error : new Error("Failed to send WebSocket ping."));
      }
    }, this.pingIntervalMs);
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.reconnectTimer) {
      return;
    }
    this.reconnectAttempts += 1;
    this.setState("reconnecting");
    const delayMs = this.computeReconnectDelayMs(this.reconnectAttempts);
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
