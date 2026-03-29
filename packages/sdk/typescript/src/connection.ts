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
