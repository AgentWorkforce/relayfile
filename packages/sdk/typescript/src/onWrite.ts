import type { WriteEvent, WriteEventOperation, WriteEventSource } from "@relayfile/core";

import { RelayFileClient, DEFAULT_RELAYFILE_BASE_URL } from "./client.js";
import { RelayFileSync, type RelayFileSyncSocket, type RelayFileSyncTokenProvider } from "./sync.js";
import type { FilesystemEvent } from "./types.js";

export type OnWriteHandler = (event: WriteEvent) => void | Promise<void>;

export interface OnWriteHandlerError {
  pattern: string;
  path: string;
  error: unknown;
  retryable: false;
}

export type OnWriteClient = RelayFileClient & {
  recordHandlerError?(error: OnWriteHandlerError): void | Promise<void>;
};

export interface OnWriteOptions {
  client?: OnWriteClient;
  workspaceId?: string;
  operations?: WriteEventOperation[];
  signal?: AbortSignal;
  baseUrl?: string;
  /**
   * Optional WebSocket auth override. Accepts the same `string | () =>
   * string | Promise<string>` shape as the underlying sync.
   *
   * If omitted, onWrite auto-derives WS auth from `client.getToken()` — the
   * same JWT the REST API is using — and re-resolves it on every reconnect
   * so token rotation propagates without restart. **Most callers should
   * leave this unset.** Passing a literal string is back-compat for older
   * code; passing a factory is the right shape for production.
   */
  token?: RelayFileSyncTokenProvider;
  webSocketFactory?: (url: string) => RelayFileSyncSocket;
  // Override the WebSocket ping/heartbeat cadence. Lower values catch silent
  // socket death faster at the cost of slightly more chatter; the default is
  // tuned for production (30s ping, 60s pong timeout).
  pingIntervalMs?: number;
  pongTimeoutMs?: number;
  /**
   * Notification hook fired when the underlying sync degrades to HTTP
   * polling because the WebSocket failed to open. Useful for surfacing a
   * "live updates paused" banner. The SDK also `console.warn`s and emits
   * an `error` regardless.
   */
  onPollingFallback?: (info: { reason: string; cause?: unknown }) => void;
}

interface OnWriteRegistration {
  id: number;
  pattern: string;
  operations: Set<WriteEventOperation>;
  handler: OnWriteHandler;
}

interface EnvironmentLike {
  process?: {
    env?: Record<string, string | undefined>;
  };
}

const DEFAULT_OPERATIONS: WriteEventOperation[] = ["create", "update"];
const DEFAULT_RECONNECT_MIN_DELAY_MS = 1000;
const DEFAULT_RECONNECT_MAX_DELAY_MS = 30000;
const dispatchers = new WeakMap<OnWriteClient, OnWriteDispatcher>();
let nextRegistrationId = 1;
let defaultClient: OnWriteClient | undefined;

