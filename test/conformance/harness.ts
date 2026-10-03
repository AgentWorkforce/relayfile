import type {
  Capability,
  CaseResult,
  ControlOperation,
  EvidenceSummary,
  Exchange,
  Profile,
  ResolvedTarget,
  TokenName,
} from "./types.js";
import { validateOpenApiResponse } from "./openapi-validator.js";

const TRACE_SKEW_MARGIN_MS = 5_000;

export interface ApiResponse<T = unknown> {
  status: number;
  data: T;
  headers: Headers;
  correlationId: string;
}

export interface RequestOptions {
  token?: TokenName | false;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

export interface HarnessOptions {
  includeCaseIds?: ReadonlySet<string>;
  maxPollTimeoutMs?: number;
  registerOnly?: boolean;
}

interface RuntimeResponse<T = unknown> {
  status: number;
  data: T;
}

interface TracePage {
  epoch?: string;
  cursor?: number;
  dropped?: number;
  evicted?: number;
  persistenceFailed?: boolean;
  records?: Array<{ sequence?: number; requestId?: string }>;
  nextCursor?: string | null;
  resumeCursor?: string;
  reset?: boolean;
}

interface TraceHighWater {
  epoch: string;
  cursor: number;
  sequence: number;
}

export class RequestTransportError extends Error {
  constructor(
    readonly correlationId: string,
    readonly phase: "request" | "response-body",
  ) {
    super(`request transport failure during ${phase} (correlation ${correlationId})`);
    this.name = "RequestTransportError";
  }
}

export class Harness {
  readonly cases: CaseResult[] = [];
  readonly exchanges: Exchange[] = [];
  readonly startedAt = new Date().toISOString();
  private correlationCounter = 0;
  private activeCorrelations?: string[];
  private activePollSignal?: AbortSignal;
  private activeEvidenceBasis?: CaseResult["evidenceBasis"];

  constructor(
    readonly target: ResolvedTarget,
    readonly profile: Profile,
    readonly seed: string,
    private readonly localRestart?: () => Promise<void>,
    private readonly options: HarnessOptions = {},
  ) {}

  path(suffix: string): string {
    return `/conformance/${this.seed}/${suffix.replace(/^\/+/, "")}`;
  }

  workspacePath(workspace: string, suffix: string): string {
    return `/v1/workspaces/${encodeURIComponent(workspace)}${suffix}`;
  }

  async case(
    id: string,
    name: string,
    requiredCapabilities: Capability[],
    run: () => Promise<void>,
  ): Promise<void> {
    if (this.options.includeCaseIds && !this.options.includeCaseIds.has(id)) return;
    if (this.options.registerOnly) {
      this.cases.push({
        id,
        name,
        status: "skipped",
        durationMs: 0,
        requiredCapabilities,
        correlationIds: [],
        skipReason: "registration audit",
        skipKind: "not-applicable",
      });
      return;
    }
    const missing = requiredCapabilities.filter((capability) => !this.target.capabilities.has(capability));
    if (missing.length > 0) {
      const reason = `target ${this.target.id} lacks ${missing.join(", ")}`;
      this.cases.push({
        id,
        name,
        status: this.profile === "full" ? "failed" : "skipped",
        durationMs: 0,
        requiredCapabilities,
        correlationIds: [],
        ...(this.profile === "full"
          ? { error: reason }
          : { skipReason: reason, skipKind: "missing-capability" as const }),
      });
      return;
    }

    const started = performance.now();
    this.activeCorrelations = [];
    this.activeEvidenceBasis = undefined;
    try {
      await run();
      this.cases.push({
        id,
        name,
        status: "passed",
        durationMs: Math.round(performance.now() - started),
        requiredCapabilities,
        correlationIds: this.activeCorrelations,
        ...(this.activeEvidenceBasis ? { evidenceBasis: this.activeEvidenceBasis } : {}),
      });
    } catch (error) {
      this.cases.push({
        id,
        name,
        status: "failed",
        durationMs: Math.round(performance.now() - started),
        requiredCapabilities,
        correlationIds: this.activeCorrelations,
        error: error instanceof Error ? error.message : String(error),
        ...(this.activeEvidenceBasis ? { evidenceBasis: this.activeEvidenceBasis } : {}),
      });
    } finally {
      this.activeCorrelations = undefined;
      this.activeEvidenceBasis = undefined;
    }
  }

