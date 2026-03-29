import { RelayFileApiError } from "../errors.js";
import { computeCanonicalPath, type ConnectionProvider, type NormalizedWebhook } from "../provider.js";
import type {
  FileReadResponse,
  FileSemantics,
  IngestWebhookInput,
  QueuedResponse,
  WriteFileInput,
  WriteQueuedResponse
} from "../types.js";

type MaybePromise<TValue> = TValue | Promise<TValue>;

export interface MockIngestResult {
  filesWritten: number;
  filesUpdated: number;
  filesDeleted: number;
  paths: string[];
  errors: Array<{
    path: string;
    error: string;
  }>;
}

export interface MockAdapterClient {
  readFile?(
    workspaceId: string,
    path: string
  ): Promise<FileReadResponse> | FileReadResponse;
  writeFile?(
    input: WriteFileInput
  ): Promise<WriteQueuedResponse> | WriteQueuedResponse;
  ingestWebhook?(
    input: IngestWebhookInput
  ): Promise<QueuedResponse> | QueuedResponse;
}

export interface MockAdapterCall {
  workspaceId: string;
  event: NormalizedWebhook;
}

export interface MockComputePathCall {
  objectType: string;
  objectId: string;
  event?: NormalizedWebhook;
}

export interface MockComputeSemanticsCall {
  objectType: string;
  objectId: string;
  payload: Record<string, unknown>;
  event?: NormalizedWebhook;
}

export interface MockWriteFileCall {
  kind: "writeFile";
  input: WriteFileInput;
  response: WriteQueuedResponse;
}

export interface MockRelayIngestCall {
  kind: "ingestWebhook";
  input: IngestWebhookInput;
  response: QueuedResponse;
}

export type MockAdapterWriteCall = MockWriteFileCall | MockRelayIngestCall;

export interface MockAdapterOverrides {
  name?: string;
  version?: string;
  supportedEvents?: string[];
  computePath?: (
    objectType: string,
    objectId: string,
    event?: NormalizedWebhook
  ) => string;
  computeSemantics?: (
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>,
    event?: NormalizedWebhook
  ) => FileSemantics;
  renderContent?: (event: NormalizedWebhook) => string;
  baseRevision?:
    | string
    | ((
        workspaceId: string,
        path: string,
        event: NormalizedWebhook
      ) => MaybePromise<string>);
  ingestWebhook?: (
    context: {
      client: MockAdapterClient;
      provider: ConnectionProvider;
      adapter: MockIntegrationAdapter;
      workspaceId: string;
      event: NormalizedWebhook;
    }
  ) => MaybePromise<MockIngestResult>;
}

export interface MockIntegrationAdapter {
  readonly name: string;
  readonly version: string;
  readonly provider: ConnectionProvider;
  readonly ingestWebhookCalls: MockAdapterCall[];
  readonly computePathCalls: MockComputePathCall[];
  readonly computeSemanticsCalls: MockComputeSemanticsCall[];
  readonly writes: MockAdapterWriteCall[];
  readonly results: MockIngestResult[];
  ingestWebhook(
    workspaceId: string,
    event: NormalizedWebhook
  ): Promise<MockIngestResult>;
  computePath(
    objectType: string,
    objectId: string,
    event?: NormalizedWebhook
  ): string;
  computeSemantics(
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>,
    event?: NormalizedWebhook
  ): FileSemantics;
  supportedEvents(): string[];
  getLastIngestCall(): MockAdapterCall | undefined;
  getLastWrite(): MockAdapterWriteCall | undefined;
  reset(): void;
}

function cloneWebhook(event: NormalizedWebhook): NormalizedWebhook {
  return {
    ...event,
    payload: { ...event.payload },
    relations: event.relations ? event.relations.slice() : undefined,
    metadata: event.metadata ? { ...event.metadata } : undefined
  };
}

