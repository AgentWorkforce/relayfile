import {
  type AckResponse,
  type DeleteFileInput,
  type DeadLetterItem,
  type DeadLetterFeedResponse,
  type ErrorResponse,
  type EventFeedResponse,
  type FileReadResponse,
  type GetEventsOptions,
  type GetOperationsOptions,
  type GetSyncDeadLettersOptions,
  type GetSyncIngressStatusOptions,
  type GetSyncStatusOptions,
  type ListTreeOptions,
  type OperationFeedResponse,
  type OperationStatusResponse,
  type QueuedResponse,
  type SyncIngressStatusResponse,
  type SyncStatusResponse,
  type TreeResponse,
  type WriteFileInput,
  type WriteQueuedResponse
} from "./types.js";
import { InvalidStateError, QueueFullError, RelayFileApiError, RevisionConflictError } from "./errors.js";

export type AccessTokenProvider = string | (() => string | Promise<string>);

export interface RelayFileRetryOptions {
  maxRetries?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  jitterRatio?: number;
}

export interface RelayFileClientOptions {
  baseUrl: string;
  token: AccessTokenProvider;
  fetchImpl?: typeof fetch;
  userAgent?: string;
  retry?: RelayFileRetryOptions;
}

interface NormalizedRetryOptions {
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
  jitterRatio: number;
}

const DEFAULT_RETRY_OPTIONS: NormalizedRetryOptions = {
  maxRetries: 3,
  baseDelayMs: 100,
  maxDelayMs: 2000,
  jitterRatio: 0.2
};

function normalizeRetryOptions(options?: RelayFileRetryOptions): NormalizedRetryOptions {
  const maxRetries = options?.maxRetries ?? DEFAULT_RETRY_OPTIONS.maxRetries;
  const baseDelayMs = options?.baseDelayMs ?? DEFAULT_RETRY_OPTIONS.baseDelayMs;
  const maxDelayMs = options?.maxDelayMs ?? DEFAULT_RETRY_OPTIONS.maxDelayMs;
  const jitterRatio = options?.jitterRatio ?? DEFAULT_RETRY_OPTIONS.jitterRatio;
  return {
    maxRetries: Math.max(0, Math.floor(maxRetries)),
    baseDelayMs: Math.max(1, Math.floor(baseDelayMs)),
    maxDelayMs: Math.max(1, Math.floor(maxDelayMs)),
    jitterRatio: Math.max(0, Math.min(1, jitterRatio))
  };
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) {
      query.set(key, String(value));
    }
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function generateCorrelationId(): string {
  return `rf_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`;
}

async function resolveToken(tokenProvider: AccessTokenProvider): Promise<string> {
  if (typeof tokenProvider === "function") {
    return tokenProvider();
  }
  return tokenProvider;
}

function createAbortError(): Error {
  const err = new Error("The operation was aborted.");
  err.name = "AbortError";
  return err;
}

export class RelayFileClient {
  private readonly baseUrl: string;
  private readonly tokenProvider: AccessTokenProvider;
  private readonly fetchImpl: typeof fetch;
  private readonly userAgent?: string;
  private readonly retryOptions: NormalizedRetryOptions;

  constructor(options: RelayFileClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.tokenProvider = options.token;
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.userAgent = options.userAgent;
    this.retryOptions = normalizeRetryOptions(options.retry);
  }

  async listTree(workspaceId: string, options: ListTreeOptions = {}): Promise<TreeResponse> {
    const query = buildQuery({
      path: options.path ?? "/",
      depth: options.depth,
      cursor: options.cursor
    });
    return this.request<TreeResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/tree${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async readFile(workspaceId: string, path: string, correlationId?: string, signal?: AbortSignal): Promise<FileReadResponse> {
    const query = buildQuery({ path });
    return this.request<FileReadResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`,
      correlationId,
      signal
    });
  }

  async writeFile(input: WriteFileInput): Promise<WriteQueuedResponse> {
    const query = buildQuery({ path: input.path });
    return this.request<WriteQueuedResponse>({
      method: "PUT",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/file${query}`,
      correlationId: input.correlationId,
      headers: {
        "If-Match": input.baseRevision
      },
      body: {
        contentType: input.contentType ?? "text/markdown",
        content: input.content
      },
      signal: input.signal
    });
  }

