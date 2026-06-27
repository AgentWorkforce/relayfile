import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";

import {
  genericPresent,
  genericReplyPathFor,
  onWrite,
  type IntegrationAdapter,
  type RelayFileClient,
  type WriteEvent,
} from "@relayfile/sdk";

export interface RelayBindingRecord {
  provider: string;
  pathGlob: string;
  channel: string;
  webhookId: string;
  webhookToken: string;
}

export interface RelayCastLike {
  webhooks: {
    trigger(webhookId: string, body: Record<string, unknown>, token?: string): Promise<unknown>;
  };
}

export interface ForwarderOptions {
  workspaceId: string;
  client: RelayFileClient;
  relayCastSdk: RelayCastLike;
  adapter: IntegrationAdapter;
  bindings?: RelayBindingRecord[];
  bindingsPath?: string;
  signal?: AbortSignal;
}

export interface ForwarderHandle {
  unsubscribe(): void;
}

export interface RelaycastDelivery {
  message?: {
    text?: unknown;
    body?: unknown;
    content?: unknown;
    metadata?: unknown;
  };
}

export function defaultBindingsPath(): string {
  return join(homedir(), ".relayfile", "bindings.json");
}

export async function loadRelayBindings(bindingsPath = defaultBindingsPath()): Promise<RelayBindingRecord[]> {
  let payload: string;
  try {
    payload = await readFile(bindingsPath, "utf-8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return [];
    }
    throw error;
  }

  const parsed = JSON.parse(payload) as unknown;
  const entries = Array.isArray(parsed)
    ? parsed
    : isRecord(parsed) && Array.isArray(parsed.bindings)
      ? parsed.bindings
      : [];

  return entries.filter(isRelayBindingRecord);
}

export async function startForwarder(opts: ForwarderOptions): Promise<ForwarderHandle> {
  const bindings = opts.bindings ?? await loadRelayBindings(opts.bindingsPath);
  const handler = createInboundForwardHandler({
    adapter: opts.adapter,
    bindings,
    relayCastSdk: opts.relayCastSdk,
  });
  const unsubscribe = onWrite("/**", handler, {
    client: opts.client,
    workspaceId: opts.workspaceId,
    operations: ["create", "update", "delete"],
    signal: opts.signal,
  });
  return { unsubscribe };
}

export function createInboundForwardHandler(input: {
  adapter: IntegrationAdapter;
  bindings: RelayBindingRecord[];
  relayCastSdk: RelayCastLike;
}): (event: WriteEvent) => Promise<void> {
  return async (event) => {
    if (event.operation === "delete" || isRelayfileWritebackActor(event)) {
      return;
    }

    const binding = matchBinding(event.path, input.bindings);
    if (!binding) {
      return;
    }

    const view = input.adapter.relayBinding?.present
      ? input.adapter.relayBinding.present(event)
      : genericPresent(event, input.adapter);
    if (!view || view.skip) {
      return;
    }

    await input.relayCastSdk.webhooks.trigger(
      binding.webhookId,
      {
        text: view.text,
        author: view.author,
        source: binding.provider,
        payload: {
          relayfile: {
            provider: binding.provider,
            path: event.path,
            revision: event.revision,
          },
        },
      },
      binding.webhookToken,
    );
  };
}

export async function handleWritebackDelivery(
  body: RelaycastDelivery | Record<string, unknown>,
  adapter: IntegrationAdapter,
  workspaceId: string,
  bindings: RelayBindingRecord[],
): Promise<void> {
  const message = isRecord(body.message) ? body.message : undefined;
  const metadata = isRecord(message?.metadata) ? message.metadata : undefined;
  if (metadata?.__relaycast_origin === "inbound_webhook") {
    return;
  }

  const relayfile = isRecord(metadata?.relayfile) ? metadata.relayfile : undefined;
  const path = typeof relayfile?.path === "string" ? relayfile.path : "";
  if (!path) {
    return;
  }

  const binding = matchBinding(path, bindings);
  if (!binding) {
    return;
  }

  const replyPath = adapter.relayBinding?.replyPathFor
    ? adapter.relayBinding.replyPathFor(path)
    : genericReplyPathFor(path);
  if (!replyPath || typeof adapter.writeBack !== "function") {
    return;
  }

  await adapter.writeBack(workspaceId, replyPath, messageText(message));
}

export function matchBinding(path: string, bindings: RelayBindingRecord[]): RelayBindingRecord | undefined {
  return bindings.find((binding) => globMatches(binding.pathGlob, path));
}

function globMatches(glob: string, path: string): boolean {
  const normalizedGlob = normalizePath(glob);
  const normalizedPath = normalizePath(path);
  if (normalizedGlob === "/**" || normalizedGlob === "**") {
    return true;
  }
  if (normalizedGlob.endsWith("/**")) {
    const prefix = normalizedGlob.slice(0, -3);
    return normalizedPath === prefix || normalizedPath.startsWith(`${prefix}/`);
  }
  const globSegments = normalizedGlob.split("/").filter(Boolean);
  const pathSegments = normalizedPath.split("/").filter(Boolean);
  if (globSegments.length !== pathSegments.length) {
    return false;
  }
  return globSegments.every((segment, index) => segment === "*" || segment === pathSegments[index]);
}

function normalizePath(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    return "/";
  }
  const withSlash = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return withSlash.replace(/\/+$/, "") || "/";
}

function isRelayfileWritebackActor(event: WriteEvent): boolean {
  return event.actor?.id === "__relayfile_writeback__" || event.actor?.id === "relayfile-writeback";
}

function messageText(message: Record<string, unknown> | undefined): string {
  for (const key of ["text", "body", "content"]) {
    const value = message?.[key];
    if (typeof value === "string") {
      return value;
    }
  }
  return "";
}

function isRelayBindingRecord(value: unknown): value is RelayBindingRecord {
  if (!isRecord(value)) {
    return false;
  }
  return (
    typeof value.provider === "string" &&
    typeof value.pathGlob === "string" &&
    typeof value.channel === "string" &&
    typeof value.webhookId === "string" &&
    typeof value.webhookToken === "string"
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
