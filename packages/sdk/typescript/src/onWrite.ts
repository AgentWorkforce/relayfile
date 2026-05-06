import type { WriteEvent, WriteEventOperation, WriteEventSource } from "@relayfile/core";

import { RelayFileClient, DEFAULT_RELAYFILE_BASE_URL } from "./client.js";
import { RelayFileSync, type RelayFileSyncSocket } from "./sync.js";
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
  token?: string;
  webSocketFactory?: (url: string) => RelayFileSyncSocket;
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

  const operations = new Set(options.operations ?? DEFAULT_OPERATIONS);
  for (const operation of operations) {
    if (operation !== "create" && operation !== "update" && operation !== "delete") {
      throw new Error(`Invalid onWrite operation: ${operation}`);
    }
  }

  let dispatcher = dispatchers.get(client);
  if (!dispatcher) {
    dispatcher = new OnWriteDispatcher(client);
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
    baseUrl: options.baseUrl,
    token: options.token,
    webSocketFactory: options.webSocketFactory
  });

  return () => {
    dispatcher?.unregister(registration.id);
  };
}

class OnWriteDispatcher {
  private readonly client: OnWriteClient;
  private readonly registrations: OnWriteRegistration[] = [];
  private readonly patternChains = new Map<string, Promise<void>>();
  private sync?: RelayFileSync;
  // Captured at first registration. RelayFileSync normalizes the FilesystemEvent
  // shape and does not surface workspaceId on emitted events, so we thread the
  // subscribed workspaceId through here and stamp it onto every WriteEvent we
  // hand to user handlers.
  private workspaceId?: string;

  constructor(client: OnWriteClient) {
    this.client = client;
  }

  register(
    registration: OnWriteRegistration,
    options: Required<Pick<OnWriteOptions, "workspaceId">> & Pick<OnWriteOptions, "signal" | "baseUrl" | "token" | "webSocketFactory">
  ): void {
    this.workspaceId = options.workspaceId;
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

  private ensureSync(options: Required<Pick<OnWriteOptions, "workspaceId">> & Pick<OnWriteOptions, "baseUrl" | "token" | "webSocketFactory">): void {
    if (this.sync) {
      return;
    }

    this.sync = RelayFileSync.connect({
      client: this.client,
      workspaceId: options.workspaceId,
      baseUrl: options.baseUrl ?? readEnv("RELAYFILE_BASE_URL") ?? DEFAULT_RELAYFILE_BASE_URL,
      token: options.token ?? readEnv("RELAYFILE_TOKEN"),
      reconnect: {
        minDelayMs: DEFAULT_RECONNECT_MIN_DELAY_MS,
        maxDelayMs: DEFAULT_RECONNECT_MAX_DELAY_MS
      },
      webSocketFactory: options.webSocketFactory,
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

      const previous = this.patternChains.get(registration.pattern) ?? Promise.resolve();
      const next = previous
        .catch(() => undefined)
        .then(() => this.runHandler(registration, writeEvent));
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

  private async recordHandlerError(error: OnWriteHandlerError): Promise<void> {
    if (typeof this.client.recordHandlerError === "function") {
      await this.client.recordHandlerError(error);
      return;
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
  const raw = event as FilesystemEvent & Partial<WriteEvent> & { previousRevision?: string | null };
  return {
    workspaceId: raw.workspaceId ?? workspaceId ?? "",
    path: event.path,
    operation,
    revision: event.revision,
    previousRevision: raw.previousRevision ?? null,
    timestamp: event.timestamp,
    source: raw.source ?? sourceFromOrigin(event.origin),
    value: raw.value,
    actor: raw.actor
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

function readEnv(name: string): string | undefined {
  return (globalThis as EnvironmentLike).process?.env?.[name];
}
