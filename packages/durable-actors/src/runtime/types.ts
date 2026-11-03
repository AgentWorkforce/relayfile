/**
 * Wire types between the stateless gateway and the `Workspace` actor.
 * Actor RPC arguments and results are JSON, so everything here is plain data.
 */

/** One HTTP request, serialized by the gateway after it verified the bearer token. */
export interface ActorHttpRequest {
  method: string;
  /** Path relative to the workspace, e.g. "/fs/file" for /v1/workspaces/{id}/fs/file. */
  path: string;
  query: Array<[string, string]>;
  /** Lower-cased header names; only the headers the runtime reads. */
  headers: Record<string, string>;
  /** UTF-8 body (JSON for every route the actor serves), or null. */
  body: string | null;
}

export interface ActorHttpResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
}

/**
 * Who is calling, as verified by the gateway. The actor trusts it because
 * only the gateway can invoke actors (it alone holds project sessions).
 */
export interface CallerContext {
  agentName: string;
  scopes: string[];
  correlationId: string;
  /** Trusted internal ingestion (HMAC-verified /v1/internal/webhook-envelopes). */
  internal?: boolean;
  /**
   * Providers whose writebacks an external worker performs (it polls
   * /writeback/pending and acks). Writes under any other provider complete
   * immediately, like the Go server without provider adapters. "*" = all.
   */
  externalWritebackProviders?: string[];
}

/**
 * File bytes that the gateway moved to object storage. In request bodies it
 * replaces `content` on a file; in actor responses the actor emits it instead
 * of `content` and the gateway hydrates it before answering the client.
 */
export interface ContentBlobRef {
  sha256: string;
  size: number;
}

/** Response header set by the actor when the JSON body contains `contentBlob` refs to hydrate. */
export const HYDRATE_HEADER = "x-relayfile-hydrate";

/** Metadata attached to an actor WebSocket grant for /fs/ws. */
export interface SocketMetadata {
  agentName: string;
  scopes: string[];
  /** Exclusive event cursor to replay from, if the client supplied one. */
  cursor: string | null;
  /** `from=now`: no catch-up replay. */
  fromNow: boolean;
  /** Path filters (exact, `*` one segment, trailing `**`); empty = everything readable. */
  paths: string[];
}
