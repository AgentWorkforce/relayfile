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

  constructor(
    readonly target: ResolvedTarget,
    readonly profile: Profile,
    readonly seed: string,
    private readonly localRestart?: () => Promise<void>,
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
        ...(this.profile === "full" ? { error: reason } : { skipReason: reason }),
      });
      return;
    }

    const started = performance.now();
    this.activeCorrelations = [];
    try {
      await run();
      this.cases.push({
        id,
        name,
        status: "passed",
        durationMs: Math.round(performance.now() - started),
        requiredCapabilities,
        correlationIds: this.activeCorrelations,
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
      });
    } finally {
      this.activeCorrelations = undefined;
    }
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

  async restart(): Promise<void> {
    if (this.localRestart) {
      await this.localRestart();
      return;
    }
    await this.control("runtime.restart");
  }

  async poll<T>(label: string, read: () => Promise<T>, accept: (value: T) => boolean, timeoutMs = 5_000): Promise<T> {
    const deadline = Date.now() + timeoutMs;
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
              reject(new Error(`${label} did not converge within ${timeoutMs}ms; latest=${safeDiagnostic(latest)}`));
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
    throw new Error(`${label} did not converge within ${timeoutMs}ms; latest=${safeDiagnostic(latest)}`);
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
