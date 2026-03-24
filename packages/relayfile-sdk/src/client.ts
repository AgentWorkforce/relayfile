import {
  type AdminIngressStatusResponse,
  type AdminSyncStatusResponse,
  type BulkWriteInput,
  type BulkWriteResponse,
  type BackendStatusResponse,
  type AckResponse,
  type DeleteFileInput,
  type DeadLetterItem,
  type DeadLetterFeedResponse,
  type ErrorResponse,
  type EventFeedResponse,
  type ExportJsonResponse,
  type ExportOptions,
  type FileQueryResponse,
  type FileReadResponse,
  type FilesystemEvent,
  type GetEventsOptions,
  type GetAdminIngressStatusOptions,
  type GetAdminSyncStatusOptions,
  type GetOperationsOptions,
  type GetSyncDeadLettersOptions,
  type GetSyncIngressStatusOptions,
  type GetSyncStatusOptions,
  type ListTreeOptions,
  type OperationFeedResponse,
  type OperationStatusResponse,
  type QueuedResponse,
  type QueryFilesOptions,
  type SyncIngressStatusResponse,
  type SyncStatusResponse,
  type TreeResponse,
  type WriteFileInput,
  type WriteQueuedResponse,
  type IngestWebhookInput,
  type WritebackItem,
  type AckWritebackInput,
  type AckWritebackResponse
} from "./types.js";
import {
  InvalidStateError,
  PayloadTooLargeError,
  QueueFullError,
  RelayFileApiError,
  RevisionConflictError
} from "./errors.js";

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

type WebSocketEventName = "event" | "error" | "open" | "close";
type WebSocketHandlerMap = {
  event: (event: FilesystemEvent) => void;
  error: (event: Event | Error) => void;
  open: (event: Event) => void;
  close: (event: CloseEvent) => void;
};

export interface WebSocketConnection {
  close(code?: number, reason?: string): void;
  on<TEventName extends WebSocketEventName>(event: TEventName, handler: WebSocketHandlerMap[TEventName]): () => void;
}

export interface ConnectWebSocketOptions {
  token?: string;
  onEvent?: (event: FilesystemEvent) => void;
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

function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
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

function resolveSyncToken(tokenProvider: AccessTokenProvider): string {
  if (typeof tokenProvider === "function") {
    const token = tokenProvider();
    if (typeof token !== "string") {
      throw new Error("connectWebSocket requires a synchronous token provider or an explicit token option.");
    }
    return token;
  }
  return tokenProvider;
}

function createAbortError(): Error {
  const err = new Error("The operation was aborted.");
  err.name = "AbortError";
  return err;
}

function createBlobFromResponse(response: Response): Promise<Blob> {
  if (typeof response.blob === "function") {
    return response.blob();
  }
  return response.arrayBuffer().then((buffer) => new Blob([buffer], { type: response.headers.get("content-type") ?? undefined }));
}

class RelayFileWebSocketConnection implements WebSocketConnection {
  private readonly socket: WebSocket;
  private readonly handlers: {
    [K in WebSocketEventName]: Set<WebSocketHandlerMap[K]>;
  } = {
    event: new Set(),
    error: new Set(),
    open: new Set(),
    close: new Set()
  };

  constructor(socket: WebSocket, onEvent?: (event: FilesystemEvent) => void) {
    this.socket = socket;
    if (onEvent) {
      this.handlers.event.add(onEvent);
    }

    socket.addEventListener("open", (event) => {
      for (const handler of this.handlers.open) {
        handler(event);
      }
    });

    socket.addEventListener("close", (event) => {
      for (const handler of this.handlers.close) {
        handler(event);
      }
    });

    socket.addEventListener("error", (event) => {
      const errorEvent = event instanceof ErrorEvent && event.error instanceof Error ? event.error : event;
      for (const handler of this.handlers.error) {
        handler(errorEvent);
      }
    });

    socket.addEventListener("message", (event) => {
      if (typeof event.data !== "string") {
        return;
      }
      let parsed: FilesystemEvent;
      try {
        parsed = JSON.parse(event.data) as FilesystemEvent;
      } catch (error) {
        const parseError = error instanceof Error ? error : new Error("Failed to parse WebSocket event payload.");
        for (const handler of this.handlers.error) {
          handler(parseError);
        }
        return;
      }
      for (const handler of this.handlers.event) {
        handler(parsed);
      }
    });
  }

