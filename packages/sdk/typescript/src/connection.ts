/**
 * Shared ConnectionProvider interface and related types.
 *
 * These types are the contract every provider package must implement.
 */

export type ProxyMethod = "DELETE" | "GET" | "PATCH" | "POST" | "PUT";
export type ProxyHeaders = Record<string, string>;
export type ProxyQuery = Record<string, string>;

/** Request sent through a connection provider's proxy. */
export interface ProxyRequest {
  method: ProxyMethod;
  /** Base API URL for the upstream service. */
  baseUrl: string;
  endpoint: string;
  connectionId: string;
  headers?: ProxyHeaders;
  body?: unknown;
  query?: ProxyQuery;
}

/** Response returned from a proxied request. */
export interface ProxyResponse<T = unknown> {
  status: number;
  headers: ProxyHeaders;
  data: T;
}

/** Webhook normalized into a provider-agnostic shape. */
export interface NormalizedWebhook {
  provider: string;
  connectionId: string;
  eventType: string;
  objectType: string;
  objectId: string;
  payload: Record<string, unknown>;
}

/** Interface every connection provider must implement. */
export interface ConnectionProvider {
  readonly name: string;
  proxy<T = unknown>(request: ProxyRequest): Promise<ProxyResponse<T>>;
  healthCheck(connectionId: string): Promise<boolean>;
  handleWebhook?(rawPayload: unknown): Promise<NormalizedWebhook>;
  getConnection?(connectionId: string): Promise<Record<string, unknown>>;
  listConnections?(): Promise<Array<Record<string, unknown>>>;
}

/**
 * Maps a relayfile provider slug (e.g. "github", "linear") to the provider
 * config key it is registered under in your credential backend (e.g. the Nango
 * `providerConfigKey`). The relayfile slug is NOT guaranteed to equal the
 * upstream config key, so self-host callers supply this mapping explicitly.
 */
export type ProviderConfigKeyMap = Record<string, string>;

export interface CreateConnectSessionInput {
  relayfileProvider: string;
  providerConfigKey: string;
  endUserId: string;
  connectionId?: string;
  metadata?: Record<string, unknown>;
}

export interface ConnectSession {
  connectLink: string | null;
  sessionToken: string | null;
  expiresAt: string | null;
  connectionId: string;
}

export interface GetConnectConnectionStatusInput {
  relayfileProvider: string;
  providerConfigKey: string;
  connectionId: string;
}

export interface ConnectConnectionStatus {
  connectionId: string;
  state: string;
  ready?: boolean;
  raw?: unknown;
}

export interface ConnectCapableProvider extends ConnectionProvider {
  createConnectSession(input: CreateConnectSessionInput): Promise<ConnectSession>;
  getConnectionStatus(input: GetConnectConnectionStatusInput): Promise<ConnectConnectionStatus>;
}

export function supportsConnect(provider: ConnectionProvider): provider is ConnectCapableProvider {
  const candidate = provider as Partial<ConnectCapableProvider>;
  return (
    typeof candidate.createConnectSession === "function" &&
    typeof candidate.getConnectionStatus === "function"
  );
}
