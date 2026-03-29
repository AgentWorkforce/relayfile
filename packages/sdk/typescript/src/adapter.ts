import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider, NormalizedWebhook } from "./provider.js";
import type { FileSemantics } from "./types.js";

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
  objectTypes?: string[];
  fullResync?: boolean;
  signal?: AbortSignal;
}

export interface SyncResult extends IngestResult {
  nextCursor?: string | null;
}

/**
 * Base contract for provider-specific Relayfile adapters.
 *
 * Adapters translate normalized provider events into Relayfile paths,
 * semantics, and optional bulk sync/write-back operations.
 */
export abstract class IntegrationAdapter {
  protected readonly client: RelayFileClient;
  protected readonly provider: ConnectionProvider;

  abstract readonly name: string;
  abstract readonly version: string;

  constructor(client: RelayFileClient, provider: ConnectionProvider) {
    this.client = client;
    this.provider = provider;
  }

  abstract ingestWebhook(
    workspaceId: string,
    event: NormalizedWebhook
  ): Promise<IngestResult>;

  abstract computePath(objectType: string, objectId: string): string;

  abstract computeSemantics(
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>
  ): FileSemantics;

  sync?(workspaceId: string, options?: SyncOptions): Promise<SyncResult>;

  writeBack?(workspaceId: string, path: string, content: string): Promise<void>;

  supportedEvents?(): string[];
}
