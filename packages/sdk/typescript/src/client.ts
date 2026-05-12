import {
  type AdminIngressStatusResponse,
  type AdminSyncStatusResponse,
  type BulkWriteInput,
  type BulkWriteResponse,
  type BackendStatusResponse,
  type AckResponse,
  type CommitForkInput,
  type CommitForkResponse,
  type CreateForkInput,
  type DeleteFileInput,
  type DeadLetterItem,
  type DeadLetterFeedResponse,
  type DiscardForkInput,
  type ErrorResponse,
  type EventFeedResponse,
  type ExportJsonResponse,
  type ExportOptions,
  type FileQueryResponse,
  type FileReadResponse,
  type FilesystemEvent,
  type GetEventsOptions,
  type GetAdminIngressStatusOptions,
  type GetAdminSyncStatusOptions,
  type GetOperationsOptions,
  type GetSyncDeadLettersOptions,
  type GetSyncIngressStatusOptions,
  type GetSyncStatusOptions,
  type ListTreeOptions,
  type OperationFeedResponse,
  type OperationStatusResponse,
  type QueuedResponse,
  type ResourceAtEventResult,
  type ReadFileInput,
  type QueryFilesOptions,
  type Subscription,
  type SyncIngressStatusResponse,
  type SyncStatusResponse,
  type TreeResponse,
  type WriteFileInput,
  type WriteQueuedResponse,
  type IngestWebhookInput,
  type WritebackItem,
  type AckWritebackInput,
  type AckWritebackResponse,
  type ChangeEvent,
  type ChangeLogQueryResult,
  type ChangeStreamConnection,
  type ChangeStreamConnectionOptions,
  type Expansion,
  type ExpansionLevel,
  type SubscribeOptions
} from "./types.js";
import type { ForkHandle } from "@relayfile/core";
import { RelayFileSync } from "./sync.js";
import {
  InvalidStateError,
  PayloadTooLargeError,
  QueueFullError,
  RelayFileApiError,
  RevisionConflictError
} from "./errors.js";

/**
 * Bearer token or token factory used for Relayfile API requests.
 *
 * When you mint JWTs for Relayfile, the server expects claims like:
 * `{ workspace_id: "ws_123", agent_name: "review-bot", aud: ["relayfile"] }`
 */
export type AccessTokenProvider = string | (() => string | Promise<string>);

export interface RelayFileRetryOptions {
  maxRetries?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  jitterRatio?: number;
}

/** Default base URL for the hosted Relayfile API */
export const DEFAULT_RELAYFILE_BASE_URL = "https://api.relayfile.dev";

export interface RelayFileClientOptions {
  /** API base URL. Defaults to https://api.relayfile.dev */
  baseUrl?: string;
  /**
   * Bearer token or token factory for SDK requests.
   *
   * Relayfile-authenticated JWTs should include `workspace_id`, `agent_name`,
   * and `aud` containing `relayfile`.
   */
  token: AccessTokenProvider;
  fetchImpl?: typeof fetch;
  userAgent?: string;
  retry?: RelayFileRetryOptions;
}

interface NormalizedRetryOptions {
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
  jitterRatio: number;
}

interface ExportJsonApiResponseShape {
  files?: FileReadResponse[];
}

type WebSocketEventName = "event" | "error" | "open" | "close";
type WebSocketHandlerMap = {
  event: (event: FilesystemEvent) => void;
  error: (event: Event | Error) => void;
  open: (event: Event) => void;
  close: (event: CloseEvent) => void;
};

export interface WebSocketConnection {
  close(code?: number, reason?: string): void;
  on<TEventName extends WebSocketEventName>(event: TEventName, handler: WebSocketHandlerMap[TEventName]): () => void;
}

export interface ConnectWebSocketOptions {
  token?: string;
  onEvent?: (event: FilesystemEvent) => void;
}

const DEFAULT_RETRY_OPTIONS: NormalizedRetryOptions = {
  maxRetries: 3,
  baseDelayMs: 100,
  maxDelayMs: 2000,
  jitterRatio: 0.2
};

const DEFAULT_CHANGE_COALESCE_MS = 200;
const DEFAULT_CHANGE_LOG_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const DEFAULT_CHANGE_LOG_MAX_ENTRIES = 10_000;
const CLIENT_TOKEN_STREAM_KEY = "__client__";

type JsonObject = Record<string, unknown>;

interface JwtClaimsShape {
  workspace_id?: unknown;
  agent_name?: unknown;
}

interface ChangeEventWireShape {
  id: string;
  workspace: string;
  agentId?: string;
  type: "relayfile.changed";
  occurredAt: string;
  resource: ChangeEvent["resource"];
  summary: ChangeEvent["summary"];
  digest?: string;
}

interface CachedChangeRecord {
  wire: ChangeEventWireShape;
  resource: ResourceAtEventResult;
  storedAt: number;
}

const changeStreamManagers = new WeakMap<RelayFileClient, Map<string, RelayFileChangeStreamManager>>();
const changeLogCaches = new WeakMap<RelayFileClient, Map<string, WorkspaceChangeLogCache>>();

function createM2NotImplementedError(feature: string): Error & { code: "M2_NOT_IMPLEMENTED" } {
  const error = new Error(`M2_NOT_IMPLEMENTED: ${feature} is reserved for proactive runtime M2.`) as Error & {
    code: "M2_NOT_IMPLEMENTED";
  };
  error.name = "M2NotImplementedError";
  error.code = "M2_NOT_IMPLEMENTED";
  return error;
}

function normalizeRetryOptions(options?: RelayFileRetryOptions): NormalizedRetryOptions {
  const maxRetries = options?.maxRetries ?? DEFAULT_RETRY_OPTIONS.maxRetries;
  const baseDelayMs = options?.baseDelayMs ?? DEFAULT_RETRY_OPTIONS.baseDelayMs;
  const maxDelayMs = options?.maxDelayMs ?? DEFAULT_RETRY_OPTIONS.maxDelayMs;
  const jitterRatio = options?.jitterRatio ?? DEFAULT_RETRY_OPTIONS.jitterRatio;
  return {
    maxRetries: Math.max(0, Math.floor(maxRetries)),
    baseDelayMs: Math.max(1, Math.floor(baseDelayMs)),
    maxDelayMs: Math.max(1, Math.floor(maxDelayMs)),
    jitterRatio: Math.max(0, Math.min(1, jitterRatio))
  };
}

function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) {
      query.set(key, String(value));
    }
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function generateCorrelationId(): string {
  return `rf_${crypto.randomUUID()}`;
}

function getHeaderValue(headers: Record<string, string> | undefined, name: string): string | undefined {
  if (!headers) {
    return undefined;
  }
  const target = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === target) {
      return value;
    }
  }
  return undefined;
}

async function resolveToken(tokenProvider: AccessTokenProvider): Promise<string> {
  if (typeof tokenProvider === "function") {
    return tokenProvider();
  }
  return tokenProvider;
}

function resolveSyncToken(tokenProvider: AccessTokenProvider): string {
  if (typeof tokenProvider === "function") {
    const token = tokenProvider();
    if (typeof token !== "string") {
      throw new Error("connectWebSocket requires a synchronous token provider or an explicit token option.");
    }
    return token;
  }
  return tokenProvider;
}

function createAbortError(): Error {
  const err = new Error("The operation was aborted.");
  err.name = "AbortError";
  return err;
}

