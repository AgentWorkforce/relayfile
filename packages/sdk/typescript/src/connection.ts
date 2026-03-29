/**
 * Shared ConnectionProvider interface and related types.
 *
 * These types are the contract every provider package must implement.
 */

export type ProxyMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
export type ProxyHeaders = Record<string, string>;
export type ProxyQuery = Record<string, string>;

/** Request sent through a connection provider's proxy. */
export interface ProxyRequest {
  method: ProxyMethod;
  /** Optional — providers resolve it from the connection when omitted. */
  baseUrl?: string;
  endpoint: string;
  connectionId: string;
  headers?: ProxyHeaders;
  body?: unknown;
  query?: ProxyQuery;
}

/** Response returned from a proxied request. */
export interface ProxyResponse {
  status: number;
  headers: ProxyHeaders;
  data: unknown;
}

/** Webhook normalized into a provider-agnostic shape. */
export interface NormalizedWebhook {
  provider: string;
  connectionId: string;
  eventType: string;
  objectType: string;
  objectId: string;
  payload: Record<string, unknown>;
  relations?: string[];
  metadata?: Record<string, string>;
}

/** Interface every connection provider must implement. */
export interface ConnectionProvider {
  readonly name: string;
  proxy(request: ProxyRequest): Promise<ProxyResponse>;
  healthCheck(connectionId: string): Promise<boolean>;
  handleWebhook?(rawPayload: unknown): Promise<NormalizedWebhook>;
}
