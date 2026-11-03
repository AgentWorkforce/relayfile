import { randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Duplex } from "node:stream";
import WebSocket, { WebSocketServer } from "ws";
import { normalizePath } from "@relayfile/core";
import { hasAnyScopeGrant, scopeMatchesPath } from "../runtime/scopes.js";
import { HYDRATE_HEADER, type ActorHttpRequest, type CallerContext, type SocketMetadata } from "../runtime/types.js";
import type { WorkspaceActors } from "./actors.js";
import { AuthError, BearerVerifier, verifyInternalHmac, type Principal } from "./auth.js";
import { BlobCodec, HydrateTooLarge, normalizeEncoding } from "./blob-codec.js";
import type { BlobStore } from "./blobs.js";

/**
 * Stateless relayfile gateway in front of `Workspace` actors.
 *
 * Per request: verify the bearer token, require the token's workspace to
 * equal the path's (the actor id is always the VERIFIED workspace claim),
 * move large file bytes to object storage, invoke Workspace[id].handle, and
 * hydrate blob refs in the response. Any number of replicas can run: there
 * is no per-replica state besides caches.
 */
export interface GatewayOptions {
  actors: WorkspaceActors;
  verifier: BearerVerifier;
  blobStore: BlobStore | null;
  blobPrefix: string;
  inlineMaxBytes: number;
  maxBodyBytes: number;
  internalHmacSecret: string | null;
  externalWritebackProviders: string[];
  now?: () => number;
}

const FORWARDED_HEADERS = ["if-match", "if-none-match", "content-type"];
const WORKSPACE_ID = /^[A-Za-z0-9._-]{1,128}$/;

