import type { RelayFileClient } from "../client.js";
import {
  computeCanonicalPath,
  type ConnectionProvider,
  type NormalizedWebhook,
  type ProxyRequest,
  type ProxyResponse
} from "../provider.js";
import type {
  FileReadResponse,
  FileSemantics,
  WriteFileInput,
  WriteQueuedResponse
} from "../types.js";

export type MockNormalizedWebhook = NormalizedWebhook;

export interface MockWriteQueuedResponse extends WriteQueuedResponse {}

export interface MockStoredFile extends FileReadResponse {
  workspaceId: string;
  correlationId?: string;
}

export interface MockWriteRecord extends MockStoredFile {
  opId: string;
  baseRevision: string;
  previousRevision: string | null;
  existed: boolean;
  sequence: number;
}

export interface MockIngestError {
  path: string;
  error: string;
}

export interface MockIngestResult {
  filesWritten: number;
  filesUpdated: number;
  filesDeleted: number;
  paths: string[];
  errors: MockIngestError[];
}

export interface MockProxyRequest extends ProxyRequest {}

export interface MockProxyResponse extends ProxyResponse {}

export interface MockConnectionProvider extends ConnectionProvider {
  readonly proxyCalls: MockProxyRequest[];
  readonly healthCheckCalls: string[];
}

export interface MockPluginAdapter {
  readonly name: string;
  readonly version: string;
  readonly provider: MockConnectionProvider;
  readonly ingestedEvents: MockNormalizedWebhook[];
  readonly writes: MockWriteRecord[];
  ingestWebhook(workspaceId: string, event: MockNormalizedWebhook): Promise<MockIngestResult>;
  computePath(event: MockNormalizedWebhook): string;
  computeSemantics(event: MockNormalizedWebhook): FileSemantics;
  supportedEvents?(): string[];
}

export interface MockPluginEventMap {
  registered: {
    adapterName: string;
    version: string;
    providerName: string;
  };
  ingested: {
    adapterName: string;
    workspaceId: string;
    event: MockNormalizedWebhook;
    result: MockIngestResult;
  };
  error: {
    adapterName?: string;
    workspaceId: string;
    event?: MockNormalizedWebhook;
    error: Error;
  };
}

export interface MockLifecycleEventRecord<
  TEventName extends keyof MockPluginEventMap = keyof MockPluginEventMap
> {
  type: TEventName;
  payload: MockPluginEventMap[TEventName];
}

export interface MockWriteCapture {
  readonly files: ReadonlyMap<string, MockStoredFile>;
  readonly writes: readonly MockWriteRecord[];
  seed(file: MockStoredFile): void;
  reset(): void;
  snapshot(workspaceId?: string): MockStoredFile[];
  read(path: string, workspaceId?: string): MockStoredFile | undefined;
  getRevision(path: string, workspaceId?: string): string | undefined;
  listWrittenPaths(workspaceId?: string): string[];
  write(input: WriteFileInput): Promise<MockWriteQueuedResponse>;
}

export interface MockRelayFileClient extends Pick<RelayFileClient, "writeFile"> {
  readonly writeCapture: MockWriteCapture;
  readonly routedWebhooks: MockNormalizedWebhook[];
  readFile(path: string, workspaceId?: string): MockStoredFile | undefined;
  registerAdapter(adapter: MockPluginAdapter): void;
  unregisterAdapter(name: string): boolean;
  getAdapter(name: string): MockPluginAdapter | undefined;
  routeWebhook(workspaceId: string, event: MockNormalizedWebhook): Promise<MockIngestResult>;
  listWrittenPaths(workspaceId?: string): string[];
  getWrittenRevision(path: string, workspaceId?: string): string | undefined;
  getLifecycleEvents(): MockLifecycleEventRecord[];
  on<TEventName extends keyof MockPluginEventMap>(
    eventName: TEventName,
    listener: (payload: MockPluginEventMap[TEventName]) => void
  ): () => void;
}

export interface CreateInMemoryWriteCaptureOptions {
  initialFiles?: Iterable<MockStoredFile>;
  revisionPrefix?: string;
  opPrefix?: string;
}

export interface CreateMockRelayFileClientOptions extends CreateInMemoryWriteCaptureOptions {}