  notApplicable(id: string, name: string, reason: string): void {
    if (this.options.includeCaseIds && !this.options.includeCaseIds.has(id)) return;
    this.cases.push({
      id,
      name,
      status: "skipped",
      durationMs: 0,
      requiredCapabilities: [],
      correlationIds: [],
      skipReason: reason,
      skipKind: "not-applicable",
    });
  }

  markEvidenceBasis(basis: NonNullable<CaseResult["evidenceBasis"]>): void {
    if (basis === "adapter-attested" || !this.activeEvidenceBasis) this.activeEvidenceBasis = basis;
  }

  async request<T = unknown>(method: string, path: string, options: RequestOptions = {}): Promise<ApiResponse<T>> {
    const correlationId = `${this.seed}-${String(++this.correlationCounter).padStart(4, "0")}`;
    this.activeCorrelations?.push(correlationId);
    const headers: Record<string, string> = {
      "X-Correlation-Id": correlationId,
      ...options.headers,
    };
    const tokenName = options.token === undefined ? "primary" : options.token;
    if (tokenName !== false) {
      const token = this.target.tokens[tokenName];
      if (!token) throw new Error(`target ${this.target.id} has no ${tokenName} token configured`);
      headers.Authorization = `Bearer ${token}`;
    }
    if (options.body !== undefined) headers["Content-Type"] ??= "application/json";

    const startedAt = new Date().toISOString();
    const started = performance.now();
    const signal = options.signal ?? this.activePollSignal;
    let response: Response;
    try {
      response = await fetch(`${this.target.baseUrl}${path}`, {
        method,
        headers,
        ...(options.body !== undefined ? { body: JSON.stringify(options.body) } : {}),
        ...(signal ? { signal } : {}),
      });
    } catch (error) {
      this.exchanges.push({
        correlationId,
        method,
        url: `${this.target.baseUrl}${path}`,
        startedAt,
        durationMs: Math.round(performance.now() - started),
        request: { headers, ...(options.body !== undefined ? { body: options.body } : {}) },
        response: { status: 0, headers: {}, body: { transportError: error instanceof Error ? error.name : "Error" } },
      });
      throw new RequestTransportError(correlationId, "request");
    }
    const responseHeaders = Object.fromEntries(response.headers.entries());
    let raw: string;
    try {
      raw = await response.text();
    } catch (error) {
      this.exchanges.push({
        correlationId,
        method,
        url: `${this.target.baseUrl}${path}`,
        startedAt,
        durationMs: Math.round(performance.now() - started),
        request: { headers, ...(options.body !== undefined ? { body: options.body } : {}) },
        response: {
          status: response.status,
          headers: responseHeaders,
          body: { transportError: error instanceof Error ? error.name : "Error", phase: "response-body" },
        },
      });
      throw new RequestTransportError(correlationId, "response-body");
    }
    let data: unknown = raw;
    if (raw) {
      try { data = JSON.parse(raw); } catch { /* retain text */ }
    }
    this.exchanges.push({
      correlationId,
      method,
      url: `${this.target.baseUrl}${path}`,
      startedAt,
      durationMs: Math.round(performance.now() - started),
      request: { headers, ...(options.body !== undefined ? { body: options.body } : {}) },
      response: { status: response.status, headers: responseHeaders, ...(raw ? { body: data } : {}) },
    });
    await validateOpenApiResponse(method, path, response.status, data, response.headers.get("content-type"));
    return { status: response.status, data: data as T, headers: response.headers, correlationId };
  }