function createBlobFromResponse(response: Response): Promise<Blob> {
  if (typeof response.blob === "function") {
    return response.blob();
  }
  return response.arrayBuffer().then((buffer) => new Blob([buffer], { type: response.headers.get("content-type") ?? undefined }));
}

function normalizeExportJsonResponse(payload: unknown): ExportJsonResponse {
  if (Array.isArray(payload)) {
    return { files: payload as FileReadResponse[] };
  }
  const data = (payload ?? {}) as ExportJsonApiResponseShape;
  return {
    files: Array.isArray(data.files) ? data.files : []
  };
}

function normalizeErrorDetails(data: Record<string, unknown>, explicitDetails: unknown): Record<string, unknown> | undefined {
  if (explicitDetails && typeof explicitDetails === "object" && !Array.isArray(explicitDetails)) {
    return explicitDetails as Record<string, unknown>;
  }
  const details: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(data)) {
    if (key !== "code" && key !== "message" && key !== "correlationId") {
      details[key] = value;
    }
  }
  return Object.keys(details).length > 0 ? details : undefined;
}

class RelayFileWebSocketConnection implements WebSocketConnection {
  private readonly socket: WebSocket;
  private readonly handlers: {
    [K in WebSocketEventName]: Set<WebSocketHandlerMap[K]>;
  } = {
    event: new Set(),
    error: new Set(),
    open: new Set(),
    close: new Set()
  };

  constructor(socket: WebSocket, onEvent?: (event: FilesystemEvent) => void) {
    this.socket = socket;
    if (onEvent) {
      this.handlers.event.add(onEvent);
    }

    socket.addEventListener("open", (event) => {
      for (const handler of this.handlers.open) {
        handler(event);
      }
    });

    socket.addEventListener("close", (event) => {
      for (const handler of this.handlers.close) {
        handler(event);
      }
    });

    socket.addEventListener("error", (event) => {
      const errorEvent = event instanceof ErrorEvent && event.error instanceof Error ? event.error : event;
      for (const handler of this.handlers.error) {
        handler(errorEvent);
      }
    });

    socket.addEventListener("message", (event) => {
      if (typeof event.data !== "string") {
        return;
      }
      let parsed: FilesystemEvent;
      try {
        const raw = JSON.parse(event.data);
        if (typeof raw !== "object" || raw === null || typeof raw.type !== "string") {
          throw new Error("Invalid WebSocket event: missing required 'type' field.");
        }
        if (raw.path !== undefined && typeof raw.path !== "string") {
          throw new Error("Invalid WebSocket event: 'path' must be a string.");
        }
        parsed = raw as FilesystemEvent;
      } catch (error) {
        const parseError = error instanceof Error ? error : new Error("Failed to parse WebSocket event payload.");
        for (const handler of this.handlers.error) {
          handler(parseError);
        }
        return;
      }
      for (const handler of this.handlers.event) {
        handler(parsed);
      }
    });
  }

  close(code?: number, reason?: string): void {
    this.socket.close(code, reason);
  }

  on<TEventName extends WebSocketEventName>(event: TEventName, handler: WebSocketHandlerMap[TEventName]): () => void {
    this.handlers[event].add(handler);
    return () => {
      this.handlers[event].delete(handler);
    };
  }
}

class WorkspaceChangeLogCache {
  private readonly entries: CachedChangeRecord[] = [];
  private readonly byId = new Map<string, CachedChangeRecord>();

  constructor(
    private readonly retentionMs = DEFAULT_CHANGE_LOG_RETENTION_MS,
    private readonly maxEntries = DEFAULT_CHANGE_LOG_MAX_ENTRIES
  ) {}

  record(record: CachedChangeRecord): void {
    this.prune(Date.now());
    const existing = this.byId.get(record.wire.id);
    if (existing) {
      this.byId.set(record.wire.id, record);
      const index = this.entries.findIndex((entry) => entry.wire.id === record.wire.id);
      if (index >= 0) {
        this.entries[index] = record;
      }
      return;
    }
    this.entries.push(record);
    this.byId.set(record.wire.id, record);
    while (this.entries.length > this.maxEntries) {
      const removed = this.entries.shift();
      if (removed) {
        this.byId.delete(removed.wire.id);
      }
    }
  }

  get(eventId: string): CachedChangeRecord | undefined {
    this.prune(Date.now());
    return this.byId.get(eventId);
  }

  listSince(isoTimestamp: string): CachedChangeRecord[] {
    this.prune(Date.now());
    const threshold = Date.parse(isoTimestamp);
    if (!Number.isFinite(threshold)) {
      throw new Error(`Invalid ISO timestamp: ${isoTimestamp}`);
    }
    return this.entries.filter((entry) => Date.parse(entry.wire.occurredAt) >= threshold);
  }

  listLastN(limit: number): CachedChangeRecord[] {
    this.prune(Date.now());
    const safeLimit = Math.max(0, Math.floor(limit));
    if (safeLimit === 0) {
      return [];
    }
    return this.entries.slice(-safeLimit);
  }

  private prune(now: number): void {
    while (this.entries.length > 0) {
      const first = this.entries[0];
      if (!first) {
        return;
      }
      if (now - first.storedAt < this.retentionMs) {
        return;
      }
      this.entries.shift();
      this.byId.delete(first.wire.id);
    }
  }
}

class RelayFileChangeSubscription {
  private active = true;
  private readonly globPatterns: string[][];
  private readonly pathScopes: string[][] | null;
  private readonly inFlight = new Set<Promise<void>>();
  private readonly pendingByPath = new Map<
    string,
    { event: FilesystemEvent; timer: ReturnType<typeof setTimeout> }
  >();
  private readonly coalesceMs: number;
  private readonly shouldCoalesce: boolean;

  constructor(
    private readonly manager: RelayFileChangeStreamManager,
    globs: string[],
    private readonly onChange: (event: ChangeEvent) => void,
    private readonly options?: SubscribeOptions
  ) {
    this.globPatterns = globs.map((pattern) => normalizeChangePattern(pattern));
    this.pathScopes = options?.pathScope?.length ? options.pathScope.map((pattern) => normalizeChangePattern(pattern)) : null;
    this.shouldCoalesce = (options?.coalesce ?? "fire-once") !== "none";
    this.coalesceMs = Math.max(0, Math.floor(options?.coalesceMs ?? DEFAULT_CHANGE_COALESCE_MS));
  }

  push(event: FilesystemEvent): void {
    if (!this.active || !shouldPublishFilesystemEvent(event) || !this.matches(event.path)) {
      return;
    }
    if (!this.shouldCoalesce) {
      this.dispatch(event);
      return;
    }
    const existing = this.pendingByPath.get(event.path);
    if (existing) {
      clearTimeout(existing.timer);
    }
    const timer = setTimeout(() => {
      this.pendingByPath.delete(event.path);
      if (!this.active) {
        return;
      }
      this.dispatch(event);
    }, this.coalesceMs);
    this.pendingByPath.set(event.path, { event, timer });
  }

  async close(): Promise<void> {
    this.active = false;
    for (const pending of this.pendingByPath.values()) {
      clearTimeout(pending.timer);
    }
    this.pendingByPath.clear();

    const drain = Promise.allSettled(Array.from(this.inFlight));
    const drainMs = this.options?.drainMs;
    if (typeof drainMs === "number" && Number.isFinite(drainMs) && drainMs >= 0) {
      await Promise.race([
        drain,
        new Promise<void>((resolve) => {
          setTimeout(resolve, drainMs);
        })
      ]);
      return;
    }
    await drain;
  }

