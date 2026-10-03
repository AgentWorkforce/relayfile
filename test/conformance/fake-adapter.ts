import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { WebSocketServer, type WebSocket } from "ws";
import { CAPABILITIES, CONTROL_OPERATIONS, type ResolvedTarget } from "./types.js";
import type { AdvancedCaseId } from "./scenarios.js";

interface StoredFile {
  content: string;
  revision: string;
  contentType: string;
}

interface FakeEvent {
  eventId: string;
  type: "file.created" | "file.updated" | "file.deleted";
  path: string;
  revision: string;
  origin: "provider_sync" | "agent_write" | "system";
  correlationId: string;
  timestamp: string;
}

interface Operation {
  opId: string;
  state: "pending" | "succeeded";
  writebackAttempts: number;
  path?: string;
}

interface ProviderCall {
  attempts: number;
  state?: string;
  held?: boolean;
  crashReady?: boolean;
  crashPhase?: string;
  writebackAttempts?: number;
  echoDeliveries?: number;
}

interface WebhookBody {
  path: string;
  event_type?: string;
  delivery_id?: string;
  data?: Record<string, unknown>;
}

interface PendingWebhook {
  body: WebhookBody;
  response: ServerResponse;
  correlationId: string;
}

interface PendingCrash {
  path: string;
  phase: "before-commit" | "after-commit";
  response: ServerResponse;
}

interface PendingFailoverWrite {
  path: string;
  content: string;
  ifMatch: string;
  response: ServerResponse;
  correlationId: string;
}

interface MountState {
  id: string;
  mode: "read" | "write" | "mirror";
  root: string;
  known: Map<string, string>;
  timer: ReturnType<typeof setInterval>;
}

interface FakeConfig {
  ingestFailures?: number;
  matchPath?: string;
  permanentIngestFailurePath?: string | null;
  ingestBackpressure?: { count: number; retryAfterSeconds: number };
  backpressureRemaining?: number;
  backpressureUntil?: number;
  echoWritebackWebhook?: boolean;
  holdIngestPath?: string;
  crashBarrier?: { matchPath: string; phase: "before-commit" | "after-commit" };
}

const PRIMARY_WORKSPACE = "fake-primary";
const SECONDARY_WORKSPACE = "fake-secondary";
const PRIMARY_TOKEN = "fake-primary-token";
const CONTROL_TOKEN = "fake-control-token";
const ADMIN_KEY = "fake-admin-key";

export class FakeConformanceAdapter {
  private server?: Server;
  private wsServer?: WebSocketServer;
  private baseUrl = "";
  private root = "";
  private revision = 0;
  private eventSequence = 0;
  private operationSequence = 0;
  private virtualNow = Date.parse("2026-01-02T12:00:00.000Z");
  private servingRuntime = "terse-durable-actors";
  private readonly files = new Map<string, StoredFile>();
  private readonly events: FakeEvent[] = [];
  private readonly operations = new Map<string, Operation>();
  private readonly calls = new Map<string, ProviderCall>();
  private readonly identities = new Map<string, { expiresAt: number; revision: string; opId: string }>();
  private readonly deadLetters = new Map<string, { deliveryId: string; envelopeId: string; body?: WebhookBody }>();
  private readonly sockets = new Set<WebSocket>();
  private readonly mounts = new Map<string, MountState>();
  private readonly traceRecords: unknown[] = [];
  private config: FakeConfig = {};
  private heldWebhook?: PendingWebhook;
  private pendingCrash?: PendingCrash;
  private failover?: { id: string; writes: PendingFailoverWrite[] };
  private pendingMigrationId?: string;
  private exportedArtifact?: string;

  constructor(readonly mutant?: AdvancedCaseId) {}

