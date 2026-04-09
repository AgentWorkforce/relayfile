import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider } from "./connection.js";
import type { FileSemantics } from "./types.js";

export interface AdapterWebhookMetadata {
  deliveryId?: string;
  delivery_id?: string;
  timestamp?: string;
  [key: string]: unknown;
}

export interface AdapterWebhook {
  provider: string;
  connectionId?: string;
  eventType: string;
  objectType: string;
  objectId: string;
  payload: Record<string, unknown>;
  metadata?: AdapterWebhookMetadata;
  raw?: unknown;
}

export interface IngestError {
  path: string;
  error: string;
}

export interface IngestResult {
  filesWritten: number;
  filesUpdated: number;
  filesDeleted: number;
  paths: string[];
  errors: IngestError[];
}

export interface SyncOptions {
  cursor?: string;
  limit?: number;
  signal?: AbortSignal;
  [key: string]: unknown;
}

export interface SyncResult {
  filesWritten: number;
  filesUpdated: number;
  filesDeleted: number;
  paths?: string[];
  cursor?: string;
  nextCursor?: string | null;
  syncedObjectTypes?: string[];
  errors: Array<{
    path?: string;
    objectType?: string;
    error: string;
  }>;
}

export abstract class IntegrationAdapter {
  protected readonly client: RelayFileClient;
  protected readonly provider: ConnectionProvider;

  abstract readonly name: string;
  abstract readonly version: string;

  constructor(client: RelayFileClient, provider: ConnectionProvider) {
    this.client = client;
    this.provider = provider;
  }

  abstract ingestWebhook(workspaceId: string, event: AdapterWebhook): Promise<IngestResult>;
  abstract computePath(objectType: string, objectId: string): string;
  abstract computeSemantics(
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>,
  ): FileSemantics;
  supportedEvents?(): string[];
  writeBack?(workspaceId: string, path: string, content: string): Promise<unknown>;
  sync?(workspaceId: string, options?: SyncOptions): Promise<SyncResult>;
}