export function createGateway(options: GatewayOptions): Server {
  const codec = new BlobCodec(options.blobStore, options.blobPrefix, options.inlineMaxBytes);
  const now = options.now ?? Date.now;
  const replayCache = new Map<string, number>();

  const server = createServer((req, res) => {
    handleHttp(req, res).catch((error) => {
      console.error(JSON.stringify({ event: "gateway.unhandled", message: error instanceof Error ? error.message : String(error) }));
      if (!res.headersSent) sendError(res, 500, "internal_error", "internal error", "");
      else res.destroy();
    });
  });

  const wss = new WebSocketServer({ noServer: true, maxPayload: 8 * 1024 * 1024 });
  server.on("upgrade", (req, socket, head) => {
    handleUpgrade(req, socket, head).catch((error) => {
      console.error(JSON.stringify({ event: "gateway.ws_error", message: error instanceof Error ? error.message : String(error) }));
      rejectUpgrade(socket, 502, "unavailable", "workspace socket unavailable");
    });
  });

  async function handleHttp(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://gateway");
    const pathname = url.pathname;

    if (req.method === "GET" && (pathname === "/health" || pathname === "/healthz")) {
      return sendJson(res, 200, { status: "ok", features: ["symlink-v1"], backend: "durable-actors" });
    }
    if (req.method === "GET" && pathname === "/readyz") {
      try {
        await options.actors.ready();
        await options.blobStore?.probe();
        return sendJson(res, 200, { status: "ok" });
      } catch (error) {
        return sendJson(res, 503, { status: "unavailable", message: error instanceof Error ? error.message : String(error) });
      }
    }

    if (req.method === "POST" && pathname === "/v1/internal/webhook-envelopes") {
      return internalEnvelope(req, res);
    }

    const match = /^\/v1\/workspaces\/([^/]+)(\/.*)$/.exec(pathname);
    if (!match) return sendError(res, 404, "not_found", "route not found", "");
    const workspaceId = safeDecode(match[1]!);
    const subPath = match[2]!;
    if (workspaceId === null || !WORKSPACE_ID.test(workspaceId)) return sendError(res, 400, "bad_request", "invalid workspace id", "");

    let principal: Principal;
    try {
      principal = await options.verifier.verify(req.headers.authorization);
    } catch (error) {
      return sendAuthError(res, error, header(req, "x-correlation-id"));
    }
    if (principal.workspaceId !== workspaceId) {
      return sendError(res, 403, "forbidden", "workspace mismatch", header(req, "x-correlation-id"));
    }
    const correlationId = header(req, "x-correlation-id");
    if (!correlationId) return sendError(res, 400, "bad_request", "missing X-Correlation-Id header", "");

    let raw: Buffer;
    try {
      raw = await readBody(req, options.maxBodyBytes);
    } catch {
      return sendError(res, 413, "payload_too_large", "request body too large", correlationId);
    }

    let body: string | null = raw.length ? raw.toString("utf8") : null;
    // Upload nothing for callers without write access to the target: the
    // actor would reject the write and the object would be orphaned (no blob GC).
    const scopes = new Set(principal.scopes);
    const mayWrite = (path: unknown) => typeof path === "string" && path.trim() !== "" && scopeMatchesPath(scopes, "fs:write", normalizePath(path));
    // Offload large file content before it reaches the actor. The raw-bytes
    // branch is chosen by route and content type, so a zero-byte streaming
    // write is still converted (wildcard If-Match default, X-Relayfile-* metadata).
    const rawWrite = req.method === "PUT" && subPath === "/fs/file" && (req.headers["content-type"] ?? "").startsWith("application/octet-stream");
    if ((rawWrite || (body && req.method === "PUT" && subPath === "/fs/file")) && !mayWrite(url.searchParams.get("path"))) {
      // Leave the body to the actor, which answers 400/403 with the canonical error.
      if (rawWrite) body = null;
    } else if (rawWrite || (body && req.method === "PUT" && subPath === "/fs/file")) {
      if (rawWrite) {
        const encoding = normalizeEncoding(header(req, "x-relayfile-encoding")) === "base64" ? "base64" : "utf-8";
        const file = await codec.offloadBytes(raw, encoding);
        const mode = header(req, "x-relayfile-mode");
        const identity = header(req, "x-relayfile-content-identity");
        body = JSON.stringify({
          ...file,
          contentType: header(req, "x-relayfile-content-type") || "application/octet-stream",
          ...(mode ? { mode: Number.parseInt(mode, 8) } : {}),
          ...(identity ? { contentIdentity: safeJson(identity) } : {}),
        });
      } else if (body) {
        const parsed = safeJson(body);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          await codec.offloadFile(parsed as Record<string, unknown>);
          body = JSON.stringify(parsed);
        }
      }
    } else if (body && req.method === "POST" && subPath === "/fs/bulk") {
      const parsed = safeJson(body) as { files?: unknown } | null;
      if (parsed && Array.isArray(parsed.files) && hasAnyScopeGrant(scopes, "fs:write")) {
        await Promise.all(
          parsed.files.map((file) => {
            if (!file || typeof file !== "object") return undefined;
            const record = file as Record<string, unknown>;
            if (mayWrite(record.path)) return codec.offloadFile(record);
            // Outside the caller's scopes: the actor rejects it; just never forward client refs.
            delete record.contentBlob;
            delete record.contentHash;
            delete record.contentSize;
            return undefined;
          }),
        );
        body = JSON.stringify(parsed);
      }
    }

    const request: ActorHttpRequest = {
      method: req.method ?? "GET",
      path: subPath,
      query: [...url.searchParams.entries()],
      headers: pickHeaders(req),
      body,
    };
    const caller: CallerContext = {
      agentName: principal.agentName,
      scopes: principal.scopes,
      correlationId,
      externalWritebackProviders: options.externalWritebackProviders,
    };
    let response;
    try {
      response = await options.actors.handle(workspaceId, request, caller);
    } catch (error) {
      console.error(JSON.stringify({ event: "gateway.actor_error", workspaceId, message: error instanceof Error ? error.message : String(error) }));
      return sendError(res, 503, "unavailable", "workspace unavailable; retry", correlationId, { "retry-after": "1" });
    }

    let responseBody = response.body;
    const headers = { ...response.headers };
    if (headers[HYDRATE_HEADER]) {
      delete headers[HYDRATE_HEADER];
      const parsed = JSON.parse(responseBody) as unknown;
      try {
        await codec.hydrate(parsed);
      } catch (error) {
        if (error instanceof HydrateTooLarge) {
          return sendError(res, 413, "export_too_large", "too much file content for one response; narrow the path", correlationId);
        }
        console.error(JSON.stringify({ event: "gateway.blob_error", workspaceId, message: error instanceof Error ? error.message : String(error) }));
        return sendError(res, 503, "unavailable", "file content unavailable; retry", correlationId, { "retry-after": "1" });
      }
      responseBody = JSON.stringify(parsed);
    }
    res.writeHead(response.status, { ...headers, "content-length": Buffer.byteLength(responseBody) });
    res.end(responseBody);
  }

  async function internalEnvelope(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const correlationId = header(req, "x-correlation-id");
    if (!correlationId) return sendError(res, 400, "bad_request", "missing X-Correlation-Id header", "");
    let raw: Buffer;
    try {
      raw = await readBody(req, 1024 * 1024);
    } catch {
      return sendError(res, 413, "payload_too_large", "request body too large", correlationId);
    }
    const timestamp = header(req, "x-relay-timestamp");
    const signature = header(req, "x-relay-signature").toLowerCase();
    const failure = verifyInternalHmac(options.internalHmacSecret ?? "", timestamp, signature, raw, now());
    if (failure) return sendError(res, failure.status, failure.code, failure.message, correlationId);
    const replayKey = `${timestamp.toLowerCase()}|${signature}`;
    for (const [key, expires] of replayCache) if (expires < now()) replayCache.delete(key);
    if (replayCache.has(replayKey)) return sendError(res, 401, "unauthorized", "internal request replay detected", correlationId);
    replayCache.set(replayKey, now() + 5 * 60_000);
    const parsed = safeJson(raw.toString("utf8")) as { workspaceId?: unknown; provider?: unknown } | null;
    if (!parsed || typeof parsed.workspaceId !== "string" || !WORKSPACE_ID.test(parsed.workspaceId) || typeof parsed.provider !== "string") {
      return sendError(res, 400, "bad_request", "workspaceId and provider are required", correlationId);
    }
    try {
      // The signature goes to the actor too: an envelope without a deliveryId
      // is deduplicated on it there, so a replay through another gateway
      // replica (whose replay cache never saw it) is not applied twice.
      const response = await options.actors.handle(
        parsed.workspaceId,
        { method: "POST", path: "/internal/webhook-envelopes", query: [], headers: { "x-relay-signature": signature }, body: raw.toString("utf8") },
        { agentName: "relayfile-internal", scopes: [], correlationId, internal: true, externalWritebackProviders: options.externalWritebackProviders },
      );
      res.writeHead(response.status, response.headers);
      res.end(response.body);
    } catch {
      // Let the sender retry the same signed request: the actor deduplicates
      // it (by deliveryId or signature) if the failed call had committed.
      replayCache.delete(replayKey);
      return sendError(res, 503, "unavailable", "workspace unavailable; retry", correlationId, { "retry-after": "1" });
    }
  }

  async function handleUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer): Promise<void> {
    const url = new URL(req.url ?? "/", "http://gateway");
    const match = /^\/v1\/workspaces\/([^/]+)\/fs\/ws$/.exec(url.pathname);
    if (!match) return rejectUpgrade(socket, 404, "not_found", "route not found");
    const workspaceId = safeDecode(match[1]!);
    if (workspaceId === null) return rejectUpgrade(socket, 400, "bad_request", "invalid workspace id");
    const token = url.searchParams.get("token");
    let principal: Principal;
    try {
      principal = token
        ? await options.verifier.verifyToken(token)
        : await options.verifier.verify(req.headers.authorization);
    } catch (error) {
      const status = error instanceof AuthError ? error.status : 401;
      const code = error instanceof AuthError ? error.code : "unauthorized";
      return rejectUpgrade(socket, status, code, error instanceof Error ? error.message : "unauthorized");
    }
    if (principal.workspaceId !== workspaceId) return rejectUpgrade(socket, 403, "forbidden", "workspace mismatch");
    const paths = url.searchParams.getAll("path").map((path) => (path.trim() ? normalizePath(path) : path));
    const scopes = new Set(principal.scopes);
    for (const path of paths.length ? paths : ["/"]) {
      if (!scopeMatchesPath(scopes, "fs:read", path)) return rejectUpgrade(socket, 403, "forbidden", "missing required scope: fs:read");
    }
    const metadata: SocketMetadata = {
      agentName: principal.agentName,
      scopes: principal.scopes,
      cursor: url.searchParams.get("cursor"),
      fromNow: url.searchParams.get("from") === "now",
      paths,
    };
    const grantUrl = await options.actors.socketGrant(workspaceId, metadata);
    const upstream = new WebSocket(grantUrl, { maxPayload: 8 * 1024 * 1024, handshakeTimeout: 10_000 });
    // Frames the actor sends before the client upgrade completes (catch-up) are buffered.
    const early: string[] = [];
    let client: WebSocket | null = null;
    upstream.on("message", (data, isBinary) => {
      if (isBinary) return;
      const text = data.toString();
      if (client) client.send(text);
      else early.push(text);
    });
    await new Promise<void>((resolve, reject) => {
      upstream.once("open", resolve);
      upstream.once("error", reject);
      upstream.once("unexpected-response", (_r, response) => reject(new Error(`actor socket refused: HTTP ${response.statusCode}`)));
    });
    wss.handleUpgrade(req, socket, head, (ws) => {
      client = ws;
      for (const text of early.splice(0)) ws.send(text);
      ws.on("message", (data, isBinary) => {
        if (isBinary) return;
        const text = data.toString();
        // Answer application pings here: no need to wake the actor.
        const parsed = safeJson(text) as { type?: unknown } | null;
        if (parsed && parsed.type === "ping") {
          ws.send(JSON.stringify({ type: "pong", timestamp: new Date(now()).toISOString() }));
          return;
        }
        if (upstream.readyState === WebSocket.OPEN) upstream.send(text);
      });
      ws.on("close", () => upstream.close(1000));
      ws.on("error", () => upstream.close(1000));
      upstream.on("close", (code) => {
        if (ws.readyState === WebSocket.OPEN) ws.close(code === 1000 ? 1000 : 1011, "workspace socket closed");
      });
      upstream.on("error", () => {
        if (ws.readyState === WebSocket.OPEN) ws.close(1011, "workspace socket error");
      });
    });
  }

  return server;
}