  close(code?: number, reason?: string): void {
    this.socket.close(code, reason);
  }

  on<TEventName extends WebSocketEventName>(event: TEventName, handler: WebSocketHandlerMap[TEventName]): () => void {
    this.handlers[event].add(handler);
    return () => {
      this.handlers[event].delete(handler);
    };
  }
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

  async queryFiles(workspaceId: string, options: QueryFilesOptions = {}): Promise<FileQueryResponse> {
    const params = new URLSearchParams();
    if (options.path !== undefined) params.set("path", options.path);
    if (options.provider !== undefined) params.set("provider", options.provider);
    if (options.relation !== undefined) params.set("relation", options.relation);
    if (options.permission !== undefined) params.set("permission", options.permission);
    if (options.comment !== undefined) params.set("comment", options.comment);
    if (options.cursor !== undefined) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    if (options.properties !== undefined) {
      for (const [key, value] of Object.entries(options.properties)) {
        if (key !== "" && value !== undefined) {
          params.set(`property.${key}`, value);
        }
      }
    }
    const encoded = params.toString();
    const query = encoded ? `?${encoded}` : "";
    return this.request<FileQueryResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/query${query}`,
      correlationId: options.correlationId,
      signal: options.signal
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
        content: input.content,
        encoding: input.encoding,
        semantics: input.semantics
      },
      signal: input.signal
    });
  }

  async bulkWrite(input: BulkWriteInput): Promise<BulkWriteResponse> {
    return this.request<BulkWriteResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/fs/bulk`,
      correlationId: input.correlationId,
      body: {
        files: input.files
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
      provider: options.provider,
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

  async exportWorkspace(options: ExportOptions): Promise<ExportJsonResponse | Blob> {
    const format = options.format ?? "json";
    const query = buildQuery({ format });
    const correlationId = options.correlationId ?? generateCorrelationId();
    const response = await this.performRequest({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(options.workspaceId)}/fs/export${query}`,
      correlationId,
      signal: options.signal,
      accept: format === "json" ? "application/json" : "*/*"
    });

    if (format === "json") {
      return this.readPayload(response) as Promise<ExportJsonResponse>;
    }

    return createBlobFromResponse(response);
  }

  connectWebSocket(
    workspaceId: string,
    options: ConnectWebSocketOptions = {}
  ): WebSocketConnection {
    const token = options.token ?? resolveSyncToken(this.tokenProvider);
    const url = new URL(`${this.baseUrl}/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/ws`);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("token", token);

    const socket = new WebSocket(url.toString());
    return new RelayFileWebSocketConnection(socket, options.onEvent);
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

  async replayAdminEnvelope(envelopeId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/admin/replay/envelope/${encodeURIComponent(envelopeId)}`,
      correlationId,
      signal
    });
  }

  async replayAdminOp(opId: string, correlationId?: string, signal?: AbortSignal): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/admin/replay/op/${encodeURIComponent(opId)}`,
      correlationId,
      signal
    });
  }

  async getBackendStatus(correlationId?: string, signal?: AbortSignal): Promise<BackendStatusResponse> {
    return this.request<BackendStatusResponse>({
      method: "GET",
      path: "/v1/admin/backends",
      correlationId,
      signal
    });
  }

  async getAdminIngressStatus(
    optionsOrCorrelationId: GetAdminIngressStatusOptions | string = {},
    signal?: AbortSignal
  ): Promise<AdminIngressStatusResponse> {
    const options: GetAdminIngressStatusOptions =
      typeof optionsOrCorrelationId === "string"
        ? { correlationId: optionsOrCorrelationId, signal }
        : optionsOrCorrelationId;
    const query = buildQuery({
      workspaceId: options.workspaceId,
      provider: options.provider,
      alertProfile: options.alertProfile,
      pendingThreshold: options.pendingThreshold,
      deadLetterThreshold: options.deadLetterThreshold,
      staleThreshold: options.staleThreshold,
      dropRateThreshold: options.dropRateThreshold,
      nonZeroOnly: options.nonZeroOnly,
      maxAlerts: options.maxAlerts,
      cursor: options.cursor,
      limit: options.limit,
      includeWorkspaces: options.includeWorkspaces,
      includeAlerts: options.includeAlerts
    });
    return this.request<AdminIngressStatusResponse>({
      method: "GET",
      path: `/v1/admin/ingress${query}`,
      correlationId: options.correlationId,
      signal: options.signal
    });
  }

  async getAdminSyncStatus(
    optionsOrCorrelationId: GetAdminSyncStatusOptions | string = {},
    signal?: AbortSignal
  ): Promise<AdminSyncStatusResponse> {
    const options: GetAdminSyncStatusOptions =
      typeof optionsOrCorrelationId === "string"
        ? { correlationId: optionsOrCorrelationId, signal }
        : optionsOrCorrelationId;
    const query = buildQuery({
      workspaceId: options.workspaceId,
      provider: options.provider,
      nonZeroOnly: options.nonZeroOnly,
      cursor: options.cursor,
      limit: options.limit,
      includeWorkspaces: options.includeWorkspaces,
      statusErrorThreshold: options.statusErrorThreshold,
      lagSecondsThreshold: options.lagSecondsThreshold,
      deadLetteredEnvelopesThreshold: options.deadLetteredEnvelopesThreshold,
      deadLetteredOpsThreshold: options.deadLetteredOpsThreshold,
      maxAlerts: options.maxAlerts,
      includeAlerts: options.includeAlerts
    });
    return this.request<AdminSyncStatusResponse>({
      method: "GET",
      path: `/v1/admin/sync${query}`,
      correlationId: options.correlationId,
      signal: options.signal
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
    const query = buildQuery({
      provider: options.provider
    });
    return this.request<SyncIngressStatusResponse>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/ingress${query}`,
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

  async ingestWebhook(input: IngestWebhookInput): Promise<QueuedResponse> {
    return this.request<QueuedResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/webhooks/ingest`,
      correlationId: input.correlationId,
      body: {
        provider: input.provider,
        event_type: input.event_type,
        path: input.path,
        data: input.data,
        delivery_id: input.delivery_id,
        timestamp: input.timestamp,
        headers: input.headers
      },
      signal: input.signal
    });
  }

  async listPendingWritebacks(
    workspaceId: string,
    correlationId?: string,
    signal?: AbortSignal
  ): Promise<WritebackItem[]> {
    return this.request<WritebackItem[]>({
      method: "GET",
      path: `/v1/workspaces/${encodeURIComponent(workspaceId)}/writeback/pending`,
      correlationId,
      signal
    });
  }

  async ackWriteback(input: AckWritebackInput): Promise<AckWritebackResponse> {
    return this.request<AckWritebackResponse>({
      method: "POST",
      path: `/v1/workspaces/${encodeURIComponent(input.workspaceId)}/writeback/${encodeURIComponent(input.itemId)}/ack`,
      correlationId: input.correlationId,
      body: {
        success: input.success,
        error: input.error
      },
      signal: input.signal
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
    const response = await this.performRequest(params);
    return this.readPayload(response) as Promise<T>;
  }

  private async performRequest(params: {
    method: string;
    path: string;
    headers?: Record<string, string>;
    body?: unknown;
    correlationId?: string;
    signal?: AbortSignal;
    accept?: string;
  }): Promise<Response> {
    const correlationId = params.correlationId ?? generateCorrelationId();
    const baseHeaders: Record<string, string> = {
      "X-Correlation-Id": correlationId,
      ...params.headers
    };
    if (params.body !== undefined) {
      baseHeaders["Content-Type"] = "application/json";
    }
    if (params.accept) {
      baseHeaders["Accept"] = params.accept;
    }
    if (this.userAgent) {
      baseHeaders["User-Agent"] = this.userAgent;
    }

    let retries = 0;
    const url = `${this.baseUrl}${params.path}`;
    for (;;) {
      const token = await resolveToken(this.tokenProvider);
      const headers: Record<string, string> = {
        Authorization: `Bearer ${token}`,
        ...baseHeaders
      };
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

      if (response.ok) {
        return response;
      }

      const payload = await this.readPayload(response);
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

    if (status === 413) {
      throw new PayloadTooLargeError(status, {
        code: data.code ?? "payload_too_large",
        message: data.message ?? "Request payload exceeds configured limit",
        correlationId: data.correlationId,
        details: data.details
      });
    }

    throw new RelayFileApiError(status, {
      code: data.code ?? "api_error",
      message: data.message ?? `HTTP ${status}`,
      correlationId: data.correlationId,
      details: data.details
    });
  }
}