  async control<T = unknown>(operation: ControlOperation, payload: Record<string, unknown> = {}): Promise<T> {
    this.markEvidenceBasis("adapter-attested");
    const control = this.target.control;
    if (!control || !control.operations.has(operation)) {
      throw new Error(`target ${this.target.id} does not provide control operation ${operation}`);
    }
    const correlationId = `${this.seed}-control-${String(++this.correlationCounter).padStart(4, "0")}`;
    this.activeCorrelations?.push(correlationId);
    const url = `${control.baseUrl}/v1/conformance/${encodeURIComponent(operation)}`;
    const headers = {
      "Content-Type": "application/json",
      "X-Correlation-Id": correlationId,
      ...(control.token ? { Authorization: `Bearer ${control.token}` } : {}),
    };
    const body = { workspaceId: this.target.workspaces.primary, seed: this.seed, ...payload };
    const startedAt = new Date().toISOString();
    const started = performance.now();
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        ...(this.activePollSignal ? { signal: this.activePollSignal } : {}),
      });
    } catch (error) {
      this.exchanges.push({
        correlationId,
        method: "CONTROL",
        url,
        startedAt,
        durationMs: Math.round(performance.now() - started),
        request: { headers, body },
        response: { status: 0, headers: {}, body: { transportError: error instanceof Error ? error.name : "Error" } },
      });
      throw new Error(`control ${operation} transport failure (correlation ${correlationId})`);
    }
    const responseHeaders = Object.fromEntries(response.headers.entries());
    let raw: string;
    try {
      raw = await response.text();
    } catch (error) {
      this.exchanges.push({
        correlationId,
        method: "CONTROL",
        url,
        startedAt,
        durationMs: Math.round(performance.now() - started),
        request: { headers, body },
        response: {
          status: response.status,
          headers: responseHeaders,
          body: { transportError: error instanceof Error ? error.name : "Error", phase: "response-body" },
        },
      });
      throw new Error(`control ${operation} response body transport failure (correlation ${correlationId})`);
    }
    let data: unknown = {};
    let jsonError = false;
    if (raw) {
      try { data = JSON.parse(raw); } catch { data = raw; jsonError = true; }
    }
    this.exchanges.push({
      correlationId,
      method: "CONTROL",
      url,
      startedAt,
      durationMs: Math.round(performance.now() - started),
      request: { headers, body },
      response: {
        status: response.status,
        headers: responseHeaders,
        ...(raw ? { body: data } : {}),
      },
    });
    if (!response.ok) throw new Error(`control ${operation} failed with status ${response.status}; inspect redacted requests.jsonl`);
    if (jsonError) throw new Error(`control ${operation} returned non-JSON success; inspect redacted requests.jsonl`);
    return data as T;
  }

  async verifyStateInspection<T>(read: () => Promise<T>): Promise<T> {
    if (this.target.runtime.kind === "cloudflare-do") {
      this.markEvidenceBasis("adapter-attested");
      return read();
    }
    if (this.target.runtime.kind !== "terse-durable-actors") return read();

    this.markEvidenceBasis("runtime-native");
    const traceMark = await this.captureRuntimeTraceHighWater();
    const value = await read();
    const observed = await this.runtimeObserveState();
    assert(
      Object.hasOwn(observed, "snapshot") && Object.hasOwn(observed, "schema"),
      "Terse observe/state omitted snapshot or schema",
    );
    await this.assertRuntimeTraceUnchanged(traceMark);
    return value;
  }

  async verifyRuntimeAuthRejects(): Promise<void> {
    const verification = this.requireRuntimeVerification();
    this.markEvidenceBasis("runtime-native");
    const path = `/v1/projects/${encodeURIComponent(verification.projectId)}/actors/${encodeURIComponent(
      verification.actorName,
    )}/${encodeURIComponent(verification.actorId)}/invoke`;
    const probeRequestId = `${this.seed}-runtime-auth-probe`;
    const body = {
      requestId: probeRequestId,
      method: "__relayfile_conformance_auth_probe__",
      args: [],
    };
    const fromMs = Date.now();
    const omitted = await this.runtimeRequest("POST", path, {}, body);
    const invalid = await this.runtimeRequest("POST", path, { Authorization: "Bearer invalid-conformance-key" }, body);
    const toMs = Date.now();
    assert([401, 403].includes(omitted.status), `Terse runtime accepted omitted auth with status ${omitted.status}`);
    assert([401, 403].includes(invalid.status), `Terse runtime accepted invalid auth with status ${invalid.status}`);
    await this.assertNoRuntimeActorRequestsForId(fromMs, toMs, probeRequestId);
  }

  private async runtimeObserveState(): Promise<Record<string, unknown>> {
    const verification = this.requireRuntimeVerification();
    const query = new URLSearchParams({ actorName: verification.actorName, actorId: verification.actorId });
    const response = await this.runtimeRequest(
      "GET",
      `/v1/projects/${encodeURIComponent(verification.projectId)}/observe/state?${query.toString()}`,
      { Authorization: `Bearer ${verification.adminKey}` },
    );
    assert(response.status === 200, `Terse observe/state returned ${response.status}`);
    return asRecord(response.data);
  }

  private async captureRuntimeTraceHighWater(): Promise<TraceHighWater> {
    const trace = await this.runtimeTracePage(new URLSearchParams({ limit: "500" }));
    this.assertHealthyTracePage(trace, "before state inspection");
    assert(typeof trace.epoch === "string" && trace.epoch.length > 0, "Terse trace response omitted epoch");
    assert(Number.isSafeInteger(trace.cursor) && trace.cursor! >= 0, "Terse trace response omitted cursor");
    return {
      epoch: trace.epoch,
      cursor: trace.cursor!,
      sequence: this.highestTraceSequence(trace),
    };
  }

  private async assertRuntimeTraceUnchanged(mark: TraceHighWater): Promise<void> {
    const trace = await this.runtimeTracePage(new URLSearchParams({ limit: "500" }));
    this.assertHealthyTracePage(trace, "after state inspection");
    assert(trace.epoch === mark.epoch, "Terse trace epoch changed while verifying no-wake behavior");
    assert(trace.cursor === mark.cursor, `Terse trace cursor advanced from ${mark.cursor} to ${String(trace.cursor)}`);
    const newRecords = trace.records!.filter((record) => {
      assert(Number.isSafeInteger(record.sequence) && record.sequence! >= 0, "Terse trace record omitted sequence");
      return record.sequence! > mark.sequence;
    });
    assert(newRecords.length === 0, `state inspection reached actor application code (${newRecords.length} records)`);
  }

  private highestTraceSequence(trace: TracePage): number {
    let highest = -1;
    for (const record of trace.records!) {
      assert(Number.isSafeInteger(record.sequence) && record.sequence! >= 0, "Terse trace record omitted sequence");
      highest = Math.max(highest, record.sequence!);
    }
    return highest;
  }

  private assertHealthyTracePage(trace: TracePage, context: string): void {
    assert(trace.dropped === 0, `Terse trace ${context} dropped ${String(trace.dropped)} records`);
    assert(trace.evicted === 0, `Terse trace ${context} evicted ${String(trace.evicted)} records`);
    assert(trace.persistenceFailed === false, `Terse trace persistence failed ${context}`);
    assert(trace.reset === false, `Terse trace reset ${context}`);
    assert(Array.isArray(trace.records), "Terse trace response omitted records");
  }

  private async runtimeTracePage(extra: URLSearchParams): Promise<TracePage> {
    const verification = this.requireRuntimeVerification();
    const query = new URLSearchParams({
      actorName: verification.actorName,
      actorId: verification.actorId,
    });
    for (const [key, value] of extra) query.set(key, value);
    const response = await this.runtimeRequest<TracePage>(
      "GET",
      `/v1/projects/${encodeURIComponent(verification.projectId)}/observe/requests?${query.toString()}`,
      { Authorization: `Bearer ${verification.adminKey}` },
    );
    assert(response.status === 200, `Terse observe/requests returned ${response.status}`);
    return asRecord(response.data) as TracePage;
  }

  private async assertNoRuntimeActorRequestsForId(fromMs: number, toMs: number, requestId: string): Promise<void> {
    let cursor: string | undefined;
    for (let pageNumber = 0; pageNumber < 100; pageNumber++) {
      const query = new URLSearchParams({
        requestId,
        fromMs: String(Math.max(0, fromMs - TRACE_SKEW_MARGIN_MS)),
        toMs: String(toMs + TRACE_SKEW_MARGIN_MS),
        limit: "500",
      });
      if (cursor) query.set("cursor", cursor);
      const trace = await this.runtimeTracePage(query);
      this.assertHealthyTracePage(trace, "while verifying runtime auth rejection");
      assert(trace.records.length === 0, `runtime-native probe reached actor application code (${trace.records.length} records)`);
      if (!trace.nextCursor) return;
      cursor = trace.nextCursor;
    }
    throw new Error("Terse trace pagination exceeded 100 pages");
  }

  private requireRuntimeVerification(): NonNullable<ResolvedTarget["runtimeVerification"]> {
    const verification = this.target.runtimeVerification;
    assert(verification, `target ${this.target.id} lacks runtime-native verification configuration`);
    return verification;
  }

  private async runtimeRequest<T = unknown>(
    method: string,
    path: string,
    extraHeaders: Record<string, string>,
    body?: unknown,
  ): Promise<RuntimeResponse<T>> {
    const verification = this.requireRuntimeVerification();
    const correlationId = `${this.seed}-runtime-${String(++this.correlationCounter).padStart(4, "0")}`;
    this.activeCorrelations?.push(correlationId);
    const headers: Record<string, string> = {
      "X-Correlation-Id": correlationId,
      ...extraHeaders,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    };
    const url = `${verification.baseUrl}${path}`;
    const startedAt = new Date().toISOString();
    const started = performance.now();
    let response: Response;
    try {
      response = await fetch(url, {
        method,
        headers,
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        ...(this.activePollSignal ? { signal: this.activePollSignal } : {}),
      });
    } catch (error) {
      this.exchanges.push({
        correlationId,
        method: "RUNTIME",
        url,
        startedAt,
        durationMs: Math.round(performance.now() - started),
        request: { headers, ...(body === undefined ? {} : { body }) },
        response: { status: 0, headers: {}, body: { transportError: error instanceof Error ? error.name : "Error" } },
      });
      throw new Error(`runtime-native request transport failure (correlation ${correlationId})`);
    }
    const raw = await response.text();
    let data: unknown = raw;
    if (raw) {
      try { data = JSON.parse(raw); } catch { /* retain text */ }
    }
    const responseHeaders = Object.fromEntries(response.headers.entries());
    this.exchanges.push({
      correlationId,
      method: "RUNTIME",
      url,
      startedAt,
      durationMs: Math.round(performance.now() - started),
      request: { headers, ...(body === undefined ? {} : { body }) },
      response: { status: response.status, headers: responseHeaders, ...(raw ? { body: data } : {}) },
    });
    return { status: response.status, data: data as T };
  }

  async restart(): Promise<void> {
    if (this.localRestart) {
      await this.localRestart();
      return;
    }
    await this.control("runtime.restart");
  }

  async poll<T>(label: string, read: () => Promise<T>, accept: (value: T) => boolean, timeoutMs = 5_000): Promise<T> {
    const effectiveTimeoutMs = Math.min(timeoutMs, this.options.maxPollTimeoutMs ?? timeoutMs);
    const deadline = Date.now() + effectiveTimeoutMs;
    let delay = 20;
    let latest: T | undefined;
    while (Date.now() < deadline) {
      const remaining = deadline - Date.now();
      const controller = new AbortController();
      const previousSignal = this.activePollSignal;
      this.activePollSignal = controller.signal;
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        latest = await Promise.race([
          read(),
          new Promise<never>((_, reject) => {
            timer = setTimeout(() => {
              controller.abort();
              reject(new Error(`${label} did not converge within ${effectiveTimeoutMs}ms; latest=${safeDiagnostic(latest)}`));
            }, remaining);
          }),
        ]);
      } finally {
        if (timer) clearTimeout(timer);
        this.activePollSignal = previousSignal;
      }
      if (Date.now() <= deadline && accept(latest)) return latest;
      const sleepMs = Math.min(delay, Math.max(0, deadline - Date.now()));
      if (sleepMs > 0) await new Promise((resolve) => setTimeout(resolve, sleepMs));
      delay = Math.min(delay * 2, 250);
    }
    throw new Error(`${label} did not converge within ${effectiveTimeoutMs}ms; latest=${safeDiagnostic(latest)}`);
  }

  summary(): EvidenceSummary {
    const counts = { passed: 0, failed: 0, skipped: 0 };
    for (const result of this.cases) counts[result.status]++;
    return {
      schemaVersion: 1,
      seed: this.seed,
      profile: this.profile,
      target: {
        id: this.target.id,
        baseUrl: this.target.baseUrl,
        runtime: this.target.runtime,
        relayfileSha: this.target.relayfileSha,
        capabilities: [...this.target.capabilities].sort(),
      },
      startedAt: this.startedAt,
      finishedAt: new Date().toISOString(),
      counts,
      cases: this.cases,
    };
  }
}

export function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

export function assertStatus(response: ApiResponse, expected: number | number[]): void {
  const statuses = Array.isArray(expected) ? expected : [expected];
  assert(
    statuses.includes(response.status),
    `expected status ${statuses.join("/")}, got ${response.status} (correlation ${response.correlationId})`,
  );
}

export function asRecord(value: unknown): Record<string, unknown> {
  assert(value !== null && typeof value === "object" && !Array.isArray(value), `expected object, got ${typeof value}`);
  return value as Record<string, unknown>;
}

function safeDiagnostic(value: unknown): string {
  if (Array.isArray(value)) return `array(length=${value.length})`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    if (typeof record.status === "number") {
      return `response(status=${record.status},correlation=${String(record.correlationId ?? "unknown")})`;
    }
    return `object(keys=${Object.keys(record).sort().join(",")})`;
  }
  return typeof value;
}