  async deleteFile(input: DeleteFileInput): Promise<WriteQueuedResponse> {
    const query = buildQuery({ path: input.path });
    return this.request<WriteQueuedResponse>({
      method: "DELETE",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/file${query}`,
      correlationId: input.correlationId,
      headers: {
        "If-Match": input.baseRevision
      },
      signal: input.signal
    });
  }

  async getEvents(workspaceId: string, options: GetEventsOptions = {}): Promise<EventFeedResponse> {
    const query = buildQuery({
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<EventFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/events${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getOp(
    workspaceId: string,
    opId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<OperationStatusResponse> {
    return this.request<OperationStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops/${encodeURIComponent(opId)}`,
      correlationId,
      signal
    });
  }

  async listOps(workspaceId: string, options: GetOperationsOptions = {}): Promise<OperationFeedResponse> {
    const query = buildQuery({
      status: options.status,
      action: options.action,
      provider: options.provider,
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<OperationFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async replayOp(workspaceId: string, opId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/ops/${encodeURIComponent(opId)}/replay`,
      correlationId,
      signal
    });
  }

  async getSyncStatus(workspaceId: string, options: GetSyncStatusOptions = {}): Promise<SyncStatusResponse> {
    const query = buildQuery({ provider: options.provider });
    return this.request<SyncStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/status${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncIngressStatus(
    workspaceId: string,
    options: GetSyncIngressStatusOptions = {}
  ): Promise<SyncIngressStatusResponse> {
    return this.request<SyncIngressStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/ingress`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncDeadLetters(
    workspaceId: string,
    options: GetSyncDeadLettersOptions = {}
  ): Promise<DeadLetterFeedResponse> {
    const query = buildQuery({
      provider: options.provider,
      cursor: options.cursor,
      limit: options.limit
    });
    return this.request<DeadLetterFeedResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getSyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<DeadLetterItem> {
    return this.request<DeadLetterItem>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}`,
      correlationId,
      signal
    });
  }

  async replaySyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}/replay`,
      correlationId,
      signal
    });
  }

  async ackSyncDeadLetter(
    workspaceId: string,
    envelopeId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<AckResponse> {
    return this.request<AckResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/dead-letter/${encodeURIComponent(envelopeId)}/ack`,
      correlationId,
      signal
    });
  }

  async triggerSyncRefresh(
    workspaceId: string,
    provider: string,
    reason?: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/refresh`,
      correlationId,
      body: {
        provider,
        reason
      },
      signal
    });
  }

  private async request<T>(params: {
    method: string;
    path: string;
    headers?: Record<string, string>;
    body?: unknown;
    correlationId?: string;
    signal?: AbortSignal;
  }): Promise<T> {
    const token = await resolveToken(this.tokenProvider);
    const correlationId = params.correlationId ?? generateCorrelationId();
    const headers: Record<string, string> = {
      "Authorization": `Bearer ${token}`,
      "Content-Type": "application/json",
      "X-Correlation-Id": correlationId,
      ...params.headers
    };
    if (this.userAgent) {
      headers["User-Agent"] = this.userAgent;
    }
    let retries = 0;
    const url = `${this.baseUrl}${params.path}`;
    for (;;) {
      const requestInit: RequestInit = {
        method: params.method,
        headers,
        body: params.body === undefined ? undefined : JSON.stringify(params.body)
      };
      if (params.signal) {
        requestInit.signal = params.signal;
      }

      let response: Response;
      try {
        response = await this.fetchImpl(url, requestInit);
      } catch (error) {
        if (this.shouldRetryError(error, retries, params.signal)) {
          retries += 1;
          await this.sleep(this.computeRetryDelayMs(retries, null), params.signal);
          continue;
        }
        throw error;
      }

      const payload = await this.readPayload(response);
      if (response.ok) {
        return payload as T;
      }

      if (this.shouldRetryStatus(response.status, retries, params.signal)) {
        retries += 1;
        await this.sleep(this.computeRetryDelayMs(retries, response.headers.get("retry-after")), params.signal);
        continue;
      }

      this.throwForError(response.status, payload, response.headers);
    }
  }

  private shouldRetryStatus(status: number, retries: number, signal?: AbortSignal): boolean {
    if (signal?.aborted) {
      return false;
    }
    if (retries >= this.retryOptions.maxRetries) {
      return false;
    }
    return status === 429 || (status >= 500 && status <= 599);
  }

  private shouldRetryError(error: unknown, retries: number, signal?: AbortSignal): boolean {
    if (signal?.aborted) {
      return false;
    }
    if (error instanceof Error && error.name === "AbortError") {
      return false;
    }
    return retries < this.retryOptions.maxRetries;
  }

  private computeRetryDelayMs(retryAttempt: number, retryAfterHeader: string | null): number {
    const retryAfterMs = this.parseRetryAfterMs(retryAfterHeader);
    if (retryAfterMs !== null) {
      return Math.min(this.retryOptions.maxDelayMs, retryAfterMs);
    }
    const backoff = this.retryOptions.baseDelayMs * Math.pow(2, Math.max(0, retryAttempt - 1));
    const capped = Math.min(this.retryOptions.maxDelayMs, backoff);
    const jitter = this.retryOptions.jitterRatio;
    const factor = 1 + (Math.random() * 2 - 1) * jitter;
    return Math.max(0, Math.round(capped * factor));
  }

  private parseRetryAfterMs(retryAfterHeader: string | null): number | null {
    if (!retryAfterHeader) {
      return null;
    }
    const seconds = Number.parseInt(retryAfterHeader, 10);
    if (Number.isFinite(seconds) && seconds >= 0) {
      return seconds * 1000;
    }
    const timestamp = Date.parse(retryAfterHeader);
    if (!Number.isNaN(timestamp)) {
      return Math.max(0, timestamp - Date.now());
    }
    return null;
  }

  private async sleep(delayMs: number, signal?: AbortSignal): Promise<void> {
    if (delayMs <= 0) {
      return;
    }
    if (signal?.aborted) {
      throw createAbortError();
    }
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      }, delayMs);
      const onAbort = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", onAbort);
        reject(createAbortError());
      };
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  }

  private async readPayload(response: Response): Promise<unknown> {
    const contentType = response.headers.get("content-type") ?? "";
    if (contentType.includes("application/json")) {
      try {
        return await response.json();
      } catch {
        return {};
      }
    }
    const text = await response.text();
    return {
      message: text
    };
  }

  private throwForError(status: number, payload: unknown, headers: Headers): never {
    const data = (payload ?? {}) as Partial<ErrorResponse> & {
      expectedRevision?: string;
      currentRevision?: string;
      currentContentPreview?: string;
    };

    if (
      status === 409 &&
      typeof data.expectedRevision === "string" &&
      typeof data.currentRevision === "string"
    ) {
      throw new RevisionConflictError(status, {
        code: data.code ?? "revision_conflict",
        message: data.message ?? "Revision conflict",
        correlationId: data.correlationId ?? "",
        details: data.details,
        expectedRevision: data.expectedRevision,
        currentRevision: data.currentRevision,
        currentContentPreview: data.currentContentPreview
      });
    }

    if (status === 409 && data.code === "invalid_state") {
      throw new InvalidStateError(status, {
        code: data.code,
        message: data.message ?? "Invalid resource state",
        correlationId: data.correlationId,
        details: data.details
      });
    }

    if (status === 429 && data.code === "queue_full") {
      const retryAfterRaw = headers.get("retry-after");
      let retryAfterSeconds: number | undefined;
      if (retryAfterRaw) {
        const parsed = Number.parseInt(retryAfterRaw, 10);
        if (Number.isFinite(parsed) && parsed >= 0) {
          retryAfterSeconds = parsed;
        }
      }
      throw new QueueFullError(
        status,
        {
          code: data.code,
          message: data.message ?? "Ingress queue full",
          correlationId: data.correlationId,
          details: data.details
        },
        retryAfterSeconds
      );
    }

    throw new RelayFileApiError(status, {
      code: data.code ?? "api_error",
      message: data.message ?? `HTTP ${status}`,
      correlationId: data.correlationId,
      details: data.details
    });
  }
}