const debugEnabled = ((): boolean => {
  try {
    const value = (globalThis as EnvironmentLike).process?.env?.RELAYFILE_SDK_DEBUG;
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
    console.error("[relayfile-sdk:onWrite]", ...args);
  }
}

export function pathMatches(pattern: string, path: string): boolean {
  const patternSegments = normalizePattern(pattern);
  const pathSegments = normalizePath(path);
  return matchSegments(patternSegments, pathSegments);
}

export function onWrite(
  pattern: string,
  handler: OnWriteHandler,
  options: OnWriteOptions = {}
): () => void {
  const normalizedPattern = `/${normalizePattern(pattern).join("/")}`;
  if (typeof handler !== "function") {
    throw new Error("onWrite handler must be a function.");
  }

  const client = options.client ?? getDefaultClient();
  const workspaceId = options.workspaceId ?? readEnv("RELAYFILE_WORKSPACE_ID");
  if (!workspaceId) {
    throw new Error("onWrite requires options.workspaceId or RELAYFILE_WORKSPACE_ID.");
  }
  const baseUrl = resolveOnWriteBaseUrl(options, client);

  const operations = new Set(options.operations ?? DEFAULT_OPERATIONS);
  for (const operation of operations) {
    if (operation !== "create" && operation !== "update" && operation !== "delete") {
      throw new Error(`Invalid onWrite operation: ${operation}`);
    }
  }

  // The dispatcher cache is keyed by client; a single shared WebSocket is
  // bound to one workspace. v1 scopes a client to a single workspace per the
  // design doc (Out-of-scope: "Cross-workspace subscriptions"). Reject
  // mismatched workspaceId rather than silently attaching to the wrong feed.
  let dispatcher = dispatchers.get(client);
  if (dispatcher && dispatcher.workspaceId !== workspaceId) {
    throw new Error(
      `onWrite registrations on the same client must use the same workspaceId. Existing="${dispatcher.workspaceId}", new="${workspaceId}". Construct a separate RelayFileClient per workspace.`
    );
  }
  if (!dispatcher) {
    dispatcher = new OnWriteDispatcher(client, workspaceId);
    dispatchers.set(client, dispatcher);
  }

  const registration: OnWriteRegistration = {
    id: nextRegistrationId++,
    pattern: normalizedPattern,
    operations,
    handler
  };

  dispatcher.register(registration, {
    workspaceId,
    signal: options.signal,
    baseUrl,
    token: options.token,
    webSocketFactory: options.webSocketFactory,
    pingIntervalMs: options.pingIntervalMs,
    pongTimeoutMs: options.pongTimeoutMs,
    onPollingFallback: options.onPollingFallback
  });

  debugLog("registered", { id: registration.id, pattern: normalizedPattern, workspaceId });

  return () => {
    dispatcher?.unregister(registration.id);
  };
}

class OnWriteDispatcher {
  private readonly client: OnWriteClient;
  // Captured at construction. RelayFileSync normalizes the FilesystemEvent
  // shape and does not surface workspaceId on emitted events, so we stamp the
  // subscribed workspaceId onto every WriteEvent we hand to user handlers.
  // It also gates registrations: see the cross-workspace check in onWrite().
  readonly workspaceId: string;
  private readonly registrations: OnWriteRegistration[] = [];
  private readonly patternChains = new Map<string, Promise<void>>();
  private sync?: RelayFileSync;

  constructor(client: OnWriteClient, workspaceId: string) {
    this.client = client;
    this.workspaceId = workspaceId;
  }

  register(
    registration: OnWriteRegistration,
    options: Required<Pick<OnWriteOptions, "workspaceId">> &
      Pick<OnWriteOptions, "signal" | "baseUrl" | "token" | "webSocketFactory" | "pingIntervalMs" | "pongTimeoutMs" | "onPollingFallback">
  ): void {
    this.registrations.push(registration);
    if (options.signal) {
      if (options.signal.aborted) {
        this.unregister(registration.id);
        return;
      }
      options.signal.addEventListener("abort", () => this.unregister(registration.id), { once: true });
    }
    this.ensureSync(options);
  }

  unregister(id: number): void {
    const index = this.registrations.findIndex((registration) => registration.id === id);
    if (index >= 0) {
      this.registrations.splice(index, 1);
    }
    if (this.registrations.length === 0 && this.sync) {
      void this.sync.stop();
      this.sync = undefined;
    }
  }

  private ensureSync(
    options: Required<Pick<OnWriteOptions, "workspaceId">> &
      Pick<OnWriteOptions, "baseUrl" | "token" | "webSocketFactory" | "pingIntervalMs" | "pongTimeoutMs" | "onPollingFallback">
  ): void {
    if (this.sync) {
      return;
    }

    // Token resolution order:
    //  1. options.token (literal or factory) — back-compat for callers that
    //     mint their own auth out-of-band.
    //  2. fall through to undefined → RelayFileSync auto-derives from
    //     `client.getToken()` on every (re)connect. This is the recommended
    //     path: it is the same JWT the REST surface is using AND it is
    //     re-resolved on every reconnect, so an expired/rotated token gets
    //     refreshed automatically by whatever provider the client wraps
    //     (e.g. WorkspaceHandle's getOrRefreshToken).
    //
    // Bug 5: previously this fell through to a `readEnv("RELAYFILE_TOKEN")`
    // literal when the caller omitted `options.token`. That env value is
    // captured once at workspace-join time (e.g. by
    // `WorkspaceHandle.mountEnv()`) and never refreshed, so it expires
    // ~1 hour later and the WS upgrade then fails with an auth error
    // (visible only as a JS-layer "ws error" with no successor close,
    // leaving the dispatcher stalled forever). The env literal also
    // shadowed the auto-derive fix from PR #96 whenever a caller happened
    // to have RELAYFILE_TOKEN set in their environment, which is the
    // common case for any workspace started via mountEnv. Auto-derive must
    // win over the env literal whenever a client is available.
    //
    // The default-client path (constructed by `getDefaultClient()` when
    // `options.client` is omitted) already wires RELAYFILE_TOKEN into the
    // client's tokenProvider, so callers that depend on the env-only flow
    // continue to work — `client.getToken()` returns the env value.
    const token: RelayFileSyncTokenProvider | undefined = options.token;

    this.sync = RelayFileSync.connect({
      client: this.client,
      workspaceId: options.workspaceId,
      baseUrl: options.baseUrl ?? DEFAULT_RELAYFILE_BASE_URL,
      token,
      reconnect: {
        minDelayMs: DEFAULT_RECONNECT_MIN_DELAY_MS,
        maxDelayMs: DEFAULT_RECONNECT_MAX_DELAY_MS
      },
      webSocketFactory: options.webSocketFactory,
      pingIntervalMs: options.pingIntervalMs,
      pongTimeoutMs: options.pongTimeoutMs,
      onPollingFallback: options.onPollingFallback,
      onEvent: (event) => {
        void this.dispatch(event);
      }
    });
  }

  private async dispatch(event: FilesystemEvent): Promise<void> {
    const writeEvent = toWriteEvent(event, this.workspaceId);
    if (!writeEvent) {
      return;
    }

    for (const registration of [...this.registrations]) {
      if (!registration.operations.has(writeEvent.operation) || !pathMatches(registration.pattern, writeEvent.path)) {
        continue;
      }

      debugLog("dispatch", {
        registrationId: registration.id,
        pattern: registration.pattern,
        path: writeEvent.path,
        operation: writeEvent.operation
      });

      // patternChains serializes handlers per pattern. runHandler is the only
      // thing chained here and it already swallows handler errors, so the
      // chain itself can never reject — but we still defensively `.catch`
      // before chaining so a future refactor of runHandler cannot silently
      // break the chain (which is the failure mode hypothesis 3 in the bug
      // report).
      const previous = this.patternChains.get(registration.pattern) ?? Promise.resolve();
      const next = previous
        .catch(() => undefined)
        .then(() => this.runHandler(registration, writeEvent))
        .catch((error) => {
          // Belt-and-braces: should be unreachable because runHandler catches.
          debugLog("patternChain unexpectedly rejected", { pattern: registration.pattern, error });
        });
      this.patternChains.set(registration.pattern, next);
      void next.finally(() => {
        if (this.patternChains.get(registration.pattern) === next) {
          this.patternChains.delete(registration.pattern);
        }
      });
    }
  }

  private async runHandler(registration: OnWriteRegistration, event: WriteEvent): Promise<void> {
    try {
      await registration.handler(event);
    } catch (error) {
      await this.recordHandlerError({
        pattern: registration.pattern,
        path: event.path,
        error,
        retryable: false
      });
    }
  }

  // The "handler errors do not propagate" guarantee covers the recorder too:
  // if the customer's recordHandlerError implementation throws or rejects, fall
  // back to console.error rather than letting the rejection bubble up the
  // dispatch chain (which would skip subsequent handlers for the same pattern).
  private async recordHandlerError(error: OnWriteHandlerError): Promise<void> {
    if (typeof this.client.recordHandlerError === "function") {
      try {
        await this.client.recordHandlerError(error);
        return;
      } catch (reportingError) {
        if (typeof console !== "undefined" && typeof console.error === "function") {
          console.error("Relayfile onWrite handler-error reporter failed", reportingError);
        }
      }
    }
    if (typeof console !== "undefined" && typeof console.error === "function") {
      console.error("Relayfile onWrite handler error", error);
    }
  }
}

function normalizePattern(pattern: string): string[] {
  if (typeof pattern !== "string" || pattern.length === 0) {
    throw new Error("onWrite pattern must be a non-empty string.");
  }
  if (!pattern.startsWith("/")) {
    throw new Error("onWrite pattern must start with '/'.");
  }
  if (pattern.includes("//")) {
    throw new Error("onWrite pattern cannot contain empty path segments.");
  }
  const segments = normalizePath(pattern);
  const recursiveIndex = segments.indexOf("**");
  if (recursiveIndex >= 0 && recursiveIndex !== segments.length - 1) {
    throw new Error("onWrite pattern only supports '**' as the trailing segment.");
  }
  return segments;
}

function normalizePath(path: string): string[] {
  if (!path.startsWith("/")) {
    return normalizePath(`/${path}`);
  }
  const trimmed = path.replace(/\/+$/, "");
  if (trimmed === "") {
    return [];
  }
  return trimmed.split("/").filter(Boolean);
}

// Trailing `**` matches **zero or more** trailing segments — same as gitignore
// and standard glob conventions, and what the design doc specifies ("any
// number of segments"). `/linear/issues/**` therefore matches both
// `/linear/issues` (the collection root) and `/linear/issues/PROJ-1/comments`.
// `*` matches exactly one segment; `**` is only valid as the last segment.
function matchSegments(pattern: string[], path: string[]): boolean {
  if (pattern.length > 0 && pattern[pattern.length - 1] === "**") {
    const prefix = pattern.slice(0, -1);
    return path.length >= prefix.length && prefix.every((segment, index) => segment === "*" || segment === path[index]);
  }

  if (pattern.length !== path.length) {
    return false;
  }
  return pattern.every((segment, index) => segment === "*" || segment === path[index]);
}

function toWriteEvent(event: FilesystemEvent, workspaceId?: string): WriteEvent | null {
  const operation = operationFromEventType(event.type);
  if (!operation) {
    return null;
  }
  // RelayFileSync currently surfaces only:
  //   eventId, type, path, revision, origin, provider, correlationId, timestamp
  // Fields the wider WriteEvent contract advertises but the wire format does
  // not yet preserve — previousRevision, value, actor — are intentionally
  // omitted/null here. Wiring them through the wire format and
  // normalizeFilesystemEvent is a follow-up; v1 callers should treat
  // previousRevision/value/actor as not-yet-populated.
  return {
    workspaceId: workspaceId ?? "",
    path: event.path,
    operation,
    revision: event.revision,
    previousRevision: null,
    timestamp: event.timestamp,
    source: sourceFromOrigin(event.origin)
  };
}

function operationFromEventType(type: FilesystemEvent["type"]): WriteEventOperation | null {
  if (type === "file.created") {
    return "create";
  }
  if (type === "file.updated") {
    return "update";
  }
  if (type === "file.deleted") {
    return "delete";
  }
  return null;
}

function sourceFromOrigin(origin: FilesystemEvent["origin"]): WriteEventSource {
  if (origin === "agent_write") {
    return "agent";
  }
  if (origin === "provider_sync") {
    return "sync";
  }
  return "api";
}

function getDefaultClient(): OnWriteClient {
  if (!defaultClient) {
    const token = readEnv("RELAYFILE_TOKEN");
    if (!token) {
      throw new Error("onWrite requires options.client or RELAYFILE_TOKEN.");
    }
    defaultClient = new RelayFileClient({
      baseUrl: readEnv("RELAYFILE_BASE_URL") ?? DEFAULT_RELAYFILE_BASE_URL,
      token
    }) as OnWriteClient;
  }
  return defaultClient;
}

function resolveOnWriteBaseUrl(options: OnWriteOptions, client: OnWriteClient): string {
  if (options.baseUrl) {
    return options.baseUrl;
  }
  if (!options.client) {
    return readEnv("RELAYFILE_BASE_URL") ?? DEFAULT_RELAYFILE_BASE_URL;
  }
  if (client instanceof RelayFileClient) {
    return client.getBaseUrl();
  }
  return DEFAULT_RELAYFILE_BASE_URL;
}

function readEnv(name: string): string | undefined {
  return (globalThis as EnvironmentLike).process?.env?.[name];
}