  private matches(path: string): boolean {
    const pathSegments = normalizeChangePath(path);
    const matchesGlob = this.globPatterns.some((pattern) => matchChangeSegments(pattern, pathSegments));
    if (!matchesGlob) {
      return false;
    }
    if (!this.pathScopes) {
      return true;
    }
    return this.pathScopes.some((pattern) => matchChangeSegments(pattern, pathSegments));
  }

  private dispatch(event: FilesystemEvent): void {
    const task = this.manager.materialize(event)
      .then((changeEvent) => {
        if (!this.active || !changeEvent) {
          return;
        }
        return Promise.resolve(this.onChange(changeEvent));
      })
      .catch((error) => {
        if (typeof console !== "undefined" && typeof console.error === "function") {
          console.error("RelayFile subscribe handler failed", error);
        }
      })
      .finally(() => {
        this.inFlight.delete(task);
      });
    this.inFlight.add(task);
  }
}

class RelayFileChangeStreamManager {
  private readonly subscriptions = new Set<RelayFileChangeSubscription>();
  private openHandleCount = 0;
  private sync?: RelayFileSync;
  private readyResolved = false;
  private readonly readyInternal: Promise<void>;
  private resolveReady!: () => void;
  private rejectReady!: (reason?: unknown) => void;

  constructor(
    private readonly client: RelayFileClient,
    private readonly workspaceId: string,
    private readonly token: string | undefined,
    private readonly baseUrl: string
  ) {
    this.readyInternal = new Promise<void>((resolve, reject) => {
      this.resolveReady = resolve;
      this.rejectReady = reject;
    });
  }

  get ready(): Promise<void> {
    return this.readyInternal;
  }

  addSubscription(globs: string[], onChange: (event: ChangeEvent) => void, options?: SubscribeOptions): Subscription {
    const subscription = new RelayFileChangeSubscription(this, globs, onChange, options);
    this.subscriptions.add(subscription);
    this.ensureStarted();
    return {
      unsubscribe: async () => {
        this.subscriptions.delete(subscription);
        await subscription.close();
        await this.maybeStop();
      }
    };
  }

  open(): ChangeStreamConnection {
    this.openHandleCount += 1;
    this.ensureStarted();
    return {
      ready: this.ready,
      unsubscribe: async () => {
        this.openHandleCount = Math.max(0, this.openHandleCount - 1);
        await this.maybeStop();
      }
    };
  }

  async materialize(event: FilesystemEvent): Promise<ChangeEvent | null> {
    if (!shouldPublishFilesystemEvent(event)) {
      return null;
    }
    const cached = getChangeLogCache(this.client, this.workspaceId).get(normalizeChangeEventId(event, this.workspaceId));
    if (cached) {
      return toChangeEvent(this.client, cached);
    }
    const record = await materializeChangeRecord(this.client, this.workspaceId, event);
    return record ? toChangeEvent(this.client, record) : null;
  }

  private ensureStarted(): void {
    if (this.sync) {
      return;
    }
    const sync = new RelayFileSync({
      client: this.client,
      workspaceId: this.workspaceId,
      baseUrl: this.baseUrl,
      token: this.token,
      onPollingFallback: () => {
        this.resolveReadyOnce();
      }
    });
    sync.on("open", () => {
      this.resolveReadyOnce();
    });
    sync.on("state", (state) => {
      if (state === "polling") {
        this.resolveReadyOnce();
      }
    });
    sync.on("error", (error) => {
      if (!this.readyResolved) {
        this.rejectReady(error);
      }
    });
    sync.on("event", (event) => {
      for (const subscription of this.subscriptions) {
        subscription.push(event);
      }
    });
    this.sync = sync;
    sync.start();
    if (sync.getState() === "polling") {
      this.resolveReadyOnce();
    }
  }

  private resolveReadyOnce(): void {
    if (this.readyResolved) {
      return;
    }
    this.readyResolved = true;
    this.resolveReady();
  }

  private async maybeStop(): Promise<void> {
    if (this.openHandleCount > 0 || this.subscriptions.size > 0) {
      return;
    }
    if (this.sync) {
      const sync = this.sync;
      this.sync = undefined;
      await sync.stop();
    }
    const managers = changeStreamManagers.get(this.client);
    if (!managers) {
      return;
    }
    for (const [key, manager] of managers.entries()) {
      if (manager === this) {
        managers.delete(key);
      }
    }
  }
}

function getStreamManager(
  client: RelayFileClient,
  workspaceId: string,
  token: string | undefined,
  baseUrl: string
): RelayFileChangeStreamManager {
  let managers = changeStreamManagers.get(client);
  if (!managers) {
    managers = new Map();
    changeStreamManagers.set(client, managers);
  }
  const key = `${workspaceId}:${token ?? CLIENT_TOKEN_STREAM_KEY}`;
  const existing = managers.get(key);
  if (existing) {
    return existing;
  }
  const manager = new RelayFileChangeStreamManager(client, workspaceId, token, baseUrl);
  managers.set(key, manager);
  return manager;
}

function getChangeLogCache(client: RelayFileClient, workspaceId: string): WorkspaceChangeLogCache {
  let workspaceCaches = changeLogCaches.get(client);
  if (!workspaceCaches) {
    workspaceCaches = new Map();
    changeLogCaches.set(client, workspaceCaches);
  }
  const existing = workspaceCaches.get(workspaceId);
  if (existing) {
    return existing;
  }
  const cache = new WorkspaceChangeLogCache();
  workspaceCaches.set(workspaceId, cache);
  return cache;
}

function normalizeChangePattern(pattern: string): string[] {
  if (typeof pattern !== "string" || pattern.length === 0) {
    throw new Error("subscribe globs must be non-empty strings.");
  }
  if (!pattern.startsWith("/")) {
    throw new Error("subscribe globs must start with '/'.");
  }
  if (pattern.includes("//")) {
    throw new Error("subscribe globs cannot contain empty path segments.");
  }
  const segments = normalizeChangePath(pattern);
  const recursiveIndex = segments.indexOf("**");
  if (recursiveIndex >= 0 && recursiveIndex !== segments.length - 1) {
    throw new Error("subscribe globs only support '**' as the trailing segment.");
  }
  return segments;
}

function normalizeChangePath(path: string): string[] {
  const normalized = path.startsWith("/") ? path : `/${path}`;
  const trimmed = normalized.replace(/\/+$/, "");
  if (trimmed === "") {
    return [];
  }
  return trimmed.split("/").filter(Boolean);
}

function matchChangeSegments(pattern: string[], path: string[]): boolean {
  if (pattern.length > 0 && pattern[pattern.length - 1] === "**") {
    const prefix = pattern.slice(0, -1);
    return path.length >= prefix.length && prefix.every((segment, index) => segment === "*" || segment === path[index]);
  }
  if (pattern.length !== path.length) {
    return false;
  }
  return pattern.every((segment, index) => segment === "*" || segment === path[index]);
}

function shouldPublishFilesystemEvent(event: FilesystemEvent): boolean {
  return event.type === "file.created" || event.type === "file.updated" || event.type === "file.deleted";
}

