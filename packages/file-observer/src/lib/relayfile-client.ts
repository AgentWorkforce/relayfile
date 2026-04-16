export const DEFAULT_RELAYFILE_URL = 'https://api.relayfile.dev';

export type FileNodeType = 'file' | 'dir';

export interface TreeEntry {
  path: string;
  type: FileNodeType;
  revision: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
  propertyCount?: number;
  relationCount?: number;
  permissionCount?: number;
  commentCount?: number;
}

export interface TreeResponse {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

export interface FileSemantics {
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

export interface FileReadResponse {
  path: string;
  revision: string;
  contentType: string;
  content: string;
  encoding?: 'utf-8' | 'base64';
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  semantics?: FileSemantics;
}

export interface FileQueryItem {
  path: string;
  revision: string;
  contentType: string;
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  size: number;
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

export interface FileQueryResponse {
  items: FileQueryItem[];
  nextCursor: string | null;
}

export type FilesystemEventType =
  | 'file.created'
  | 'file.updated'
  | 'file.deleted'
  | 'dir.created'
  | 'dir.deleted'
  | 'sync.error'
  | 'sync.ignored'
  | 'sync.suppressed'
  | 'sync.stale'
  | 'writeback.failed'
  | 'writeback.succeeded';

export type EventOrigin = 'provider_sync' | 'agent_write' | 'system';

export interface FilesystemEvent {
  eventId: string;
  type: FilesystemEventType;
  path: string;
  revision: string;
  origin?: EventOrigin;
  provider?: string;
  correlationId?: string;
  timestamp: string;
}

export interface RelayfileApiErrorPayload {
  code?: string;
  message?: string;
  details?: Record<string, unknown>;
}

export class RelayfileApiError extends Error {
  readonly status: number;
  readonly statusText: string;
  readonly code?: string;
  readonly details?: Record<string, unknown>;

  constructor(response: Response, payload?: RelayfileApiErrorPayload) {
    super(payload?.message ?? `${response.status} ${response.statusText}`);
    this.name = 'RelayfileApiError';
    this.status = response.status;
    this.statusText = response.statusText;
    this.code = payload?.code;
    this.details = payload?.details;
  }
}

export interface ListTreeOptions {
  path?: string;
  depth?: number;
  cursor?: string;
  correlationId?: string;
  signal?: AbortSignal;
}

export interface ReadFileOptions {
  correlationId?: string;
  signal?: AbortSignal;
}

export interface QueryFilesOptions {
  path?: string;
  provider?: string;
  relation?: string;
  permission?: string;
  comment?: string;
  properties?: Record<string, string>;
  cursor?: string;
  limit?: number;
  correlationId?: string;
  signal?: AbortSignal;
}

type WebSocketEventName = 'event' | 'open' | 'close' | 'error';

type WebSocketHandlerMap = {
  event: (event: FilesystemEvent) => void;
  open: (event: Event) => void;
  close: (event: CloseEvent) => void;
  error: (event: Event | Error) => void;
};

export interface RelayfileWebSocketConnection {
  close(code?: number, reason?: string): void;
  on<TName extends WebSocketEventName>(event: TName, handler: WebSocketHandlerMap[TName]): () => void;
}

export interface ConnectWebSocketOptions {
  token?: string;
  onEvent?: (event: FilesystemEvent) => void;
}

export interface RelayfileClientOptions {
  baseUrl?: string;
  token?: string;
  fetchImpl?: typeof fetch;
  webSocketFactory?: (url: string) => WebSocket;
}

interface RequestOptions {
  correlationId?: string;
  signal?: AbortSignal;
}

type EnvMap = Record<string, string | undefined>;

function getEnv(): EnvMap {
  if (typeof globalThis !== 'undefined') {
    const processValue = (globalThis as { process?: { env?: EnvMap } }).process;
    if (processValue?.env) {
      return processValue.env;
    }
  }
  return {};
}

function readEnvVariable(name: 'RELAYFILE_URL' | 'RELAYFILE_TOKEN'): string | undefined {
  const value = getEnv()[name];
  if (typeof value !== 'string') {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === '' ? undefined : trimmed;
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      search.set(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded === '' ? '' : `?${encoded}`;
}

function createCorrelationId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `rf_${crypto.randomUUID()}`;
  }
  return `rf_${Date.now()}`;
}

function resolveBaseUrl(baseUrl?: string): string {
  return (baseUrl ?? readEnvVariable('RELAYFILE_URL') ?? DEFAULT_RELAYFILE_URL).replace(/\/+$/, '');
}

function resolveToken(token?: string): string {
  const resolved = token?.trim() || readEnvVariable('RELAYFILE_TOKEN');
  if (!resolved) {
    throw new Error('Relayfile authentication is not configured. Set RELAYFILE_TOKEN or pass a token explicitly.');
  }
  return resolved;
}

async function parseErrorPayload(response: Response): Promise<RelayfileApiErrorPayload | undefined> {
  const contentType = response.headers.get('content-type') ?? '';
  if (!contentType.toLowerCase().includes('application/json')) {
    return undefined;
  }
  try {
    return (await response.json()) as RelayfileApiErrorPayload;
  } catch {
    return undefined;
  }
}

class DefaultRelayfileWebSocketConnection implements RelayfileWebSocketConnection {
  private readonly socket: WebSocket;
  private readonly handlers: {
    [K in WebSocketEventName]: Set<WebSocketHandlerMap[K]>;
  } = {
    event: new Set(),
    open: new Set(),
    close: new Set(),
    error: new Set()
  };

