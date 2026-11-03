import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { WorkspaceRuntime } from "../src/runtime/workspace-runtime.js";
import type { ActorHttpRequest, ActorHttpResponse, CallerContext } from "../src/runtime/types.js";
import type { WorkspaceActors } from "../src/gateway/actors.js";
import { BearerVerifier, mintHs256 } from "../src/gateway/auth.js";
import { MemoryBlobStore } from "../src/gateway/blobs.js";
import { createGateway } from "../src/gateway/server.js";
import { FakeActorDb } from "./fake-actor-db.js";

export const JWT_SECRET = "test-secret-test-secret-test-secret";
export const HMAC_SECRET = "internal-hmac-secret";

/** Workspace actors in-process: one FakeActorDb per workspace, one call at a time per actor. */
export class LocalWorkspaceActors implements WorkspaceActors {
  readonly dbs = new Map<string, FakeActorDb>();
  readonly runtimes = new Map<string, WorkspaceRuntime>();
  private queues = new Map<string, Promise<unknown>>();
  /** Per-workspace alarm, like durable-actors' per-actor alarm port. */
  readonly alarms = new Map<string, number | null>();

  runtime(workspaceId: string): WorkspaceRuntime {
    let runtime = this.runtimes.get(workspaceId);
    if (!runtime) {
      const db = new FakeActorDb();
      this.dbs.set(workspaceId, db);
      runtime = new WorkspaceRuntime(db, workspaceId, {
        alarm: { get: () => this.alarms.get(workspaceId) ?? null, set: (ms) => void this.alarms.set(workspaceId, ms) },
      });
      this.runtimes.set(workspaceId, runtime);
    }
    return runtime;
  }

  async handle(workspaceId: string, request: ActorHttpRequest, caller: CallerContext): Promise<ActorHttpResponse> {
    const runtime = this.runtime(workspaceId);
    const db = this.dbs.get(workspaceId)!;
    // Serialize like an actor mailbox, and wrap in a transaction like the Workspace actor does.
    const previous = this.queues.get(workspaceId) ?? Promise.resolve();
    const next = previous
      .catch(() => undefined)
      .then(async () => {
        const response = await db.transaction(() => runtime.handle(request, caller));
        await runtime.flush();
        return response;
      });
    this.queues.set(workspaceId, next);
    // JSON round trip, as actor RPC does.
    return JSON.parse(JSON.stringify(await next)) as ActorHttpResponse;
  }

  async socketGrant(): Promise<string> {
    throw new Error("sockets are not available in-process");
  }

  async ready(): Promise<void> {}
}

export interface TestGateway {
  url: string;
  actors: LocalWorkspaceActors;
  blobs: MemoryBlobStore;
  close(): Promise<void>;
}

export async function startGateway(options: { inlineMaxBytes?: number; externalWritebackProviders?: string[]; blobs?: boolean } = {}): Promise<TestGateway> {
  const actors = new LocalWorkspaceActors();
  const blobs = new MemoryBlobStore();
  const server: Server = createGateway({
    actors,
    verifier: new BearerVerifier({ hs256Secret: JWT_SECRET }),
    blobStore: options.blobs === false ? null : blobs,
    blobPrefix: "relayfile/",
    inlineMaxBytes: options.inlineMaxBytes ?? 1024,
    maxBodyBytes: 32 * 1024 * 1024,
    internalHmacSecret: HMAC_SECRET,
    externalWritebackProviders: options.externalWritebackProviders ?? [],
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}`,
    actors,
    blobs,
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

export function token(workspaceId: string, scopes: string[] = ["fs:read", "fs:write", "ops:read", "sync:read", "sync:trigger"], sub = "agent-1"): string {
  return mintHs256(JWT_SECRET, {
    wks: workspaceId,
    sub,
    aud: ["relayfile"],
    scopes,
    exp: Math.floor(Date.now() / 1000) + 3600,
  });
}

export async function api(
  gateway: TestGateway,
  method: string,
  path: string,
  options: { token?: string; body?: unknown; headers?: Record<string, string>; correlation?: boolean } = {},
): Promise<{ status: number; headers: Headers; json: any }> {
  const headers: Record<string, string> = { ...(options.headers ?? {}) };
  if (options.token) headers.authorization = `Bearer ${options.token}`;
  if (options.correlation !== false) headers["x-correlation-id"] ??= "corr-test";
  if (options.body !== undefined) headers["content-type"] ??= "application/json";
  const response = await fetch(`${gateway.url}${path}`, {
    method,
    headers,
    body: options.body === undefined ? undefined : typeof options.body === "string" ? options.body : JSON.stringify(options.body),
  });
  const text = await response.text();
  let json: unknown = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = text;
  }
  return { status: response.status, headers: response.headers, json };
}