export interface CreateMockConnectionProviderOptions {
  name?: string;
  healthCheckResult?: boolean;
  proxyResponse?: MockProxyResponse;
}

export interface MakeRegisteredMockPluginOptions {
  name?: string;
  version?: string;
  providerName?: string;
  connectionId?: string;
  contentType?: string;
  supportedEvents?: string[];
  proxyResponse?: MockProxyResponse;
  computePath?: (event: MockNormalizedWebhook) => string;
  computeSemantics?: (event: MockNormalizedWebhook) => FileSemantics;
  renderContent?: (event: MockNormalizedWebhook) => string;
  ingestWebhook?: (context: {
    client: MockRelayFileClient;
    adapter: MockPluginAdapter;
    workspaceId: string;
    event: MockNormalizedWebhook;
  }) => Promise<MockIngestResult> | MockIngestResult;
}

function toFileKey(workspaceId: string, path: string): string {
  return `${workspaceId}:${path}`;
}

function cloneStoredFile(file: MockStoredFile): MockStoredFile {
  return {
    ...file,
    semantics: file.semantics
      ? {
          properties: file.semantics.properties ? { ...file.semantics.properties } : undefined,
          relations: file.semantics.relations ? [...file.semantics.relations] : undefined,
          permissions: file.semantics.permissions ? [...file.semantics.permissions] : undefined,
          comments: file.semantics.comments ? [...file.semantics.comments] : undefined
        }
      : undefined
  };
}

function describeValue(value: unknown): string {
  return JSON.stringify(value, null, 2) ?? String(value);
}

function arraysEqual(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) {
    return false;
  }
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) {
      return false;
    }
  }
  return true;
}

function createAssertionError(message: string): Error {
  const error = new Error(message);
  error.name = "MockSystemAssertionError";
  return error;
}

function createRevisionConflictError(path: string, expected: string, actual: string | undefined): Error {
  const error = new Error(
    `Revision conflict for ${path}: expected ${expected}, found ${actual ?? "<missing>"}`
  );
  error.name = "MockRevisionConflictError";
  return error;
}

function createPluginEventEmitter() {
  const listeners: {
    [TEventName in keyof MockPluginEventMap]: Set<(payload: MockPluginEventMap[TEventName]) => void>;
  } = {
    registered: new Set(),
    ingested: new Set(),
    error: new Set()
  };
  const records: MockLifecycleEventRecord[] = [];

  return {
    emit<TEventName extends keyof MockPluginEventMap>(
      type: TEventName,
      payload: MockPluginEventMap[TEventName]
    ): void {
      records.push({ type, payload });
      for (const listener of listeners[type]) {
        listener(payload);
      }
    },
    on<TEventName extends keyof MockPluginEventMap>(
      type: TEventName,
      listener: (payload: MockPluginEventMap[TEventName]) => void
    ): () => void {
      listeners[type].add(listener);
      return () => {
        listeners[type].delete(listener);
      };
    },
    list(): MockLifecycleEventRecord[] {
      return records.slice();
    }
  };
}