  constructor(socket: WebSocket, onEvent?: (event: FilesystemEvent) => void) {
    this.socket = socket;
    if (onEvent) {
      this.handlers.event.add(onEvent);
    }

    socket.addEventListener('open', (event) => {
      for (const handler of this.handlers.open) {
        handler(event);
      }
    });

    socket.addEventListener('close', (event) => {
      for (const handler of this.handlers.close) {
        handler(event);
      }
    });

    socket.addEventListener('error', (event) => {
      const normalized = event instanceof ErrorEvent && event.error instanceof Error ? event.error : event;
      for (const handler of this.handlers.error) {
        handler(normalized);
      }
    });

    socket.addEventListener('message', (event) => {
      if (typeof event.data !== 'string') {
        return;
      }

      let parsed: FilesystemEvent;
      try {
        const raw = JSON.parse(event.data) as Partial<FilesystemEvent> | null;
        if (!raw || typeof raw !== 'object' || typeof raw.type !== 'string') {
          throw new Error("Invalid Relayfile WebSocket event: missing required 'type' field.");
        }
        if (typeof raw.path !== 'string') {
          throw new Error("Invalid Relayfile WebSocket event: missing required 'path' field.");
        }
        if (typeof raw.revision !== 'string') {
          throw new Error("Invalid Relayfile WebSocket event: missing required 'revision' field.");
        }
        if (typeof raw.timestamp !== 'string') {
          throw new Error("Invalid Relayfile WebSocket event: missing required 'timestamp' field.");
        }

        parsed = {
          eventId: typeof raw.eventId === 'string' ? raw.eventId : '',
          type: raw.type as FilesystemEventType,
          path: raw.path,
          revision: raw.revision,
          origin: raw.origin,
          provider: raw.provider,
          correlationId: raw.correlationId,
          timestamp: raw.timestamp
        };
      } catch (error) {
        const normalized = error instanceof Error ? error : new Error('Failed to parse Relayfile WebSocket event payload.');
        for (const handler of this.handlers.error) {
          handler(normalized);
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

  on<TName extends WebSocketEventName>(event: TName, handler: WebSocketHandlerMap[TName]): () => void {
    this.handlers[event].add(handler);
    return () => {
      this.handlers[event].delete(handler);
    };
  }
}

export class RelayfileClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly fetchImpl: typeof fetch;
  private readonly webSocketFactory?: (url: string) => WebSocket;

  constructor(options: RelayfileClientOptions = {}) {
    this.baseUrl = resolveBaseUrl(options.baseUrl);
    this.token = resolveToken(options.token);
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.webSocketFactory = options.webSocketFactory;
  }

  async listTree(workspaceId: string, options: ListTreeOptions = {}): Promise<TreeResponse> {
    const query = buildQuery({
      path: options.path ?? '/',
      depth: options.depth,
      cursor: options.cursor
    });

    return this.request<TreeResponse>(
      `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/tree${query}`,
      options
    );
  }

  async readFile(workspaceId: string, path: string, options: ReadFileOptions = {}): Promise<FileReadResponse> {
    const query = buildQuery({ path });
    return this.request<FileReadResponse>(
      `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`,
      options
    );
  }

  async queryFiles(workspaceId: string, options: QueryFilesOptions = {}): Promise<FileQueryResponse> {
    const search = new URLSearchParams();
    if (options.path !== undefined) search.set('path', options.path);
    if (options.provider !== undefined) search.set('provider', options.provider);
    if (options.relation !== undefined) search.set('relation', options.relation);
    if (options.permission !== undefined) search.set('permission', options.permission);
    if (options.comment !== undefined) search.set('comment', options.comment);
    if (options.cursor !== undefined) search.set('cursor', options.cursor);
    if (options.limit !== undefined) search.set('limit', String(options.limit));
    if (options.properties) {
      for (const [key, value] of Object.entries(options.properties)) {
        if (key !== '' && value !== undefined) {
          search.set(`property.${key}`, value);
        }
      }
    }

    const query = search.toString();
    return this.request<FileQueryResponse>(
      `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/query${query === '' ? '' : `?${query}`}`,
      options
    );
  }

  connectWebSocket(
    workspaceId: string,
    options: ConnectWebSocketOptions = {}
  ): RelayfileWebSocketConnection {
    const WebSocketImpl = this.webSocketFactory ?? getGlobalWebSocket();
    const url = new URL(`${this.baseUrl}/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/ws`);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    url.searchParams.set('token', options.token?.trim() || this.token);

    const socket = WebSocketImpl(url.toString());
    return new DefaultRelayfileWebSocketConnection(socket, options.onEvent);
  }

  private async request<T>(path: string, options: RequestOptions): Promise<T> {
    const response = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method: 'GET',
      signal: options.signal,
      headers: {
        Accept: 'application/json',
        Authorization: `Bearer ${this.token}`,
        'X-Correlation-Id': options.correlationId ?? createCorrelationId()
      }
    });

    if (!response.ok) {
      throw new RelayfileApiError(response, await parseErrorPayload(response));
    }

    return (await response.json()) as T;
  }
}

function getGlobalWebSocket(): (url: string) => WebSocket {
  if (typeof WebSocket !== 'function') {
    throw new Error('WebSocket is not available in this runtime. Provide a webSocketFactory or run in a browser-like environment.');
  }

  return (url: string) => new WebSocket(url);
}

let defaultClient: RelayfileClient | undefined;

export function createRelayfileClient(options: RelayfileClientOptions = {}): RelayfileClient {
  return new RelayfileClient(options);
}

function getDefaultClient(): RelayfileClient {
  if (!defaultClient) {
    defaultClient = createRelayfileClient();
  }
  return defaultClient;
}

export function listTree(workspaceId: string, options: ListTreeOptions = {}): Promise<TreeResponse> {
  return getDefaultClient().listTree(workspaceId, options);
}

export function readFile(
  workspaceId: string,
  path: string,
  options: ReadFileOptions = {}
): Promise<FileReadResponse> {
  return getDefaultClient().readFile(workspaceId, path, options);
}

export function queryFiles(
  workspaceId: string,
  options: QueryFilesOptions = {}
): Promise<FileQueryResponse> {
  return getDefaultClient().queryFiles(workspaceId, options);
}

export function connectWebSocket(
  workspaceId: string,
  options: ConnectWebSocketOptions = {}
): RelayfileWebSocketConnection {
  return getDefaultClient().connectWebSocket(workspaceId, options);
}