function decodeBase64Url(value: string): string {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  if (typeof atob === "function") {
    const decoded = atob(padded);
    return decodeURIComponent(Array.from(decoded).map((char) => `%${char.charCodeAt(0).toString(16).padStart(2, "0")}`).join(""));
  }
  const bufferCtor = (globalThis as { Buffer?: typeof Buffer }).Buffer;
  if (bufferCtor) {
    return bufferCtor.from(padded, "base64").toString("utf8");
  }
  throw new Error("No base64 decoder is available in this environment.");
}

function getWorkspaceIdFromToken(token: string): string | undefined {
  const parts = token.split(".");
  if (parts.length < 2) {
    return undefined;
  }
  try {
    const parsed = JSON.parse(decodeBase64Url(parts[1] ?? "")) as JwtClaimsShape;
    return typeof parsed.workspace_id === "string" && parsed.workspace_id.length > 0 ? parsed.workspace_id : undefined;
  } catch {
    return undefined;
  }
}

function getAgentIdFromToken(token: string): string | undefined {
  const parts = token.split(".");
  if (parts.length < 2) {
    return undefined;
  }
  try {
    const parsed = JSON.parse(decodeBase64Url(parts[1] ?? "")) as JwtClaimsShape;
    return typeof parsed.agent_name === "string" && parsed.agent_name.length > 0 ? parsed.agent_name : undefined;
  } catch {
    return undefined;
  }
}

function getSingleKnownWorkspaceId(client: RelayFileClient): string | undefined {
  const workspaceIds = new Set<string>();
  for (const registry of [changeStreamManagers.get(client), changeLogCaches.get(client)]) {
    if (!registry) {
      continue;
    }
    for (const key of registry.keys()) {
      workspaceIds.add(key.split(":")[0] ?? key);
    }
  }
  return workspaceIds.size === 1 ? Array.from(workspaceIds)[0] : undefined;
}

function inferProviderFromPath(path: string): string {
  return normalizeChangePath(path)[0] ?? "relayfile";
}

function singularizeSegment(segment: string): string {
  return segment.endsWith("s") && segment.length > 1 ? segment.slice(0, -1) : segment;
}

function stripExtension(value: string): string {
  return value.replace(/\.[^/.]+$/, "");
}

function inferResourceMetadata(path: string, data: unknown): ChangeEvent["resource"] {
  const segments = normalizeChangePath(path);
  const provider = readStringField(data, ["provider"]) ?? inferProviderFromPath(path);
  const kind = readStringField(data, ["kind", "resourceType"])
    ?? readStringField(data, ["type"])
    ?? `${provider}.${singularizeSegment(segments[1] ?? "resource")}`;
  const id = readStringField(data, ["id", "resourceId", "key"])
    ?? stripExtension(segments[segments.length - 1] ?? path);
  return {
    path,
    kind,
    id,
    provider
  };
}

function readStringField(data: unknown, keys: string[]): string | undefined {
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return undefined;
  }
  const record = data as JsonObject;
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim().length > 0) {
      return value.trim();
    }
  }
  return undefined;
}

function readStringArrayField(data: unknown, keys: string[]): string[] | undefined {
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return undefined;
  }
  const record = data as JsonObject;
  for (const key of keys) {
    const value = record[key];
    if (Array.isArray(value)) {
      const items = value.filter((entry): entry is string => typeof entry === "string" && entry.trim().length > 0).map((entry) => entry.trim());
      if (items.length > 0) {
        return items;
      }
    }
  }
  return undefined;
}

function readActorField(data: unknown): ChangeEvent["summary"]["actor"] | undefined {
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return undefined;
  }
  const record = data as JsonObject;
  const rawActor = record.actor ?? record.assignee ?? record.author;
  if (typeof rawActor === "string" && rawActor.trim().length > 0) {
    return { id: rawActor.trim() };
  }
  if (!rawActor || typeof rawActor !== "object" || Array.isArray(rawActor)) {
    return undefined;
  }
  const actorRecord = rawActor as JsonObject;
  const id = typeof actorRecord.id === "string" ? actorRecord.id.trim() : undefined;
  if (!id) {
    return undefined;
  }
  const displayName = typeof actorRecord.displayName === "string"
    ? actorRecord.displayName.trim()
    : typeof actorRecord.name === "string"
      ? actorRecord.name.trim()
      : undefined;
  return { id, ...(displayName ? { displayName } : {}) };
}

function truncateString(value: string, maxLength: number): string {
  return value.length <= maxLength ? value : `${value.slice(0, Math.max(0, maxLength - 3))}...`;
}

function basename(path: string): string {
  const parts = normalizeChangePath(path);
  return parts[parts.length - 1] ?? path;
}

function buildChangeSummary(path: string, data: unknown): ChangeEvent["summary"] {
  const title = readStringField(data, ["title", "name", "summary", "subject"]) ?? stripExtension(basename(path));
  const summary: ChangeEvent["summary"] = {
    title: truncateString(title, 120)
  };
  const status = readStringField(data, ["status", "state"]);
  if (status) {
    summary.status = status;
  }
  const priority = readStringField(data, ["priority"]);
  if (priority) {
    summary.priority = priority;
  }
  const labels = readStringArrayField(data, ["labels"]);
  if (labels) {
    summary.labels = labels.slice(0, 8);
  }
  const actor = readActorField(data);
  if (actor) {
    summary.actor = actor;
  }
  const fieldsChanged = readStringArrayField(data, ["fieldsChanged", "changedFields"]);
  if (fieldsChanged) {
    summary.fieldsChanged = fieldsChanged;
  }
  const tags = readStringArrayField(data, ["tags"]);
  if (tags) {
    summary.tags = tags.slice(0, 8);
  }
  return summary;
}

function decodeFilePayload(file: FileReadResponse): unknown {
  if (file.encoding === "base64") {
    return {
      contentBase64: file.content,
      contentType: file.contentType,
      encoding: "base64"
    };
  }
  const looksJson = file.contentType.includes("json") || file.path.endsWith(".json");
  if (looksJson) {
    try {
      return JSON.parse(file.content) as unknown;
    } catch {
      // Fall through to the raw string when payloads are malformed.
    }
  }
  return file.content;
}