export function createInMemoryWriteCapture(
  options: CreateInMemoryWriteCaptureOptions = {}
): MockWriteCapture {
  const files = new Map<string, MockStoredFile>();
  const writes: MockWriteRecord[] = [];
  const revisionPrefix = options.revisionPrefix ?? "rev";
  const opPrefix = options.opPrefix ?? "op";
  let revisionSequence = 0;
  let opSequence = 0;

  const seed = (file: MockStoredFile): void => {
    files.set(toFileKey(file.workspaceId, file.path), {
      ...cloneStoredFile(file),
      contentType: file.contentType ?? "text/markdown",
      encoding: file.encoding ?? "utf-8"
    });
  };

  for (const file of options.initialFiles ?? []) {
    seed(file);
  }
  revisionSequence = files.size;

  return {
    get files(): ReadonlyMap<string, MockStoredFile> {
      return files;
    },
    get writes(): readonly MockWriteRecord[] {
      return writes;
    },
    seed,
    reset(): void {
      files.clear();
      writes.length = 0;
      revisionSequence = 0;
      opSequence = 0;
    },
    snapshot(workspaceId?: string): MockStoredFile[] {
      return Array.from(files.values())
        .filter((file) => workspaceId === undefined || file.workspaceId === workspaceId)
        .map((file) => cloneStoredFile(file));
    },
    read(path: string, workspaceId?: string): MockStoredFile | undefined {
      if (workspaceId !== undefined) {
        const stored = files.get(toFileKey(workspaceId, path));
        return stored ? cloneStoredFile(stored) : undefined;
      }

      const values = Array.from(files.values());
      for (let index = values.length - 1; index >= 0; index -= 1) {
        const candidate = values[index];
        if (candidate?.path === path) {
          return cloneStoredFile(candidate);
        }
      }
      return undefined;
    },
    getRevision(path: string, workspaceId?: string): string | undefined {
      return this.read(path, workspaceId)?.revision;
    },
    listWrittenPaths(workspaceId?: string): string[] {
      return writes
        .filter((write) => workspaceId === undefined || write.workspaceId === workspaceId)
        .map((write) => write.path);
    },
    async write(input: WriteFileInput): Promise<MockWriteQueuedResponse> {
      const key = toFileKey(input.workspaceId, input.path);
      const previous = files.get(key);
      const previousRevision = previous?.revision ?? null;

      if (previous) {
        if (input.baseRevision !== previous.revision) {
          throw createRevisionConflictError(input.path, input.baseRevision, previous.revision);
        }
      } else if (input.baseRevision !== "0" && input.baseRevision !== "") {
        throw createRevisionConflictError(input.path, input.baseRevision, undefined);
      }

      revisionSequence += 1;
      opSequence += 1;

      const revision = `${revisionPrefix}_${revisionSequence}`;
      const opId = `${opPrefix}_${opSequence}`;
      const stored: MockStoredFile = {
        workspaceId: input.workspaceId,
        path: input.path,
        revision,
        contentType: input.contentType ?? "text/markdown",
        content: input.content,
        encoding: input.encoding ?? "utf-8",
        semantics: input.semantics
          ? {
              properties: input.semantics.properties ? { ...input.semantics.properties } : undefined,
              relations: input.semantics.relations ? [...input.semantics.relations] : undefined,
              permissions: input.semantics.permissions ? [...input.semantics.permissions] : undefined,
              comments: input.semantics.comments ? [...input.semantics.comments] : undefined
            }
          : undefined,
        correlationId: input.correlationId
      };
      const writeRecord: MockWriteRecord = {
        ...cloneStoredFile(stored),
        opId,
        baseRevision: input.baseRevision,
        previousRevision,
        existed: previous !== undefined,
        sequence: writes.length + 1
      };

      files.set(key, stored);
      writes.push(writeRecord);

      return {
        opId,
        status: "queued",
        targetRevision: revision
      };
    }
  };
}

export function createMockConnectionProvider(
  options: CreateMockConnectionProviderOptions = {}
): MockConnectionProvider {
  const proxyCalls: MockProxyRequest[] = [];
  const healthCheckCalls: string[] = [];
  const proxyResponse: MockProxyResponse = options.proxyResponse ?? {
    status: 200,
    headers: {},
    data: { ok: true }
  };

  return {
    name: options.name ?? "mock-provider",
    proxyCalls,
    healthCheckCalls,
    async proxy(request: MockProxyRequest): Promise<MockProxyResponse> {
      proxyCalls.push(request);
      return proxyResponse;
    },
    async healthCheck(connectionId: string): Promise<boolean> {
      healthCheckCalls.push(connectionId);
      return options.healthCheckResult ?? true;
    }
  };
}

export function createMockNormalizedWebhook(
  overrides: Partial<MockNormalizedWebhook> = {}
): MockNormalizedWebhook {
  return {
    provider: overrides.provider ?? "github",
    connectionId: overrides.connectionId ?? "conn_test",
    eventType: overrides.eventType ?? "created",
    objectType: overrides.objectType ?? "issues",
    objectId: overrides.objectId ?? "42",
    payload: overrides.payload ?? {
      id: overrides.objectId ?? "42",
      title: "Mock RelayFile event"
    },
    relations: overrides.relations ? [...overrides.relations] : undefined,
    metadata: overrides.metadata ? { ...overrides.metadata } : undefined
  };
}

export function buildMockPathFromWebhook(event: MockNormalizedWebhook): string {
  return computeCanonicalPath(event.provider, event.objectType, event.objectId);
}

