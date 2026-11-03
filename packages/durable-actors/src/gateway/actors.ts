import { createActorTransport, type ActorRpcTransport } from "durable-actors/backend";
import type { ActorHttpRequest, ActorHttpResponse, CallerContext } from "../runtime/types.js";

export const WORKSPACE_ACTOR = "Workspace";

/** The actor calls the gateway makes; tests substitute an in-process runtime. */
export interface WorkspaceActors {
  handle(workspaceId: string, request: ActorHttpRequest, caller: CallerContext): Promise<ActorHttpResponse>;
  /** A WebSocket URL for one connection to this workspace's actor, carrying `metadata`. */
  socketGrant(workspaceId: string, metadata: unknown): Promise<string>;
  ready(): Promise<void>;
}

export interface ActorSessionGrant {
  projectId: string;
  controlPlaneUrl: string;
  token: string;
  expiresAtMs: number;
}

/** Session credentials and socket grants come from somewhere that holds the admin key. */
export interface CredentialSource {
  session(): Promise<ActorSessionGrant>;
  socketGrant(actorId: string, metadata: unknown): Promise<string>;
}

/**
 * Admin-key source: only for a durable-actors dev sidecar in the same pod
 * (RELAYFILE_ACTORS_ALLOW_ADMIN_KEY=true). The gateway never needs this in
 * production; the session broker holds the key instead.
 */
export class AdminKeySource implements CredentialSource {
  constructor(
    private readonly controlPlaneUrl: string,
    private readonly projectId: string,
    private readonly apiKey: string,
    private readonly fetchImpl: typeof fetch = fetch,
  ) {}
  async session(): Promise<ActorSessionGrant> {
    return issueSession(this.fetchImpl, this.controlPlaneUrl, this.projectId, this.apiKey, "relayfile-gateway");
  }
  async socketGrant(actorId: string, metadata: unknown): Promise<string> {
    return findWebsocket(this.fetchImpl, this.controlPlaneUrl, this.projectId, this.apiKey, actorId, metadata);
  }
}

/** Broker source: the gateway holds only a broker token. */
export class BrokerSource implements CredentialSource {
  constructor(
    private readonly brokerUrl: string,
    private readonly brokerToken: string,
    private readonly fetchImpl: typeof fetch = fetch,
  ) {}
  async session(): Promise<ActorSessionGrant> {
    const response = await this.fetchImpl(`${this.brokerUrl.replace(/\/$/, "")}/v1/session`, {
      method: "POST",
      headers: { authorization: `Bearer ${this.brokerToken}` },
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error(`session broker returned HTTP ${response.status}`);
    }
    return (await response.json()) as ActorSessionGrant;
  }
  async socketGrant(actorId: string, metadata: unknown): Promise<string> {
    const response = await this.fetchImpl(`${this.brokerUrl.replace(/\/$/, "")}/v1/socket-grant`, {
      method: "POST",
      headers: { authorization: `Bearer ${this.brokerToken}`, "content-type": "application/json" },
      body: JSON.stringify({ actorId, metadata }),
      signal: AbortSignal.timeout(30_000),
    });
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error(`session broker socket grant returned HTTP ${response.status}`);
    }
    const body = (await response.json()) as { websocketUrl?: string };
    if (!body.websocketUrl) throw new Error("session broker returned no websocketUrl");
    return body.websocketUrl;
  }
}

/**
 * Invokes `Workspace` actors with short-lived project sessions, renewing them
 * before they expire. (The SDK's ActorSessionTransport refuses non-HTTPS
 * control planes outside localhost, but in-cluster the control plane is
 * plain HTTP on a ClusterIP, so this keeps its own small cache.)
 */
export class SessionWorkspaceActors implements WorkspaceActors {
  private current: { transport: ActorRpcTransport; expiresAtMs: number } | undefined;
  private pending: Promise<ActorRpcTransport> | undefined;
  constructor(
    private readonly source: CredentialSource,
    private readonly now: () => number = Date.now,
  ) {}

  async handle(workspaceId: string, request: ActorHttpRequest, caller: CallerContext): Promise<ActorHttpResponse> {
    const transport = await this.transport();
    return (await transport.invoke(WORKSPACE_ACTOR, workspaceId, "handle", [request, caller])) as ActorHttpResponse;
  }

  socketGrant(workspaceId: string, metadata: unknown): Promise<string> {
    return this.source.socketGrant(workspaceId, metadata);
  }

  async ready(): Promise<void> {
    await this.transport();
  }

  private async transport(): Promise<ActorRpcTransport> {
    if (this.current && this.current.expiresAtMs - this.now() > 15_000) return this.current.transport;
    this.pending ??= (async () => {
      try {
        const grant = await this.source.session();
        const transport = createActorTransport({
          projectId: grant.projectId,
          controlPlaneUrl: grant.controlPlaneUrl,
          apiKey: grant.token,
        });
        this.current = { transport, expiresAtMs: grant.expiresAtMs };
        return transport;
      } finally {
        this.pending = undefined;
      }
    })();
    return this.pending;
  }
}

export async function issueSession(
  fetchImpl: typeof fetch,
  controlPlaneUrl: string,
  projectId: string,
  apiKey: string,
  subject: string,
  lifetimeMs = 55_000,
): Promise<ActorSessionGrant> {
  const origin = controlPlaneUrl.replace(/\/$/, "");
  const response = await fetchImpl(`${origin}/v1/projects/${encodeURIComponent(projectId)}/sessions`, {
    method: "POST",
    headers: { authorization: `Bearer ${apiKey}`, "content-type": "application/json" },
    body: JSON.stringify({ subject, expiresAtMs: Date.now() + lifetimeMs }),
    redirect: "manual",
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(`control plane session request returned HTTP ${response.status}`);
  }
  const body = (await response.json()) as { token: string; expiresAtMs: number };
  return { projectId, controlPlaneUrl: origin, token: body.token, expiresAtMs: body.expiresAtMs };
}

export async function findWebsocket(
  fetchImpl: typeof fetch,
  controlPlaneUrl: string,
  projectId: string,
  apiKey: string,
  actorId: string,
  metadata: unknown,
): Promise<string> {
  const origin = controlPlaneUrl.replace(/\/$/, "");
  const path = `/v1/projects/${encodeURIComponent(projectId)}/actors/${WORKSPACE_ACTOR}/${encodeURIComponent(actorId)}/find-websocket`;
  const response = await fetchImpl(`${origin}${path}`, {
    method: "POST",
    headers: { authorization: `Bearer ${apiKey}`, "content-type": "application/json" },
    body: JSON.stringify({ metadata }),
    redirect: "manual",
    signal: AbortSignal.timeout(180_000),
  });
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(`control plane socket grant returned HTTP ${response.status}`);
  }
  const body = (await response.json()) as { websocketUrl?: string };
  if (!body.websocketUrl) throw new Error("control plane returned no websocketUrl");
  return body.websocketUrl;
}