async function sha256Hex(value: string): Promise<string> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    return `sha256:${value}`;
  }
  const hash = await subtle.digest("SHA-256", new TextEncoder().encode(value));
  const bytes = new Uint8Array(hash);
  return `sha256:${Array.from(bytes).map((byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

function normalizeChangeEventId(event: FilesystemEvent, workspaceId: string): string {
  if (event.eventId && !event.eventId.startsWith("ws:")) {
    return event.eventId;
  }
  return [
    "relayfile",
    workspaceId,
    event.type,
    event.path,
    event.revision,
    event.timestamp
  ].join(":");
}

function toChangeEvent(client: RelayFileClient, record: CachedChangeRecord): ChangeEvent {
  return {
    ...record.wire,
    expand: async <L extends ExpansionLevel = "summary">(level?: L): Promise<Expansion<L>> => {
      const normalizedLevel = (level ?? "summary") as ExpansionLevel;
      if (normalizedLevel === "summary") {
        return {
          level: normalizedLevel,
          path: record.wire.resource.path,
          summary: record.wire.summary
        } as Expansion<L>;
      }
      if (normalizedLevel === "full") {
        const resource = await client.getResourceAtEvent(record.wire.id);
        return {
          level: normalizedLevel,
          path: resource.path,
          data: resource.data
        } as Expansion<L>;
      }
      throw createM2NotImplementedError(`ChangeEvent.expand(${JSON.stringify(normalizedLevel)})`);
    }
  };
}

function normalizeWireChangeEvent(payload: unknown): ChangeEventWireShape {
  const data = (payload ?? {}) as Record<string, unknown>;
  const resource = (data.resource ?? {}) as Record<string, unknown>;
  const summary = (data.summary ?? {}) as Record<string, unknown>;
  return {
    id: typeof data.id === "string" ? data.id : "",
    workspace: typeof data.workspace === "string" ? data.workspace : "",
    agentId: typeof data.agentId === "string" ? data.agentId : undefined,
    type: "relayfile.changed",
    occurredAt: typeof data.occurredAt === "string" ? data.occurredAt : new Date().toISOString(),
    resource: {
      path: typeof resource.path === "string" ? resource.path : "",
      kind: typeof resource.kind === "string" ? resource.kind : "relayfile.resource",
      id: typeof resource.id === "string" ? resource.id : "",
      provider: typeof resource.provider === "string" ? resource.provider : "relayfile"
    },
    summary: {
      ...(typeof summary.title === "string" ? { title: summary.title } : {}),
      ...(typeof summary.status === "string" ? { status: summary.status } : {}),
      ...(typeof summary.priority === "string" ? { priority: summary.priority } : {}),
      ...(Array.isArray(summary.labels) ? { labels: summary.labels.filter((entry): entry is string => typeof entry === "string") } : {}),
      ...(summary.actor && typeof summary.actor === "object"
        ? {
            actor: {
              id: typeof (summary.actor as Record<string, unknown>).id === "string" ? (summary.actor as Record<string, unknown>).id as string : "",
              ...(typeof (summary.actor as Record<string, unknown>).displayName === "string"
                ? { displayName: (summary.actor as Record<string, unknown>).displayName as string }
                : {})
            }
          }
        : {}),
      ...(Array.isArray(summary.fieldsChanged)
        ? { fieldsChanged: summary.fieldsChanged.filter((entry): entry is string => typeof entry === "string") }
        : {}),
      ...(Array.isArray(summary.tags) ? { tags: summary.tags.filter((entry): entry is string => typeof entry === "string").slice(0, 8) } : {})
    },
    ...(typeof data.digest === "string" ? { digest: data.digest } : {})
  };
}

async function materializeChangeRecord(
  client: RelayFileClient,
  workspaceId: string,
  event: FilesystemEvent
): Promise<CachedChangeRecord | null> {
  const eventId = normalizeChangeEventId(event, workspaceId);
  const occurredAt = event.timestamp || new Date().toISOString();
  const fallbackDigest = event.revision ? `revision:${event.revision}` : `deleted:${occurredAt}`;
  const fallbackResource: ResourceAtEventResult = {
    path: event.path,
    data: { path: event.path, deleted: event.type === "file.deleted" },
    digest: fallbackDigest
  };

  let tokenAgentId: string | undefined;
  try {
    tokenAgentId = getAgentIdFromToken(await client.getToken());
  } catch {
    tokenAgentId = undefined;
  }

  let resource = fallbackResource;
  let wire: ChangeEventWireShape = {
    id: eventId,
    workspace: workspaceId,
    ...(tokenAgentId ? { agentId: tokenAgentId } : {}),
    type: "relayfile.changed",
    occurredAt,
    resource: inferResourceMetadata(event.path, resource.data),
    summary: buildChangeSummary(event.path, resource.data),
    digest: resource.digest
  };

  if (event.type !== "file.deleted") {
    try {
      const file = await client.readFile(workspaceId, event.path);
      const data = decodeFilePayload(file);
      const digest = await sha256Hex(file.content);
      resource = {
        path: event.path,
        data,
        digest
      };
      wire = {
        ...wire,
        resource: inferResourceMetadata(event.path, data),
        summary: buildChangeSummary(event.path, data),
        digest
      };
    } catch (error) {
      if (typeof console !== "undefined" && typeof console.warn === "function") {
        console.warn(`[relayfile-sdk] Failed to materialize change event for ${event.path}; falling back to path-only summary.`, error);
      }
    }
  }

  const record: CachedChangeRecord = {
    wire,
    resource,
    storedAt: Date.now()
  };
  getChangeLogCache(client, workspaceId).record(record);
  return record;
}

export class RelayFileClient {
  private readonly baseUrl: string;
  private readonly tokenProvider: AccessTokenProvider;
  private readonly fetchImpl: typeof fetch;
  private readonly userAgent?: string;
  private readonly retryOptions: NormalizedRetryOptions;

  constructor(options: RelayFileClientOptions) {
    this.baseUrl = (options.baseUrl ?? DEFAULT_RELAYFILE_BASE_URL).replace(/\/+$/, "");
    this.tokenProvider = options.token;
    this.fetchImpl = options.fetchImpl ?? fetch.bind(globalThis);
    this.userAgent = options.userAgent;
    this.retryOptions = normalizeRetryOptions(options.retry);
  }

  /**
   * Resolve the current access token via the configured token provider.
   *
   * Components that need a fresh JWT for out-of-band auth (the WebSocket
   * upgrade handshake, signed URLs, downstream services that proxy Relayfile
   * tokens) should call this on every connection rather than caching the
   * value, so token rotation/refresh propagates without restart.
   *
   * Always returns a Promise so callers don't need to special-case the
   * sync-vs-async tokenProvider shapes.
   */
  async getToken(): Promise<string> {
    return resolveToken(this.tokenProvider);
  }

  /**
   * Return the normalized API base URL this client was constructed with.
   *
   * onWrite/sync consumers use this so an explicit RelayFileClient remains the
   * source of truth for both HTTP and WebSocket traffic instead of letting a
   * process-level env override silently redirect only the WS side.
   */
  getBaseUrl(): string {
    return this.baseUrl;
  }

  async listTree(workspaceId: string, options: ListTreeOptions = {}): Promise<TreeResponse> {
    const query = buildQuery({
      path: options.path ?? "/",
      depth: options.depth,
      cursor: options.cursor,
      forkId: options.forkId
    });
    return this.request<TreeResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/tree${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async readFile(workspaceId: string, path: string, correlationId?: string, signal?: AbortSignal): Promise<FileReadResponse>;
  async readFile(input: ReadFileInput): Promise<FileReadResponse>;
  async readFile(
    workspaceOrInput: string | ReadFileInput,
    path?: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<FileReadResponse> {
    const input: ReadFileInput = typeof workspaceOrInput === "string"
      ? {
          workspaceId: workspaceOrInput,
          path: path ?? "",
          correlationId,
          signal
        }
      : workspaceOrInput;
    const query = buildQuery({ path: input.path, forkId: input.forkId });
    return this.request<FileReadResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/file${query}`,
      correlationId: input.correlationId,
      signal: input.signal
    });
  }

  async queryFiles(workspaceId: string, options: QueryFilesOptions = {}): Promise<FileQueryResponse> {
    const params = new URLSearchParams();
    if (options.path !== undefined) params.set("path", options.path);
    if (options.provider !== undefined) params.set("provider", options.provider);
    if (options.relation !== undefined) params.set("relation", options.relation);
    if (options.permission !== undefined) params.set("permission", options.permission);
    if (options.comment !== undefined) params.set("comment", options.comment);
    if (options.cursor !== undefined) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    if (options.forkId !== undefined) params.set("forkId", options.forkId);
    if (options.properties !== undefined) {
      for (const [key, value] of Object.entries(options.properties)) {
        if (key !== "" && value !== undefined) {
          params.set(`property.${key}`, value);
        }
      }
    }
    const encoded = params.toString();
    const query = encoded ? `?${encoded}` : "";
    return this.request<FileQueryResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/query${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async writeFile(input: WriteFileInput): Promise<WriteQueuedResponse> {
    const { workspaceId, path, correlationId, baseRevision, content, contentType, encoding, contentIdentity, signal } = input;
    const query = buildQuery({ path, forkId: input.forkId });
    return this.request<WriteQueuedResponse>({
      method: "PUT",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`,
      correlationId,
      headers: {
        "If-Match": baseRevision
      },
      // Single-file PUT expects the target path in the query string, not the JSON body.
      body: {
        contentType: contentType ?? "text/markdown",
        content,
        encoding,
        semantics: input.semantics,
        ...(contentIdentity ? { contentIdentity } : {})
      },
      signal
    });
  }

  async bulkWrite(input: BulkWriteInput): Promise<BulkWriteResponse> {
    const query = buildQuery({ forkId: input.forkId });
    const response = await this.performRequest({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/bulk${query}`,
      correlationId: input.correlationId,
      body: {
        files: input.files
      },
      signal: input.signal
    });
    return this.readPayload(response) as Promise<BulkWriteResponse>;
  }

  async deleteFile(input: DeleteFileInput): Promise<WriteQueuedResponse> {
    const query = buildQuery({ path: input.path, forkId: input.forkId });
    return this.request<WriteQueuedResponse>({
      method: "DELETE",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/file${query}`,
      correlationId: input.correlationId,
      headers: {
        "If-Match": input.baseRevision
      },
      signal: input.signal
    });
  }

  async createFork(input: CreateForkInput): Promise<ForkHandle> {
    const body: { proposalId: string; ttlSeconds?: number } = {
      proposalId: input.proposalId
    };
    if (input.ttlSeconds !== undefined) {
      body.ttlSeconds = input.ttlSeconds;
    }
    return this.request<ForkHandle>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/forks`,
      correlationId: input.correlationId,
      body,
      signal: input.signal
    });
  }

  async discardFork(input: DiscardForkInput): Promise<void> {
    await this.performRequest({
      method: "DELETE",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/forks/${encodeURIComponent(input.forkId)}`,
      correlationId: input.correlationId,
      signal: input.signal
    });
  }

  async commitFork(input: CommitForkInput): Promise<CommitForkResponse> {
    return this.request<CommitForkResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/forks/${encodeURIComponent(input.forkId)}/commit`,
      correlationId: input.correlationId,
      signal: input.signal
    });
  }

  async getEvents(workspaceId: string, options: GetEventsOptions = {}): Promise<EventFeedResponse> {
    const query = buildQuery({
      provider: options.provider,
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<EventFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/events${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  /**
   * M1 proactive runtime contract stub.
   *
   * Live data-trigger delivery is deferred to M2; the public method exists now
   * so downstream packages can compile against the final surface without
   * waiting for the WebSocket and change-log plumbing to land.
   */
  subscribe(
    globs: string[],
    onChange: (event: ChangeEvent) => void,
    options?: SubscribeOptions,
  ): Subscription {
    const setup = this.resolveWorkspaceId(options?.aclToken)
      .then((workspaceId) => {
        const manager = getStreamManager(this, workspaceId, options?.aclToken, this.baseUrl);
        return manager.addSubscription(globs, onChange, options);
      })
      .catch((error) => {
        if (typeof console !== "undefined" && typeof console.error === "function") {
          console.error("RelayFile subscribe initialization failed", error);
        }
        return null;
      });
    return {
      async unsubscribe(): Promise<void> {
        const subscription = await setup;
        await subscription?.unsubscribe();
      },
    };
  }

  /**
   * M1 proactive runtime transport stub.
   *
   * M2 will open the dedicated change-stream transport that powers
   * `subscribe()` and replay-on-start delivery. M1 reserves the public entry
   * point now so downstream packages can type against the final API surface.
   */
  open(options: ChangeStreamConnectionOptions): ChangeStreamConnection {
    const manager = getStreamManager(this, options.workspaceId, options.aclToken, this.baseUrl);
    const connection = manager.open();
    const replay = this.primeReplayCache(options).catch((error) => {
      if (typeof console !== "undefined" && typeof console.error === "function") {
        console.error("RelayFile change-stream replay initialization failed", error);
      }
    });
    return {
      ready: Promise.all([connection.ready, replay]).then(() => undefined),
      unsubscribe: () => connection.unsubscribe()
    };
  }

  /**
   * M1 proactive runtime contract stub used by `event.expand(\"full\")`.
   *
   * M2 will resolve the stable `eventId` through relayfile's retained change
   * log and return the canonical normalized payload stored at the resource
   * path.
   */
  async getResourceAtEvent(eventId: string): Promise<ResourceAtEventResult> {
    const workspaceId = await this.resolveWorkspaceId();
    const cached = getChangeLogCache(this, workspaceId).get(eventId);
    if (cached && cached.resource.data !== null) {
      return cached.resource;
    }
    return this.request<ResourceAtEventResult>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/changes/resource${buildQuery({ eventId })}`
    });
  }

  /**
   * M1 proactive runtime contract stub used by replay-on-start flows.
   *
   * M2 will resolve retained change-log entries newer than the supplied ISO
   * timestamp.
   */
  async listChangesSince(isoTimestamp: string): Promise<ChangeLogQueryResult> {
    const workspaceId = await this.resolveWorkspaceId();
    const cache = getChangeLogCache(this, workspaceId);
    const cached = cache.listSince(isoTimestamp);
    if (cached.length > 0) {
      return {
        events: cached.map((record) => toChangeEvent(this, record))
      };
    }
    const payload = await this.request<{ events?: unknown[] }>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/changes${buildQuery({ since: isoTimestamp })}`
    });
    const records = (payload.events ?? []).map((event) => this.cacheWireChangeEvent(workspaceId, normalizeWireChangeEvent(event)));
    return {
      events: records.map((record) => toChangeEvent(this, record))
    };
  }

  /**
   * M1 proactive runtime contract stub used by replay-on-start flows.
   *
   * M2 will return the last N retained change events for the workspace.
   */
  async listLastNChanges(limit: number): Promise<ChangeLogQueryResult> {
    const workspaceId = await this.resolveWorkspaceId();
    const cache = getChangeLogCache(this, workspaceId);
    const cached = cache.listLastN(limit);
    if (cached.length > 0) {
      return {
        events: cached.map((record) => toChangeEvent(this, record))
      };
    }
    const payload = await this.request<{ events?: unknown[] }>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/changes${buildQuery({ last: limit })}`
    });
    const records = (payload.events ?? []).map((event) => this.cacheWireChangeEvent(workspaceId, normalizeWireChangeEvent(event)));
    return {
      events: records.map((record) => toChangeEvent(this, record))
    };
  }

  async exportWorkspace(options: ExportOptions): Promise<ExportJsonResponse | Blob> {
    const format = options.format ?? "json";
    const query = buildQuery({ format });
    const correlationId = options.correlationId ?? generateCorrelationId();
    const response = await this.performRequest({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(options.workspaceId)}/fs/export${query}`,
      correlationId,
      signal: options.signal,
      accept: format === "json" ? "application/json" : "*/*"
    });

    if (format === "json") {
      return normalizeExportJsonResponse(await this.readPayload(response));
    }

    return createBlobFromResponse(response);
  }

  connectWebSocket(
    workspaceId: string,
    options: ConnectWebSocketOptions = {}
  ): WebSocketConnection {
    const token = options.token ?? resolveSyncToken(this.tokenProvider);
    const url = new URL(`${this.baseUrl}/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/ws`);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("token", token);

    const socket = new WebSocket(url.toString());
    return new RelayFileWebSocketConnection(socket, options.onEvent);
  }

  async getOp(
    workspaceId: string,
    opId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<OperationStatusResponse> {
    return this.request<OperationStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops/${encodeURIComponent(opId)}`,
      correlationId,
      signal
    });
  }

  async listOps(workspaceId: string, options: GetOperationsOptions = {}): Promise<OperationFeedResponse> {
    const query = buildQuery({
      status: options.status,
      action: options.action,
      provider: options.provider,
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<OperationFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async replayOp(workspaceId: string, opId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops/${encodeURIComponent(opId)}/replay`,
      correlationId,
      signal
    });
  }

  async replayAdminEnvelope(envelopeId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/admin/replay/envelope/${encodeURIComponent(envelopeId)}`,
      correlationId,
      signal
    });
  }

  async replayAdminOp(opId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/admin/replay/op/${encodeURIComponent(opId)}`,
      correlationId,
      signal
    });
  }

  async getBackendStatus(correlationId?: string, signal?: AbortSignal): Promise<BackendStatusResponse> {
    return this.request<BackendStatusResponse>({
      method: "GET",
      path: "/v1/admin/backends",
      correlationId,
      signal
    });
  }

  async getAdminIngressStatus(
    optionsOrCorrelationId: GetAdminIngressStatusOptions | string = {},
    signal?: AbortSignal
  ): Promise<AdminIngressStatusResponse> {
    const options: GetAdminIngressStatusOptions =
      typeof optionsOrCorrelationId === "string"
        ? { correlationId: optionsOrCorrelationId, signal }
        : optionsOrCorrelationId;
    const query = buildQuery({
      workspaceId: options.workspaceId,
      provider: options.provider,
      alertProfile: options.alertProfile,
      pendingThreshold: options.pendingThreshold,
      deadLetterThreshold: options.deadLetterThreshold,
      staleThreshold: options.staleThreshold,
      dropRateThreshold: options.dropRateThreshold,
      nonZeroOnly: options.nonZeroOnly,
      maxAlerts: options.maxAlerts,
      cursor: options.cursor,
      limit: options.limit,
      includeWorkspaces: options.includeWorkspaces,
      includeAlerts: options.includeAlerts
    });
    return this.request<AdminIngressStatusResponse>({
      method: "GET",
      path: `/v1/admin/ingress${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getAdminSyncStatus(
    optionsOrCorrelationId: GetAdminSyncStatusOptions | string = {},
    signal?: AbortSignal
  ): Promise<AdminSyncStatusResponse> {
    const options: GetAdminSyncStatusOptions =
      typeof optionsOrCorrelationId === "string"
        ? { correlationId: optionsOrCorrelationId, signal }
        : optionsOrCorrelationId;
    const query = buildQuery({
      workspaceId: options.workspaceId,
      provider: options.provider,
      nonZeroOnly: options.nonZeroOnly,
      cursor: options.cursor,
      limit: options.limit,
      includeWorkspaces: options.includeWorkspaces,
      statusErrorThreshold: options.statusErrorThreshold,
      lagSecondsThreshold: options.lagSecondsThreshold,
      deadLetteredEnvelopesThreshold: options.deadLetteredEnvelopesThreshold,
      deadLetteredOpsThreshold: options.deadLetteredOpsThreshold,
      maxAlerts: options.maxAlerts,
      includeAlerts: options.includeAlerts
    });
    return this.request<AdminSyncStatusResponse>({
      method: "GET",
      path: `/v1/admin/sync${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncStatus(workspaceId: string, options: GetSyncStatusOptions = {}): Promise<SyncStatusResponse> {
    const query = buildQuery({ provider: options.provider });
    return this.request<SyncStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/status${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncIngressStatus(
    workspaceId: string,
    options: GetSyncIngressStatusOptions = {}
  ): Promise<SyncIngressStatusResponse> {
    const query = buildQuery({
      provider: options.provider
    });
    return this.request<SyncIngressStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/ingress${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncDeadLetters(
    workspaceId: string,
    options: GetSyncDeadLettersOptions = {}
  ): Promise<DeadLetterFeedResponse> {
    const query = buildQuery({
      provider: options.provider,
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<DeadLetterFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<DeadLetterItem> {
    return this.request<DeadLetterItem>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}`,
      correlationId,
      signal
    });
  }

  async replaySyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}/replay`,
      correlationId,
      signal
    });
  }

  async ackSyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<AckResponse> {
    return this.request<AckResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}/ack`,
      correlationId,
      signal
    });
  }

  async triggerSyncRefresh(
    workspaceId: string,
    provider: string,
    reason?: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/refresh`,
      correlationId,
      body: {
        provider,
        reason
      },
      signal
    });
  }

  async ingestWebhook(input: IngestWebhookInput): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/webhooks/ingest`,
      correlationId: input.correlationId,
      body: {
        provider: input.provider,
        event_type: input.event_type,
        path: input.path,
        data: input.data,
        delivery_id: input.delivery_id,
        timestamp: input.timestamp,
        headers: input.headers
      },
      signal: input.signal
    });
  }

  async listPendingWritebacks(
    workspaceId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<WritebackItem[]> {
    return this.request<WritebackItem[]>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/writeback/pending`,
      correlationId,
      signal
    });
  }

  async ackWriteback(input: AckWritebackInput): Promise<AckWritebackResponse> {
    return this.request<AckWritebackResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/writeback/${encodeURIComponent(input.itemId)}/ack`,
      correlationId: input.correlationId,
      body: {
        success: input.success,
        error: input.error
      },
      signal: input.signal
    });
  }

  private cacheWireChangeEvent(workspaceId: string, wire: ChangeEventWireShape): CachedChangeRecord {
    const record: CachedChangeRecord = {
      wire,
      resource: {
        path: wire.resource.path,
        data: null,
        digest: wire.digest ?? `event:${wire.id}`
      },
      storedAt: Date.now()
    };
    getChangeLogCache(this, workspaceId).record(record);
    return record;
  }

  private async primeReplayCache(options: ChangeStreamConnectionOptions): Promise<void> {
    if (!options.replayOnStart || options.replayOnStart === "none") {
      return;
    }
    if (options.replayOnStart.startsWith("since:")) {
      await this.listChangesSince(options.replayOnStart.slice("since:".length));
      return;
    }
    if (options.replayOnStart.startsWith("last:")) {
      const limit = Number.parseInt(options.replayOnStart.slice("last:".length), 10);
      await this.listLastNChanges(limit);
    }
  }

  private async resolveWorkspaceId(tokenOverride?: string): Promise<string> {
    const knownWorkspaceId = getSingleKnownWorkspaceId(this);
    if (knownWorkspaceId) {
      return knownWorkspaceId;
    }
    const token = tokenOverride ?? await this.getToken();
    const workspaceId = getWorkspaceIdFromToken(token);
    if (workspaceId) {
      return workspaceId;
    }
    throw new Error("RelayFile proactive-runtime APIs require a workspace-scoped JWT with a workspace_id claim.");
  }

  private async request<T>(params: {
    method: string;
    path: string;
    headers?: Record<string, string>;
    body?: unknown;
    correlationId?: string;
    signal?: AbortSignal;
  }): Promise<T> {
    const response = await this.performRequest(params);
    const payload = await this.readPayload(response);
    return payload as T;
  }

  private async performRequest(params: {
    method: string;
    path: string;
    headers?: Record<string, string>;
    body?: unknown;
    correlationId?: string;
    signal?: AbortSignal;
    accept?: string;
  }): Promise<Response> {
    const existingCorrelationId = getHeaderValue(params.headers, "X-Correlation-Id");
    const correlationId = existingCorrelationId ?? params.correlationId ?? generateCorrelationId();
    const baseHeaders: Record<string, string> = {
      ...(params.headers ?? {})
    };
    if (!existingCorrelationId) {
      baseHeaders["X-Correlation-Id"] = correlationId;
    }
    if (params.body !== undefined) {
      baseHeaders["Content-Type"] = "application/json";
    }
    if (params.accept) {
      baseHeaders["Accept"] = params.accept;
    }
    if (this.userAgent) {
      baseHeaders["User-Agent"] = this.userAgent;
    }

    let retries = 0;
    const url = `${this.baseUrl}${params.path}`;
    for (;;) {
      const token = await resolveToken(this.tokenProvider);
      const headers: Record<string, string> = {
        Authorization: `Bearer ${token}`,
        ...baseHeaders
      };
      const requestInit: RequestInit = {
        method: params.method,
        headers,
        body: params.body === undefined ? undefined : JSON.stringify(params.body)
      };
      if (params.signal) {
        requestInit.signal = params.signal;
      }

      let response: Response;
      try {
        response = await this.fetchImpl(url, requestInit);
      } catch (error) {
        if (this.shouldRetryError(error, retries, params.signal)) {
          retries += 1;
          await this.sleep(this.computeRetryDelayMs(retries, null), params.signal);
          continue;
        }
        throw error;
      }

      if (response.ok) {
        return response;
      }

      const payload = await this.readPayload(response);
      if (this.shouldRetryStatus(response.status, retries, params.signal)) {
        retries += 1;
        await this.sleep(this.computeRetryDelayMs(retries, response.headers.get("retry-after")), params.signal);
        continue;
      }

      this.throwForError(response.status, payload, response.headers);
    }
  }

  private shouldRetryStatus(status: number, retries: number, signal?: AbortSignal): boolean {
    if (signal?.aborted) {
      return false;
    }
    if (retries >= this.retryOptions.maxRetries) {
      return false;
    }
    return status === 429 || (status >= 500 && status <= 599);
  }

  private shouldRetryError(error: unknown, retries: number, signal?: AbortSignal): boolean {
    if (signal?.aborted) {
      return false;
    }
    if (error instanceof Error && error.name === "AbortError") {
      return false;
    }
    return retries < this.retryOptions.maxRetries;
  }

  private computeRetryDelayMs(retryAttempt: number, retryAfterHeader: string | null): number {
    const retryAfterMs = this.parseRetryAfterMs(retryAfterHeader);
    if (retryAfterMs !== null) {
      return Math.min(this.retryOptions.maxDelayMs, retryAfterMs);
    }
    const backoff = this.retryOptions.baseDelayMs * Math.pow(2, Math.max(0, retryAttempt - 1));
    const capped = Math.min(this.retryOptions.maxDelayMs, backoff);
    const jitter = this.retryOptions.jitterRatio;
    const factor = 1 + (Math.random() * 2 - 1) * jitter;
    return Math.max(0, Math.round(capped * factor));
  }

  private parseRetryAfterMs(retryAfterHeader: string | null): number | null {
    if (!retryAfterHeader) {
      return null;
    }
    const seconds = Number.parseInt(retryAfterHeader, 10);
    if (Number.isFinite(seconds) && seconds >= 0) {
      return seconds * 1000;
    }
    const timestamp = Date.parse(retryAfterHeader);
    if (!Number.isNaN(timestamp)) {
      return Math.max(0, timestamp - Date.now());
    }
    return null;
  }

  private async sleep(delayMs: number, signal?: AbortSignal): Promise<void> {
    if (delayMs <= 0) {
      return;
    }
    if (signal?.aborted) {
      throw createAbortError();
    }
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      }, delayMs);
      const onAbort = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        reject(createAbortError());
      };
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  }

  private async readPayload(response: Response): Promise<unknown> {
    const contentType = response.headers.get("content-type") ?? "";
    if (contentType.includes("application/json")) {
      try {
        return await response.json();
      } catch {
        return {};
      }
    }
    const text = await response.text();
    return {
      message: text
    };
  }

  private throwForError(status: number, payload: unknown, headers: Headers): never {
    const rawData = (payload ?? {}) as Record<string, unknown>;
    const data = rawData as Partial<ErrorResponse> & {
      expectedRevision?: string;
      currentRevision?: string;
      currentContentPreview?: string;
    };
    const details = normalizeErrorDetails(rawData, data.details);

    if (
      status === 409 &&
      typeof data.expectedRevision === "string" &&
      typeof data.currentRevision === "string"
    ) {
      throw new RevisionConflictError(status, {
        code: data.code ?? "revision_conflict",
        message: data.message ?? "Revision conflict",
        correlationId: data.correlationId ?? "",
        details,
        expectedRevision: data.expectedRevision,
        currentRevision: data.currentRevision,
        currentContentPreview: data.currentContentPreview
      });
    }

    if (status === 409 && data.code === "invalid_state") {
      throw new InvalidStateError(status, {
        code: data.code,
        message: data.message ?? "Invalid resource state",
        correlationId: data.correlationId,
        details
      });
    }

    if (status === 429 && data.code === "queue_full") {
      const retryAfterRaw = headers.get("retry-after");
      let retryAfterSeconds: number | undefined;
      if (retryAfterRaw) {
        const parsed = Number.parseInt(retryAfterRaw, 10);
        if (Number.isFinite(parsed) && parsed >= 0) {
          retryAfterSeconds = parsed;
        }
      }
      throw new QueueFullError(
        status,
        {
          code: data.code,
          message: data.message ?? "Ingress queue full",
          correlationId: data.correlationId,
          details
        },
        retryAfterSeconds
      );
    }

    if (status === 413) {
      throw new PayloadTooLargeError(status, {
        code: data.code ?? "payload_too_large",
        message: data.message ?? "Request payload exceeds configured limit",
        correlationId: data.correlationId,
        details
      });
    }

    throw new RelayFileApiError(status, {
      code: data.code ?? "api_error",
      message: data.message ?? `HTTP ${status}`,
      correlationId: data.correlationId,
      details
    });
  }
}
