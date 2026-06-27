import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider } from "./connection.js";
import type { FileSemantics } from "./types.js";
import type { WriteEvent } from "@relayfile/core";

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

export interface RelayBindingView {
  text: string;
  author?: string;
  skip?: boolean;
}

export interface RelayBinding {
  present?(event: WriteEvent): RelayBindingView | null;
  replyPathFor?(sourcePath: string): string | null;
}

const DEFAULT_PRESENT_TEXT_LIMIT = 500;

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
  relayBinding?: RelayBinding;
  sync?(workspaceId: string, options?: SyncOptions): Promise<SyncResult>;
}

export function genericPresent(event: WriteEvent, adapter: IntegrationAdapter): RelayBindingView {
  const provider = providerFromPath(event.path) || adapter.name;
  const { objectType, objectId } = objectIdentityFromPath(event.path);
  const payload = isRecord(event.value) ? event.value : {};
  let semantics: FileSemantics = {};
  try {
    semantics = adapter.computeSemantics(objectType, objectId, payload) ?? {};
  } catch {
    semantics = {};
  }

  const summary = summarizeForRelayBinding(event, semantics, payload);
  return {
    text: truncateText(summary, DEFAULT_PRESENT_TEXT_LIMIT),
    author: event.actor?.id || provider
  };
}

export function genericReplyPathFor(sourcePath: string): string | null {
  const normalized = sourcePath.trim().replace(/\/+$/, "");
  if (!normalized || !normalized.startsWith("/")) {
    return null;
  }
  return `${normalized}/replies/draft.json`;
}

function summarizeForRelayBinding(
  event: WriteEvent,
  semantics: FileSemantics,
  payload: Record<string, unknown>
): string {
  const semanticLines = [
    firstNonEmpty("status", semantics.properties?.["provider.status"], semantics.properties?.status),
    firstNonEmpty("updated", semantics.properties?.["provider.updated_at"], semantics.properties?.updatedAt),
    firstNonEmpty("title", semantics.properties?.title, semantics.properties?.name, semantics.properties?.subject)
  ].filter((line): line is string => line !== undefined);

  const relationCount = semantics.relations?.length ?? 0;
  const commentCount = semantics.comments?.length ?? 0;
  const permissionCount = semantics.permissions?.length ?? 0;
  const counts = [
    relationCount ? `${relationCount} relation${relationCount === 1 ? "" : "s"}` : undefined,
    commentCount ? `${commentCount} comment${commentCount === 1 ? "" : "s"}` : undefined,
    permissionCount ? `${permissionCount} permission${permissionCount === 1 ? "" : "s"}` : undefined
  ].filter((entry): entry is string => entry !== undefined);

  const payloadPreview = Object.keys(payload).length > 0
    ? `payload: ${JSON.stringify(payload)}`
    : undefined;

  return [
    `${event.operation} ${event.path}`,
    counts.length > 0 ? counts.join(", ") : undefined,
    ...semanticLines,
    payloadPreview
  ].filter((line): line is string => line !== undefined && line.trim() !== "").join("\n");
}

function firstNonEmpty(label: string, ...values: Array<string | undefined>): string | undefined {
  const value = values.find((candidate) => candidate && candidate.trim() !== "");
  return value ? `${label}: ${value}` : undefined;
}

function truncateText(value: string, limit: number): string {
  if (value.length <= limit) {
    return value;
  }
  return value.slice(0, limit);
}

function providerFromPath(path: string): string {
  return normalizePath(path)[0] ?? "";
}

function objectIdentityFromPath(path: string): { objectType: string; objectId: string } {
  const segments = normalizePath(path);
  const filename = segments[segments.length - 1] ?? "";
  return {
    objectType: segments[1] ?? "unknown",
    objectId: filename.replace(/\.[^.]+$/, "")
  };
}

function normalizePath(path: string): string[] {
  return path.split("/").filter(Boolean);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