  async start(): Promise<{ target: ResolvedTarget; close: () => Promise<void> }> {
    this.root = await mkdtemp(join(tmpdir(), "relayfile-conformance-fake-"));
    this.server = createServer((request, response) => {
      void this.handle(request, response).catch((error: unknown) => {
        if (!response.headersSent) this.json(response, 500, this.error(request, "fake_adapter_error", String(error)));
        else response.destroy(error instanceof Error ? error : new Error(String(error)));
      });
    });
    this.wsServer = new WebSocketServer({ noServer: true });
    this.server.on("upgrade", (request, socket, head) => this.upgrade(request, socket, head));
    await new Promise<void>((resolve) => this.server!.listen(0, "127.0.0.1", resolve));
    const address = this.server.address();
    if (!address || typeof address === "string") throw new Error("fake adapter did not bind a TCP port");
    this.baseUrl = `http://127.0.0.1:${address.port}`;
    return { target: this.target(), close: () => this.close() };
  }

  private target(): ResolvedTarget {
    return {
      id: this.mutant ? `fake-${this.mutant.toLowerCase()}` : "fake-conforming",
      runtime: { kind: "terse-durable-actors", version: "fake-v1" },
      relayfileSha: "fake-sha",
      baseUrl: this.baseUrl,
      workspaces: { primary: PRIMARY_WORKSPACE, secondary: SECONDARY_WORKSPACE },
      tokens: {
        primary: PRIMARY_TOKEN,
        secondary: "fake-secondary-token",
        pathScoped: "fake-path-token",
        readOnly: "fake-read-only-token",
      },
      capabilities: new Set(CAPABILITIES),
      control: {
        baseUrl: this.baseUrl,
        token: CONTROL_TOKEN,
        operations: new Set(CONTROL_OPERATIONS),
      },
      runtimeVerification: {
        baseUrl: this.baseUrl,
        adminKey: ADMIN_KEY,
        projectId: "fake-project",
        actorName: "RelayfileWorkspace",
        actorId: PRIMARY_WORKSPACE,
      },
      sourcePath: "in-process fake adapter",
    };
  }

  private async close(): Promise<void> {
    for (const mount of this.mounts.values()) clearInterval(mount.timer);
    this.mounts.clear();
    for (const socket of this.sockets) socket.close();
    await new Promise<void>((resolve) => this.wsServer?.close(() => resolve()));
    await new Promise<void>((resolve, reject) => this.server?.close((error) => error ? reject(error) : resolve()));
    await rm(this.root, { recursive: true, force: true });
  }

  private async handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const url = new URL(request.url ?? "/", this.baseUrl);
    const correlationId = String(request.headers["x-correlation-id"] ?? "fake-correlation");
    if (url.pathname.startsWith("/v1/conformance/")) {
      if (request.headers.authorization !== `Bearer ${CONTROL_TOKEN}`) {
        this.json(response, 401, this.error(request, "unauthorized", "control token required"));
        return;
      }
      const operation = decodeURIComponent(url.pathname.slice("/v1/conformance/".length));
      const body = await this.body(request);
      await this.control(operation, body, request, response);
      return;
    }
    if (url.pathname.includes("/observe/state")) {
      if (request.headers.authorization !== `Bearer ${ADMIN_KEY}`) {
        this.json(response, 401, { code: "unauthorized" });
        return;
      }
      this.json(response, 200, { snapshot: { servingRuntime: this.servingRuntime }, schema: { version: 1 } });
      return;
    }
    if (url.pathname.includes("/observe/requests")) {
      if (request.headers.authorization !== `Bearer ${ADMIN_KEY}`) {
        this.json(response, 401, { code: "unauthorized" });
        return;
      }
      this.json(response, 200, {
        dropped: 0,
        evicted: 0,
        persistenceFailed: false,
        records: this.traceRecords,
        nextCursor: null,
        reset: false,
      });
      return;
    }
    if (/\/actors\/[^/]+\/[^/]+\/invoke$/u.test(url.pathname)) {
      if (this.mutant === "RF-AUTH-005" && !request.headers.authorization) {
        this.traceRecords.push({ requestId: "mutant-auth-dispatch" });
        this.json(response, 200, { ok: true });
      } else {
        this.json(response, 401, { code: "unauthorized" });
      }
      return;
    }