export function buildMockSemanticsFromWebhook(event: MockNormalizedWebhook): FileSemantics {
  const properties: Record<string, string> = {
    provider: event.provider,
    "provider.object_type": event.objectType,
    "provider.object_id": event.objectId,
    "provider.event_type": event.eventType,
    "provider.connection_id": event.connectionId
  };

  for (const [key, value] of Object.entries(event.metadata ?? {})) {
    properties[`provider.metadata.${key}`] = value;
  }

  return {
    properties,
    relations: event.relations ? [...event.relations] : undefined
  };
}

export function renderMockFileContent(event: MockNormalizedWebhook): string {
  return JSON.stringify(
    {
      provider: event.provider,
      connectionId: event.connectionId,
      eventType: event.eventType,
      objectType: event.objectType,
      objectId: event.objectId,
      payload: event.payload,
      relations: event.relations ?? [],
      metadata: event.metadata ?? {}
    },
    null,
    2
  );
}

export function createMockRelayFileClient(
  options: CreateMockRelayFileClientOptions = {}
): MockRelayFileClient {
  const writeCapture = createInMemoryWriteCapture(options);
  const adapters = new Map<string, MockPluginAdapter>();
  const routedWebhooks: MockNormalizedWebhook[] = [];
  const events = createPluginEventEmitter();

  return {
    writeCapture,
    routedWebhooks,
    writeFile(input: WriteFileInput): Promise<MockWriteQueuedResponse> {
      return writeCapture.write(input);
    },
    readFile(path: string, workspaceId?: string): MockStoredFile | undefined {
      return writeCapture.read(path, workspaceId);
    },
    registerAdapter(adapter: MockPluginAdapter): void {
      if (adapters.has(adapter.name)) {
        throw new Error(`Adapter already registered: ${adapter.name}`);
      }
      adapters.set(adapter.name, adapter);
      events.emit("registered", {
        adapterName: adapter.name,
        version: adapter.version,
        providerName: adapter.provider.name
      });
    },
    unregisterAdapter(name: string): boolean {
      return adapters.delete(name);
    },
    getAdapter(name: string): MockPluginAdapter | undefined {
      return adapters.get(name);
    },
    async routeWebhook(workspaceId: string, event: MockNormalizedWebhook): Promise<MockIngestResult> {
      routedWebhooks.push(event);
      const adapter =
        adapters.get(event.provider) ??
        Array.from(adapters.values()).find((candidate) => {
          return candidate.name === event.provider || candidate.provider.name === event.provider;
        });

      if (!adapter) {
        const error = new Error(`No adapter registered for provider: ${event.provider}`);
        events.emit("error", { workspaceId, event, error });
        throw error;
      }

      try {
        const result = await adapter.ingestWebhook(workspaceId, event);
        events.emit("ingested", {
          adapterName: adapter.name,
          workspaceId,
          event,
          result
        });
        return result;
      } catch (error) {
        const normalizedError = error instanceof Error ? error : new Error(String(error));
        events.emit("error", {
          adapterName: adapter.name,
          workspaceId,
          event,
          error: normalizedError
        });
        throw normalizedError;
      }
    },
    listWrittenPaths(workspaceId?: string): string[] {
      return writeCapture.listWrittenPaths(workspaceId);
    },
    getWrittenRevision(path: string, workspaceId?: string): string | undefined {
      return writeCapture.getRevision(path, workspaceId);
    },
    getLifecycleEvents(): MockLifecycleEventRecord[] {
      return events.list();
    },
    on<TEventName extends keyof MockPluginEventMap>(
      eventName: TEventName,
      listener: (payload: MockPluginEventMap[TEventName]) => void
    ): () => void {
      return events.on(eventName, listener);
    }
  };
}