function header(req: IncomingMessage, name: string): string {
  const value = req.headers[name];
  return (Array.isArray(value) ? value[0] : value)?.trim() ?? "";
}

function pickHeaders(req: IncomingMessage): Record<string, string> {
  const out: Record<string, string> = {};
  for (const name of FORWARDED_HEADERS) {
    const value = header(req, name);
    if (value) out[name] = value;
  }
  return out;
}

function safeDecode(value: string): string | null {
  try {
    return decodeURIComponent(value);
  } catch {
    return null;
  }
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

async function readBody(req: IncomingMessage, limit: number): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > limit) throw new Error("body too large");
    chunks.push(chunk as Buffer);
  }
  return Buffer.concat(chunks);
}

function sendJson(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
  const text = JSON.stringify(body);
  res.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(text), ...headers });
  res.end(text);
}

function sendError(
  res: ServerResponse,
  status: number,
  code: string,
  message: string,
  correlationId: string,
  headers: Record<string, string> = {},
): void {
  sendJson(res, status, { code, message, correlationId }, headers);
}

function sendAuthError(res: ServerResponse, error: unknown, correlationId: string): void {
  if (error instanceof AuthError) return sendError(res, error.status, error.code, error.message, correlationId);
  return sendError(res, 401, "unauthorized", "unauthorized", correlationId);
}

function rejectUpgrade(socket: Duplex, status: number, code: string, message: string): void {
  if (socket.destroyed) return;
  const body = JSON.stringify({ code, message, correlationId: "" });
  socket.write(
    `HTTP/1.1 ${status} ${status === 401 ? "Unauthorized" : status === 403 ? "Forbidden" : status === 404 ? "Not Found" : "Error"}\r\n` +
      `Content-Type: application/json\r\nContent-Length: ${Buffer.byteLength(body)}\r\nConnection: close\r\n\r\n${body}`,
  );
  socket.destroy();
}

export function newCorrelationId(): string {
  return `gw_${randomUUID()}`;
}