function cloneSemantics(semantics: FileSemantics): FileSemantics {
  return {
    properties: semantics.properties ? { ...semantics.properties } : undefined,
    relations: semantics.relations ? semantics.relations.slice() : undefined,
    permissions: semantics.permissions ? semantics.permissions.slice() : undefined,
    comments: semantics.comments ? semantics.comments.slice() : undefined
  };
}

function cloneWriteFileInput(input: WriteFileInput): WriteFileInput {
  return {
    ...input,
    semantics: input.semantics ? cloneSemantics(input.semantics) : undefined
  };
}

function cloneIngestWebhookInput(input: IngestWebhookInput): IngestWebhookInput {
  return {
    ...input,
    headers: input.headers ? { ...input.headers } : undefined,
    data: input.data ? { ...input.data } : undefined
  };
}

function cloneIngestResult(result: MockIngestResult): MockIngestResult {
  return {
    filesWritten: result.filesWritten,
    filesUpdated: result.filesUpdated,
    filesDeleted: result.filesDeleted,
    paths: result.paths.slice(),
    errors: result.errors.map((error) => ({ ...error }))
  };
}

function buildDefaultSemantics(
  adapterName: string,
  objectType: string,
  objectId: string,
  payload: Record<string, unknown>,
  event?: NormalizedWebhook
): FileSemantics {
  const properties: Record<string, string> = {
    provider: adapterName,
    "provider.object_type": objectType,
    "provider.object_id": objectId
  };

  if (event?.eventType) {
    properties["provider.event_type"] = event.eventType;
  }
  if (event?.connectionId) {
    properties["provider.connection_id"] = event.connectionId;
  }
  if (typeof payload.status === "string") {
    properties["provider.status"] = payload.status;
  }

  return { properties };
}

function renderDefaultContent(event: NormalizedWebhook): string {
  return JSON.stringify(
    {
      provider: event.provider,
      connectionId: event.connectionId,
      eventType: event.eventType,
      objectType: event.objectType,
      objectId: event.objectId,
      payload: event.payload,
      metadata: event.metadata
    },
    null,
    2
  );
}

function isMissingFileError(error: unknown): boolean {
  if (error instanceof RelayFileApiError) {
    return error.status === 404 || error.code === "not_found";
  }

  if (typeof error !== "object" || error === null) {
    return false;
  }

  const candidate = error as {
    status?: unknown;
    code?: unknown;
    name?: unknown;
    message?: unknown;
  };

  return (
    candidate.status === 404 ||
    candidate.code === "not_found" ||
    candidate.code === "file_not_found" ||
    candidate.name === "NotFoundError" ||
    (typeof candidate.message === "string" &&
      candidate.message.toLowerCase().includes("not found"))
  );
}

async function resolveBaseRevision(
  client: MockAdapterClient,
  overrides: MockAdapterOverrides,
  workspaceId: string,
  path: string,
  event: NormalizedWebhook
): Promise<string> {
  if (typeof overrides.baseRevision === "function") {
    return overrides.baseRevision(workspaceId, path, event);
  }
  if (typeof overrides.baseRevision === "string") {
    return overrides.baseRevision;
  }
  if (typeof client.readFile !== "function") {
    return "0";
  }

  try {
    const existing = await client.readFile(workspaceId, path);
    return existing.revision;
  } catch (error) {
    if (isMissingFileError(error)) {
      return "0";
    }
    throw error;
  }
}