export function makeRegisteredMockPlugin(
  client: MockRelayFileClient,
  options: MakeRegisteredMockPluginOptions = {}
): MockPluginAdapter {
  const provider = createMockConnectionProvider({
    name: options.providerName ?? options.name ?? "github",
    proxyResponse: options.proxyResponse
  });
  const ingestedEvents: MockNormalizedWebhook[] = [];
  const writes: MockWriteRecord[] = [];

  const adapter: MockPluginAdapter = {
    name: options.name ?? provider.name,
    version: options.version ?? "0.0.0-test",
    provider,
    ingestedEvents,
    writes,
    computePath(event: MockNormalizedWebhook): string {
      return options.computePath ? options.computePath(event) : buildMockPathFromWebhook(event);
    },
    computeSemantics(event: MockNormalizedWebhook): FileSemantics {
      return options.computeSemantics ? options.computeSemantics(event) : buildMockSemanticsFromWebhook(event);
    },
    supportedEvents(): string[] {
      return options.supportedEvents?.slice() ?? [];
    },
    async ingestWebhook(workspaceId: string, event: MockNormalizedWebhook): Promise<MockIngestResult> {
      const normalizedEvent = options.connectionId
        ? { ...event, connectionId: options.connectionId }
        : event;

      ingestedEvents.push(normalizedEvent);

      if (options.ingestWebhook) {
        return options.ingestWebhook({
          client,
          adapter,
          workspaceId,
          event: normalizedEvent
        });
      }

      const path = adapter.computePath(normalizedEvent);
      const previousRevision = client.getWrittenRevision(path, workspaceId);
      const response = await client.writeFile({
        workspaceId,
        path,
        baseRevision: previousRevision ?? "0",
        content: options.renderContent
          ? options.renderContent(normalizedEvent)
          : renderMockFileContent(normalizedEvent),
        contentType: options.contentType ?? "application/json",
        semantics: adapter.computeSemantics(normalizedEvent)
      });

      const storedWrite = client.writeCapture.writes[client.writeCapture.writes.length - 1];
      if (storedWrite) {
        writes.push(storedWrite);
      }

      return {
        filesWritten: previousRevision ? 0 : 1,
        filesUpdated: previousRevision ? 1 : 0,
        filesDeleted: 0,
        paths: [path],
        errors: response.targetRevision ? [] : [{ path, error: "write failed" }]
      };
    }
  };

  client.registerAdapter(adapter);
  return adapter;
}

function resolveWriteCapture(subject: MockRelayFileClient | MockWriteCapture): MockWriteCapture {
  if ("writeCapture" in subject) {
    return subject.writeCapture;
  }
  return subject;
}

export function listWrittenPaths(
  subject: MockRelayFileClient | MockWriteCapture,
  workspaceId?: string
): string[] {
  return resolveWriteCapture(subject).listWrittenPaths(workspaceId);
}

export function getWrittenRevision(
  subject: MockRelayFileClient | MockWriteCapture,
  path: string,
  workspaceId?: string
): string | undefined {
  return resolveWriteCapture(subject).getRevision(path, workspaceId);
}

export function assertWrittenPaths(
  subject: MockRelayFileClient | MockWriteCapture,
  expectedPaths: readonly string[],
  workspaceId?: string
): void {
  const actualPaths = listWrittenPaths(subject, workspaceId);
  if (!arraysEqual(actualPaths, expectedPaths)) {
    throw createAssertionError(
      `Written paths mismatch.\nExpected: ${describeValue(expectedPaths)}\nActual: ${describeValue(actualPaths)}`
    );
  }
}

export function assertPathWasWritten(
  subject: MockRelayFileClient | MockWriteCapture,
  path: string,
  workspaceId?: string
): void {
  const actualPaths = listWrittenPaths(subject, workspaceId);
  if (!actualPaths.includes(path)) {
    throw createAssertionError(
      `Expected ${path} to be written.\nActual paths: ${describeValue(actualPaths)}`
    );
  }
}

export function assertWrittenRevision(
  subject: MockRelayFileClient | MockWriteCapture,
  path: string,
  expectedRevision: string,
  workspaceId?: string
): void {
  const actualRevision = getWrittenRevision(subject, path, workspaceId);
  if (actualRevision !== expectedRevision) {
    throw createAssertionError(
      `Revision mismatch for ${path}.\nExpected: ${expectedRevision}\nActual: ${actualRevision ?? "<missing>"}`
    );
  }
}

export function assertWrittenRevisions(
  subject: MockRelayFileClient | MockWriteCapture,
  expectedRevisions: Record<string, string>,
  workspaceId?: string
): void {
  for (const [path, revision] of Object.entries(expectedRevisions)) {
    assertWrittenRevision(subject, path, revision, workspaceId);
  }
}
