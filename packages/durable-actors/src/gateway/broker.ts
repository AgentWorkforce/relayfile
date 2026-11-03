import { createServer, type IncomingMessage, type Server } from "node:http";
import { timingSafeEqual } from "node:crypto";
import { findWebsocket, issueSession } from "./actors.js";

/**
 * Session broker: the only relayfile process that holds the durable-actors
 * admin api-key. It runs as an internal Deployment that only gateway pods can
 * reach (NetworkPolicy) and exposes exactly two operations for one project:
 *
 *   POST /v1/session        -> a <=60s project session (invoke-only: sessions
 *                              cannot deploy, delete, or open sockets)
 *   POST /v1/socket-grant   -> a WebSocket URL for one Workspace actor
 *
 * A compromised internet-facing gateway can therefore invoke Workspace
 * actors but cannot register or delete deployments, read observability
 * APIs, or touch other projects.
 */
export interface BrokerConfig {
  port: number;
  controlPlaneUrl: string;
  projectId: string;
  adminKey: string;
  brokerToken: string;
  fetch?: typeof fetch;
}

const ACTOR_ID = /^[A-Za-z0-9._-]{1,128}$/;
const MAX_BODY = 64 * 1024;

export function createBroker(config: BrokerConfig): Server {
  if (!config.adminKey) throw new Error("DURABLE_ACTORS_SECRET is required for the session broker");
  if (!config.brokerToken || config.brokerToken.length < 32) {
    throw new Error("RELAYFILE_ACTORS_BROKER_TOKEN must be at least 32 characters");
  }
  if (config.brokerToken === config.adminKey) {
    throw new Error("RELAYFILE_ACTORS_BROKER_TOKEN must differ from the durable-actors admin key");
  }
  const fetchImpl = config.fetch ?? fetch;
  const expected = Buffer.from(`Bearer ${config.brokerToken}`);
  const authorized = (req: IncomingMessage) => {
    const header = Buffer.from(req.headers.authorization ?? "");
    return header.length === expected.length && timingSafeEqual(header, expected);
  };

  return createServer(async (req, res) => {
    const send = (status: number, body: unknown) => {
      res.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" });
      res.end(JSON.stringify(body));
    };
    try {
      if (req.method === "GET" && (req.url === "/healthz" || req.url === "/health")) return send(200, { ok: true });
      if (req.method === "GET" && req.url === "/readyz") {
        const health = await fetchImpl(`${config.controlPlaneUrl.replace(/\/$/, "")}/healthz`, {
          signal: AbortSignal.timeout(3_000),
        }).catch(() => null);
        await health?.body?.cancel();
        return send(health?.ok ? 200 : 503, { ok: Boolean(health?.ok) });
      }
      if (req.method !== "POST") return send(405, { code: "method_not_allowed" });
      if (!authorized(req)) return send(401, { code: "unauthorized" });
      if (req.url === "/v1/session") {
        await drain(req);
        const grant = await issueSession(fetchImpl, config.controlPlaneUrl, config.projectId, config.adminKey, "relayfile-gateway");
        return send(200, grant);
      }
      if (req.url === "/v1/socket-grant") {
        const body = JSON.parse(await readBody(req)) as { actorId?: unknown; metadata?: unknown };
        if (typeof body.actorId !== "string" || !ACTOR_ID.test(body.actorId)) return send(400, { code: "bad_request" });
        const websocketUrl = await findWebsocket(
          fetchImpl,
          config.controlPlaneUrl,
          config.projectId,
          config.adminKey,
          body.actorId,
          body.metadata ?? null,
        );
        return send(200, { websocketUrl });
      }
      return send(404, { code: "not_found" });
    } catch (error) {
      console.error(JSON.stringify({ event: "broker.error", message: error instanceof Error ? error.message : String(error) }));
      return send(502, { code: "upstream_error" });
    }
  });
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > MAX_BODY) throw new Error("request body too large");
    chunks.push(chunk as Buffer);
  }
  return Buffer.concat(chunks).toString("utf8") || "{}";
}

async function drain(req: IncomingMessage): Promise<void> {
  for await (const _ of req) {
    // discard
  }
}