export function createMockAdapter(
  client: MockAdapterClient,
  provider: ConnectionProvider,
  overrides: MockAdapterOverrides = {}
): MockIntegrationAdapter {
  const ingestWebhookCalls: MockAdapterCall[] = [];
  const computePathCalls: MockComputePathCall[] = [];
  const computeSemanticsCalls: MockComputeSemanticsCall[] = [];
  const writes: MockAdapterWriteCall[] = [];
  const results: MockIngestResult[] = [];

  const adapter: MockIntegrationAdapter = {
    name: overrides.name ?? provider.name,
    version: overrides.version ?? "0.0.0-test",
    provider,
    ingestWebhookCalls,
    computePathCalls,
    computeSemanticsCalls,
    writes,
    results,
    computePath(
      objectType: string,
      objectId: string,
      event?: NormalizedWebhook
    ): string {
      computePathCalls.push({ objectType, objectId, event });
      return overrides.computePath
        ? overrides.computePath(objectType, objectId, event)
        : computeCanonicalPath(adapter.name, objectType, objectId);
    },
    computeSemantics(
      objectType: string,
      objectId: string,
      payload: Record<string, unknown>,
      event?: NormalizedWebhook
    ): FileSemantics {
      computeSemanticsCalls.push({
        objectType,
        objectId,
        payload: { ...payload },
        event
      });
      return overrides.computeSemantics
        ? overrides.computeSemantics(objectType, objectId, payload, event)
        : buildDefaultSemantics(adapter.name, objectType, objectId, payload, event);
    },
    supportedEvents(): string[] {
      return overrides.supportedEvents?.slice() ?? [];
    },
    async ingestWebhook(
      workspaceId: string,
      event: NormalizedWebhook
    ): Promise<MockIngestResult> {
      const capturedEvent = cloneWebhook(event);
      ingestWebhookCalls.push({ workspaceId, event: capturedEvent });

      if (overrides.ingestWebhook) {
        const overriddenResult = await overrides.ingestWebhook({
          client,
          provider,
          adapter,
          workspaceId,
          event: capturedEvent
        });
        const capturedResult = cloneIngestResult(overriddenResult);
        results.push(capturedResult);
        return capturedResult;
      }

      const path = adapter.computePath(
        capturedEvent.objectType,
        capturedEvent.objectId,
        capturedEvent
      );
      const semantics = adapter.computeSemantics(
        capturedEvent.objectType,
        capturedEvent.objectId,
        capturedEvent.payload,
        capturedEvent
      );
      const content = overrides.renderContent
        ? overrides.renderContent(capturedEvent)
        : renderDefaultContent(capturedEvent);
      const baseRevision = await resolveBaseRevision(
        client,
        overrides,
        workspaceId,
        path,
        capturedEvent
      );

      let filesWritten = baseRevision === "0" ? 1 : 0;
      let filesUpdated = baseRevision === "0" ? 0 : 1;

      if (typeof client.writeFile === "function") {
        const input: WriteFileInput = {
          workspaceId,
          path,
          baseRevision,
          content,
          contentType: "application/json",
          semantics
        };
        const response = await client.writeFile(input);
        writes.push({
          kind: "writeFile",
          input: cloneWriteFileInput(input),
          response
        });
      } else if (typeof client.ingestWebhook === "function") {
        const input: IngestWebhookInput = {
          workspaceId,
          provider: capturedEvent.provider || adapter.name,
          event_type: capturedEvent.eventType,
          path,
          data: {
            ...capturedEvent.payload,
            semantics: cloneSemantics(semantics)
          },
          headers: {
            "X-Mock-Connection-Id": capturedEvent.connectionId
          }
        };
        const response = await client.ingestWebhook(input);
        writes.push({
          kind: "ingestWebhook",
          input: cloneIngestWebhookInput(input),
          response
        });
      } else {
        throw new Error(
          "Mock adapter client must implement writeFile() or ingestWebhook()."
        );
      }

      const result: MockIngestResult = {
        filesWritten,
        filesUpdated,
        filesDeleted: 0,
        paths: [path],
        errors: []
      };
      const capturedResult = cloneIngestResult(result);
      results.push(capturedResult);
      return capturedResult;
    },
    getLastIngestCall(): MockAdapterCall | undefined {
      return ingestWebhookCalls.at(-1);
    },
    getLastWrite(): MockAdapterWriteCall | undefined {
      return writes.at(-1);
    },
    reset(): void {
      ingestWebhookCalls.length = 0;
      computePathCalls.length = 0;
      computeSemanticsCalls.length = 0;
      writes.length = 0;
      results.length = 0;
    }
  };

  return adapter;
}