    const workspaceMatch = /^\/v1\/workspaces\/([^/]+)(\/.*)$/u.exec(url.pathname);
    if (!workspaceMatch) {
      this.json(response, 404, this.error(request, "not_found", "not found"));
      return;
    }
    const workspace = decodeURIComponent(workspaceMatch[1]!);
    const suffix = workspaceMatch[2]!;
    if (workspace !== PRIMARY_WORKSPACE) {
      this.json(response, 403, this.error(request, "forbidden", "workspace denied"));
      return;
    }
    if (request.headers.authorization !== `Bearer ${PRIMARY_TOKEN}`) {
      this.json(response, 401, this.error(request, "unauthorized", "bearer token required"));
      return;
    }
    if (suffix === "/fs/file" && request.method === "GET") {
      const path = url.searchParams.get("path") ?? "";
      const file = this.files.get(path);
      if (!file) {
        this.json(response, 404, this.error(request, "not_found", "file not found"));
        return;
      }
      this.json(response, 200, { path, ...file }, { ETag: file.revision });
      return;
    }
    if (suffix === "/fs/file" && request.method === "PUT") {
      const path = url.searchParams.get("path") ?? "";
      const body = await this.body(request);
      const content = String(body.content ?? "");
      const crash = this.config.crashBarrier;
      if (crash?.matchPath === path) {
        if (crash.phase === "after-commit") this.write(path, content, "agent_write", correlationId);
        this.calls.set(path, { attempts: 1, crashReady: true, crashPhase: crash.phase });
        this.pendingCrash = { path, phase: crash.phase, response };
        return;
      }
      if (this.failover) {
        this.failover.writes.push({ path, content, ifMatch: String(request.headers["if-match"] ?? ""), response, correlationId });
        return;
      }
      const current = this.files.get(path);
      const ifMatch = String(request.headers["if-match"] ?? "");
      if (!ifMatch) {
        this.json(response, 412, this.error(request, "precondition_required", "If-Match required"));
        return;
      }
      if (ifMatch !== "*" && current?.revision !== ifMatch) {
        this.json(response, 409, {
          ...this.error(request, "conflict", "revision conflict"),
          expectedRevision: ifMatch,
          currentRevision: current?.revision ?? "0",
        });
        return;
      }
      const receipt = this.write(path, content, "agent_write", correlationId);
      const calls = this.calls.get(path) ?? { attempts: 0 };
      if (this.config.matchPath === path && this.config.echoWritebackWebhook) {
        calls.writebackAttempts = this.mutant === "RF-PROJ-003" ? 2 : 1;
        calls.echoDeliveries = 1;
        this.calls.set(path, calls);
      }
      this.json(response, 202, receipt);
      return;
    }
    if (suffix === "/fs/bulk" && request.method === "POST") {
      const body = await this.body(request);
      const files = Array.isArray(body.files) ? body.files as Array<Record<string, unknown>> : [];
      const results: Array<Record<string, unknown>> = [];
      for (const [index, item] of files.entries()) {
        const path = String(item.path ?? "");
        const identity = item.contentIdentity as Record<string, unknown> | undefined;
        const identityKey = identity ? `${String(identity.kind)}:${String(identity.key)}` : undefined;
        const cached = identityKey ? this.identities.get(identityKey) : undefined;
        if (cached && cached.expiresAt > this.virtualNow) {
          results.push({ path, revision: cached.revision, contentType: "text/plain", opId: cached.opId, contentIdentity: identity });
          continue;
        }
        const receipt = this.write(path, String(item.content ?? ""), "agent_write", correlationId, {
          suppressBroadcast: this.mutant === "RF-LIMIT-001" && index === files.length - 1,
        });
        results.push({ path, revision: receipt.targetRevision, contentType: String(item.contentType ?? "text/plain"), opId: receipt.opId, ...(identity ? { contentIdentity: identity } : {}) });
        if (identityKey) {
          const ttl = Number(identity?.ttlSeconds ?? 0) * 1_000;
          this.identities.set(identityKey, { expiresAt: this.virtualNow + ttl, revision: receipt.targetRevision, opId: receipt.opId });
        }
      }
      this.json(response, 202, { written: files.length, errorCount: 0, errors: [], results, correlationId });
      return;
    }
    if (suffix === "/fs/events" && request.method === "GET") {
      const cursor = url.searchParams.get("cursor");
      const index = cursor ? this.events.findIndex((event) => event.eventId === cursor) + 1 : 0;
      this.json(response, 200, { events: this.events.slice(Math.max(index, 0)), nextCursor: null });
      return;
    }
    if (suffix === "/webhooks/ingest" && request.method === "POST") {
      const body = await this.body(request) as unknown as WebhookBody;
      const path = body.path;
      if (this.config.matchPath === path && (this.config.backpressureRemaining ?? 0) > 0) {
        this.config.backpressureRemaining!--;
        this.config.backpressureUntil = this.virtualNow + Number(this.config.ingestBackpressure?.retryAfterSeconds ?? 1) * 1_000;
        this.json(response, 429, this.error(request, "backpressure", "retry later"), {
          "Retry-After": String(this.config.ingestBackpressure?.retryAfterSeconds ?? 1),
        });
        return;
      }
      if (this.config.holdIngestPath === path) {
        this.calls.set(path, { attempts: 1, held: true });
        this.heldWebhook = { body, response, correlationId };
        return;
      }
      if (this.heldWebhook && this.mutant === "RF-SER-001") {
        this.json(response, 500, this.error(request, "serialization_blocked", "independent object was blocked"));
        return;
      }
      if (this.config.permanentIngestFailurePath === path) {
        this.calls.set(path, { attempts: 1, state: "retrying" });
        this.deadLetters.set(path, { deliveryId: String(body.delivery_id ?? ""), envelopeId: String(body.delivery_id ?? ""), body });
        this.json(response, 202, { status: "queued", id: String(body.delivery_id ?? path), correlationId });
        return;
      }
      if ((this.config.ingestFailures ?? 0) > 0 && this.config.matchPath === path) {
        this.calls.set(path, { attempts: 1, state: "retrying" });
        this.deadLetters.set(`retry:${path}`, { deliveryId: String(body.delivery_id ?? ""), envelopeId: `retry:${path}`, body });
        this.json(response, 202, { status: "queued", id: String(body.delivery_id ?? path), correlationId });
        return;
      }
      if (this.mutant === "RF-QUEUE-002" && this.config.permanentIngestFailurePath) {
        this.json(response, 202, { status: "queued", id: String(body.delivery_id ?? path), correlationId });
        return;
      }
      this.applyWebhook(body, correlationId);
      this.json(response, 202, { status: "queued", id: String(body.delivery_id ?? path), correlationId });
      return;
    }
    const operationMatch = /^\/ops\/(.+)$/u.exec(suffix);
    if (operationMatch && request.method === "GET") {
      const opId = decodeURIComponent(operationMatch[1]!);
      const operation = this.operations.get(opId);
      if (!operation) {
        this.json(response, 404, this.error(request, "not_found", "operation not found"));
        return;
      }
      this.json(response, 200, { opId, status: operation.state, attemptCount: operation.writebackAttempts });
      return;
    }
    const replayMatch = /^\/sync\/dead-letter\/(.+)\/replay$/u.exec(suffix);
    if (replayMatch && request.method === "POST") {
      const envelopeId = decodeURIComponent(replayMatch[1]!);
      const item = [...this.deadLetters.values()].find((candidate) => candidate.envelopeId === envelopeId);
      if (!item) {
        this.json(response, 404, this.error(request, "not_found", "dead letter not found"));
        return;
      }
      this.deadLetters.set(`replay:${envelopeId}`, item);
      this.json(response, 202, { status: "queued", id: envelopeId, correlationId });
      return;
    }
    this.json(response, 404, this.error(request, "not_found", "route not found"));
  }

  private async control(
    operation: string,
    body: Record<string, unknown>,
    request: IncomingMessage,
    response: ServerResponse,
  ): Promise<void> {
    if (operation === "reset") {
      this.reset();
      this.json(response, 200, {});
      return;
    }
    if (operation === "provider.configure") {
      if (body.releaseIngestPath && this.heldWebhook?.body.path === body.releaseIngestPath) {
        const held = this.heldWebhook;
        this.heldWebhook = undefined;
        this.applyWebhook(held.body, held.correlationId);
        this.calls.set(held.body.path, { attempts: 1, held: false });
        this.json(held.response, 202, { status: "queued", id: String(held.body.delivery_id ?? held.body.path), correlationId: held.correlationId });
      }
      this.config.ingestFailures = body.ingestFailures === undefined ? 0 : Number(body.ingestFailures);
      if (body.matchPath !== undefined) this.config.matchPath = String(body.matchPath);
      if (body.permanentIngestFailurePath !== undefined) this.config.permanentIngestFailurePath = body.permanentIngestFailurePath === null ? null : String(body.permanentIngestFailurePath);
      if (body.ingestBackpressure) {
        this.config.ingestBackpressure = body.ingestBackpressure as { count: number; retryAfterSeconds: number };
        this.config.backpressureRemaining = this.config.ingestBackpressure.count;
      }
      if (body.echoWritebackWebhook !== undefined) this.config.echoWritebackWebhook = Boolean(body.echoWritebackWebhook);
      if (body.holdIngestPath !== undefined) this.config.holdIngestPath = String(body.holdIngestPath);
      if (body.releaseIngestPath !== undefined) this.config.holdIngestPath = undefined;
      if (body.crashBarrier) this.config.crashBarrier = body.crashBarrier as FakeConfig["crashBarrier"];
      const seed = body.seedMigrationState as Record<string, unknown> | undefined;
      if (seed) {
        const pending = String(seed.pendingOutboxId);
        const deadLetterId = String(seed.deadLetterId);
        this.pendingMigrationId = pending;
        this.operations.set(pending, { opId: pending, state: "pending", writebackAttempts: 0 });
        const envelopeId = this.mutant === "RF-MIG-001" ? `${deadLetterId}-mutated` : deadLetterId;
        this.deadLetters.set(deadLetterId, { deliveryId: deadLetterId, envelopeId });
      }
      this.json(response, 200, { configured: true });
      return;
    }
    if (operation === "provider.calls") {
      const path = String(body.matchPath ?? "");
      this.json(response, 200, this.calls.get(path) ?? { attempts: 0, state: "idle", held: false });
      return;
    }
    if (operation === "state.inspect") {
      const paths = Array.isArray(body.paths) ? body.paths.map(String) : [];
      const inspectedFiles = Object.fromEntries(paths.map((path) => {
        const file = this.files.get(path);
        return [path, file ? { exists: true, content: file.content, revision: file.revision } : { exists: false }];
      }));
      const eventCounts = Object.fromEntries(paths.map((path) => [path, this.events.filter((event) => event.path === path).length]));
      const identityBody = body.contentIdentity as Record<string, unknown> | undefined;
      const identityKey = identityBody ? `${String(identityBody.kind)}:${String(identityBody.key)}` : undefined;
      const operationIds = Array.isArray(body.operationIds) ? new Set(body.operationIds.map(String)) : undefined;
      const operations = [...this.operations.values()].filter((operation) =>
        operationIds ? operationIds.has(operation.opId) : operation.path !== undefined && paths.includes(operation.path));
      this.json(response, 200, {
        files: inspectedFiles,
        eventCounts,
        deadLetters: [...this.deadLetters.values()].map(({ deliveryId, envelopeId }) => ({ deliveryId, envelopeId })),
        backpressureActive: this.mutant === "RF-QUEUE-003" || (this.config.backpressureUntil ?? 0) > this.virtualNow,
        identityActive: identityKey ? (this.mutant === "RF-TIMER-002" || (this.identities.get(identityKey)?.expiresAt ?? 0) > this.virtualNow) : undefined,
        servingRuntime: this.servingRuntime,
        operations,
      });
      return;
    }
    if (operation === "clock.advance") {
      this.virtualNow += Number(body.milliseconds ?? 0);
      await this.advanceClock(Number(body.milliseconds ?? 0));
      this.json(response, 200, { now: new Date(this.virtualNow).toISOString() });
      return;
    }
    if (operation === "runtime.evict") {
      for (const socket of this.sockets) socket.close(1012, "evicted");
      this.sockets.clear();
      this.json(response, 200, { evicted: true });
      return;
    }
    if (operation === "runtime.restart") {
      this.json(response, 200, { restarted: true });
      return;
    }
    if (operation === "runtime.crash") {
      const pending = this.pendingCrash;
      this.pendingCrash = undefined;
      if (!pending) {
        this.json(response, 409, { error: "no crash barrier" });
        return;
      }
      if (pending.phase === "before-commit" && this.mutant === "RF-CRASH-001") {
        this.write(pending.path, "must roll back", "agent_write", "mutant-crash");
      }
      pending.response.destroy();
      this.json(response, 200, { terminated: true });
      return;
    }
    if (operation === "runtime.failover") {
      const phase = String(body.phase ?? "");
      if (phase === "begin") {
        this.failover = { id: "fake-switch", writes: [] };
        this.json(response, 200, { switchId: "fake-switch", state: "fenced" });
        return;
      }
      if (phase === "await-writers") {
        this.json(response, 200, { pendingWriters: this.failover?.writes.length ?? 0 });
        return;
      }
      if (phase === "release") {
        const pending = this.failover?.writes ?? [];
        this.failover = undefined;
        for (const [index, write] of pending.entries()) {
          if (index === 0 || this.mutant === "RF-FAILOVER-001") {
            const receipt = this.write(write.path, write.content, "agent_write", write.correlationId);
            this.json(write.response, 202, receipt);
          } else {
            const current = this.files.get(write.path)!;
            this.json(write.response, 409, {
              code: "conflict",
              message: "writer fenced",
              correlationId: write.correlationId,
              expectedRevision: write.ifMatch,
              currentRevision: current.revision,
            });
          }
        }
        this.json(response, 200, { released: true });
        return;
      }
    }
    if (operation === "state.export") {
      this.exportedArtifact = "fake-artifact";
      const pending = this.pendingMigrationId ? [this.pendingMigrationId] : [];
      const deadLetters = [...this.deadLetters.values()].map((item) => item.envelopeId);
      this.json(response, 200, { artifact: this.exportedArtifact, manifest: { pendingOutboxIds: pending, deadLetterIds: deadLetters } });
      return;
    }
    if (operation === "state.import") {
      this.servingRuntime = String(body.destinationRuntime ?? "cloudflare-do");
      const pending = this.pendingMigrationId ? [this.pendingMigrationId] : [];
      const deadLetters = [...this.deadLetters.values()].map((item) => item.envelopeId);
      this.json(response, 200, { servingRuntime: this.servingRuntime, manifest: { pendingOutboxIds: pending, deadLetterIds: deadLetters } });
      return;
    }
    if (operation === "mount.start") {
      const mode = String(body.mode) as MountState["mode"];
      const mount = await this.startMount(mode);
      this.json(response, 200, { id: mount.id, root: mount.root, maxReconnectDelayMs: 50 });
      return;
    }
    if (operation === "mount.stop") {
      const mount = this.mounts.get(String(body.id));
      if (mount) {
        clearInterval(mount.timer);
        this.mounts.delete(mount.id);
      }
      this.json(response, 200, { stopped: true });
      return;
    }
    this.json(response, 400, this.error(request, "unsupported_control", operation));
  }

  private async advanceClock(milliseconds: number): Promise<void> {
    if (this.config.backpressureUntil && this.virtualNow >= this.config.backpressureUntil && this.mutant !== "RF-QUEUE-003") {
      this.config.backpressureUntil = undefined;
    }
    const retry = this.config.matchPath ? this.deadLetters.get(`retry:${this.config.matchPath}`) : undefined;
    if (retry?.body && this.mutant !== "RF-QUEUE-001") {
      this.calls.set(retry.body.path, { attempts: 3, state: "succeeded" });
      this.applyWebhook(retry.body, "fake-retry");
      this.deadLetters.delete(`retry:${retry.body.path}`);
    }
    const permanent = this.config.permanentIngestFailurePath;
    if (permanent && milliseconds >= 300_000) {
      const item = this.deadLetters.get(permanent);
      if (item) this.deadLetters.set(item.deliveryId, item);
    }
    for (const [key, item] of [...this.deadLetters.entries()]) {
      if (!key.startsWith("replay:")) continue;
      if (item.body) this.applyWebhook(item.body, "fake-replay");
      this.deadLetters.delete(key);
      for (const [candidateKey, candidate] of this.deadLetters) {
        if (candidate.envelopeId === item.envelopeId) this.deadLetters.delete(candidateKey);
      }
    }
    const providerMutation = [...this.files.keys()].some((path) => path.includes("/projection/") || path.includes("/timers/rollover-source"));
    if (milliseconds < 86_400_000 && providerMutation && !this.files.has("/digests/today.md")) {
      this.write("/digests/today.md", `generated_at: ${new Date(this.virtualNow).toISOString()}`, "system", "fake-digest");
    }
    if (milliseconds < 86_400_000 && providerMutation && this.files.has("/digests/today.md") && this.mutant === "RF-PROJ-001") {
      this.write("/digests/today.md", `generated_at: ${new Date(this.virtualNow).toISOString()}`, "system", "mutant-recursive-digest");
    }
    if (this.mutant === "RF-PROJ-002") {
      const terminal = [...this.files.entries()].find(([path, file]) => path.includes("/projection/terminal.md") && file.content === "state: closed");
      if (terminal) {
        this.files.delete(terminal[0]);
        this.emit(terminal[0], "file.deleted", this.nextRevision(), "system", "mutant-terminal-cleanup");
      }
    }
    if (milliseconds >= 86_400_000 && this.mutant !== "RF-TIMER-001") {
      const today = this.files.get("/digests/today.md");
      if (today) this.write("/digests/yesterday.md", today.content, "system", "fake-rollover");
    }
    if (this.pendingMigrationId) {
      const operation = this.operations.get(this.pendingMigrationId);
      if (operation) {
        operation.state = "succeeded";
        operation.writebackAttempts = 1;
      }
    }
  }

  private applyWebhook(body: WebhookBody, correlationId: string): void {
    const content = String(body.data?.content ?? "");
    if (body.event_type === "file.deleted") {
      const current = this.files.get(body.path);
      if (current) {
        this.files.delete(body.path);
        this.emit(body.path, "file.deleted", this.nextRevision(), "provider_sync", correlationId);
      }
      return;
    }
    this.write(body.path, content, "provider_sync", correlationId);
  }

  private write(
    path: string,
    content: string,
    origin: FakeEvent["origin"],
    correlationId: string,
    options: { suppressBroadcast?: boolean } = {},
  ): { opId: string; status: "queued"; targetRevision: string } {
    const existed = this.files.has(path);
    const revision = this.nextRevision();
    this.files.set(path, { content, revision, contentType: "text/plain" });
    const opId = `op_${++this.operationSequence}`;
    this.operations.set(opId, { opId, state: "succeeded", writebackAttempts: 1, path });
    this.emit(path, existed ? "file.updated" : "file.created", revision, origin, correlationId, options.suppressBroadcast);
    void this.projectMounts(path, content);
    return { opId, status: "queued", targetRevision: revision };
  }

  private emit(
    path: string,
    type: FakeEvent["type"],
    revision: string,
    origin: FakeEvent["origin"],
    correlationId: string,
    suppressBroadcast = false,
  ): void {
    const event: FakeEvent = {
      eventId: `evt_${String(++this.eventSequence).padStart(6, "0")}`,
      type,
      path,
      revision,
      origin,
      correlationId,
      timestamp: new Date(this.virtualNow).toISOString(),
    };
    this.events.push(event);
    if (!suppressBroadcast) {
      for (const socket of this.sockets) if (socket.readyState === socket.OPEN) socket.send(JSON.stringify(event));
    }
  }

  private nextRevision(): string {
    return `rev_${++this.revision}`;
  }

  private async startMount(mode: MountState["mode"]): Promise<MountState> {
    const id = `mount-${this.mounts.size + 1}`;
    const root = await mkdtemp(join(this.root, `${id}-`));
    const known = new Map<string, string>();
    for (const [path, file] of this.files) {
      await this.writeMountFile(root, path, file.content, mode === "read");
      known.set(path, file.content);
    }
    const mount = { id, mode, root, known, timer: setInterval(() => void this.scanMount(id), 15) } satisfies MountState;
    mount.timer.unref();
    this.mounts.set(id, mount);
    return mount;
  }

  private async scanMount(id: string): Promise<void> {
    const mount = this.mounts.get(id);
    if (!mount || mount.mode === "read") return;
    const paths = await this.walk(mount.root);
    for (const localPath of paths) {
      const relative = localPath.slice(mount.root.length).replaceAll("\\", "/");
      if (!relative.startsWith("/") || relative.startsWith("/.relay/")) continue;
      const content = await readFile(localPath, "utf8").catch(() => undefined);
      if (content === undefined || mount.known.get(relative) === content) continue;
      mount.known.set(relative, content);
      this.write(relative, content, "agent_write", "fake-mount");
    }
  }

  private async projectMounts(path: string, content: string): Promise<void> {
    for (const mount of this.mounts.values()) {
      const localPath = join(mount.root, path.replace(/^\/+/, ""));
      const existing = await readFile(localPath, "utf8").catch(() => undefined);
      const known = mount.known.get(path);
      if (mount.mode === "mirror" && existing !== undefined && known !== undefined && existing !== known) {
        if (this.mutant !== "RF-MOUNT-001") {
          const conflict = join(mount.root, ".relay", "conflicts", `${path.replace(/^\/+/, "")}.local`);
          await mkdir(dirname(conflict), { recursive: true });
          await writeFile(conflict, existing);
        }
      }
      await this.writeMountFile(mount.root, path, content, mount.mode === "read");
      mount.known.set(path, content);
    }
  }

  private async writeMountFile(root: string, path: string, content: string, readonly: boolean): Promise<void> {
    const localPath = join(root, path.replace(/^\/+/, ""));
    await mkdir(dirname(localPath), { recursive: true });
    await writeFile(localPath, content);
    if (readonly) await chmod(localPath, 0o444);
  }

  private async walk(root: string): Promise<string[]> {
    const result: string[] = [];
    for (const entry of await readdir(root, { withFileTypes: true }).catch(() => [])) {
      const path = join(root, entry.name);
      if (entry.isDirectory()) result.push(...await this.walk(path));
      else if (entry.isFile()) result.push(path);
    }
    return result;
  }

  private upgrade(request: IncomingMessage, socket: import("node:stream").Duplex, head: Buffer): void {
    const url = new URL(request.url ?? "/", this.baseUrl);
    const match = /^\/v1\/workspaces\/([^/]+)\/fs\/ws$/u.exec(url.pathname);
    if (!match || decodeURIComponent(match[1]!) !== PRIMARY_WORKSPACE || url.searchParams.get("token") !== PRIMARY_TOKEN) {
      socket.write("HTTP/1.1 403 Forbidden\r\n\r\n");
      socket.destroy();
      return;
    }
    this.wsServer!.handleUpgrade(request, socket, head, (webSocket) => {
      this.sockets.add(webSocket);
      webSocket.once("close", () => this.sockets.delete(webSocket));
      const cursor = url.searchParams.get("cursor");
      const cursorIndex = cursor ? this.events.findIndex((event) => event.eventId === cursor) : -1;
      let backlog = cursor === "now" ? [] : this.events.slice(Math.max(0, cursorIndex + 1));
      if (this.mutant === "RF-WS-002" && backlog.some((event) => event.path.includes("after-eviction"))) backlog = backlog.slice(1);
      for (const event of backlog) webSocket.send(JSON.stringify(event));
    });
  }

  private reset(): void {
    this.files.clear();
    this.events.length = 0;
    this.operations.clear();
    this.calls.clear();
    this.identities.clear();
    this.deadLetters.clear();
    this.traceRecords.length = 0;
    this.config = {};
    this.revision = 0;
    this.eventSequence = 0;
    this.operationSequence = 0;
    this.servingRuntime = "terse-durable-actors";
  }

  private async body(request: IncomingMessage): Promise<Record<string, unknown>> {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    if (chunks.length === 0) return {};
    return JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, unknown>;
  }

  private error(request: IncomingMessage, code: string, message: string): Record<string, unknown> {
    return { code, message, correlationId: String(request.headers["x-correlation-id"] ?? "fake-correlation") };
  }

  private json(
    response: ServerResponse,
    status: number,
    body: unknown,
    headers: Record<string, string> = {},
  ): void {
    if (response.destroyed || response.writableEnded) return;
    response.writeHead(status, { "Content-Type": "application/json", ...headers });
    response.end(JSON.stringify(body));
  }
}
