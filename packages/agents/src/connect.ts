import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import {
  RelayfileSetup,
  type FileReadResponse,
  type FilesystemEvent,
  type RelayFileClient,
  type Subscription,
  type WebSocketConnection,
} from "@relayfile/sdk";

import { createWriteback, type WritebackApi } from "./writeback.js";

// Single canonical credential file — written by `agent-relay cloud login`.
const CLOUD_AUTH_FILE = join(homedir(), ".agentworkforce", "relay", "cloud-auth.json");

const FAR_FUTURE_ISO = new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString();

interface CloudCreds {
  accessToken: string;
  refreshToken: string;
  accessTokenExpiresAt: string;
  apiUrl: string;
  source: string;
}

export interface ConnectOptions {
  /** Cloud app-UUID. Falls back to `CLOUD_WORKSPACE_ID` env var. */
  workspaceId?: string;
  /** Path-scoped scopes, e.g. `["relayfile:fs:read:/notion/**"]`. Defaults to read+write on everything. */
  scopes?: string[];
  /** Agent name advertised to the workspace. Defaults to `relayfile-agents`. */
  agentName?: string;
}

export interface RelayfileAgents {
  /** Raw SDK client — escape hatch for anything the high-level API doesn't cover. */
  client: RelayFileClient;
  /** The `rw_` shard id returned by `/join`; use for any raw SDK calls. */
  workspaceId: string;
  /** The cloud app-UUID you passed in. */
  cloudWorkspaceId: string;
  /** Where credentials were resolved from (env, cloud-auth.json, ...). */
  credSource: string;
  /** Provider-agnostic writeback lifecycle (create/update/delete with op-poll). */
  writeback: WritebackApi;
  /** Convenience: read a single file by path. */
  read(path: string): Promise<FileReadResponse>;
  /**
   * React to provider webhooks in real time. The agent registers a handler
   * on one or more workspace path globs (`**` supported as a trailing
   * segment per the SDK convention). When a Relayfile file event matches —
   * a webhook ingested by Cloud, a writeback by another agent, anything
   * that mutates the path — the handler fires.
   *
   * This is the structural difference vs. per-provider MCPs (which are
   * pull-only). MCPs can't push; Relayfile turns every provider event into
   * a workspace event multiple agents can subscribe to.
   *
   * Auto-reconnect with token refresh and exponential backoff: when the
   * WebSocket drops (token TTL, gateway flap, sustained outage), the
   * subscription re-resolves a fresh token via the cloud token provider
   * and reconnects, with backoff `baseDelayMs → maxDelayMs` (doubling on
   * each failure, reset to base after ~10s of stable connection). Long-
   * lived reactive agents survive token expiry without silent failure
   * and without hammering the gateway on sustained outage.
   *
   * @returns `Subscription` with `unsubscribe()`.
   */
  onEvent(
    globs: string[],
    handler: (event: FilesystemEvent) => void | Promise<void>,
    opts?: OnEventOptions,
  ): Subscription;
}

export interface OnEventOptions {
  /** Called when a handler invocation rejects. Defaults to `console.error`. */
  onError?: (err: unknown, event: FilesystemEvent) => void;
  /** Base backoff in ms (doubles each failure). Default 1000. */
  baseDelayMs?: number;
  /** Cap backoff at this many ms. Default 30000. */
  maxDelayMs?: number;
  /**
   * If a connection stays open this long, reset the backoff to the base
   * (treat the connection as healthy). Default 10000.
   */
  stableThresholdMs?: number;
}

function readCloudCreds(): CloudCreds {
  // 1) Env overrides — CI / non-interactive.
  const envAccess = process.env.CLOUD_API_ACCESS_TOKEN;
  if (envAccess) {
    const refreshToken = process.env.CLOUD_API_REFRESH_TOKEN ?? "";
    return {
      accessToken: envAccess,
      refreshToken,
      // Without a refresh token, pin expiry far-future so the SDK never
      // attempts to roll the access token with an empty refresh credential.
      accessTokenExpiresAt:
        process.env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT ??
        (refreshToken ? new Date(Date.now() + 60_000).toISOString() : FAR_FUTURE_ISO),
      apiUrl: process.env.CLOUD_API_URL ?? "https://agentrelay.com/cloud",
      source: "env",
    };
  }

  // 2) Credentials from `agent-relay cloud login`.
  try {
    const raw = JSON.parse(readFileSync(CLOUD_AUTH_FILE, "utf-8"));
    if (raw.accessToken && raw.refreshToken && raw.accessTokenExpiresAt) {
      if (Date.parse(raw.accessTokenExpiresAt) < Date.now()) {
        console.warn(
          `⚠️  Cloud creds expired at ${raw.accessTokenExpiresAt}. ` +
            "Run `agent-relay cloud login`.",
        );
      }
      return {
        accessToken: raw.accessToken,
        refreshToken: raw.refreshToken,
        accessTokenExpiresAt: raw.accessTokenExpiresAt,
        apiUrl: raw.apiUrl ?? "https://agentrelay.com/cloud",
        source: CLOUD_AUTH_FILE,
      };
    }
  } catch {
    // fall through
  }

  throw new Error(
    "No Cloud credentials. Run `agent-relay cloud login`, or set " +
      "CLOUD_API_ACCESS_TOKEN (optionally CLOUD_API_REFRESH_TOKEN, CLOUD_API_URL).",
  );
}

export async function connect(opts: ConnectOptions = {}): Promise<RelayfileAgents> {
  const creds = readCloudCreds();
  const cloudWorkspaceId = opts.workspaceId ?? process.env.CLOUD_WORKSPACE_ID;

  if (!cloudWorkspaceId) {
    throw new Error(
      "workspaceId required: pass it to connect() or set CLOUD_WORKSPACE_ID. " +
        "Find your app-UUID at https://agentrelay.com/cloud.",
    );
  }

  const setup = RelayfileSetup.fromCloudTokens(
    {
      accessToken: creds.accessToken,
      refreshToken: creds.refreshToken,
      accessTokenExpiresAt: creds.accessTokenExpiresAt,
    },
    { cloudApiUrl: creds.apiUrl },
  );

  // The app-UUID may go into /join, but every downstream data-plane call uses
  // workspace.workspaceId (the rw_ shard id /join returns). That ID split was
  // the root of relayfile#306.
  const handle = await setup.joinWorkspace(cloudWorkspaceId, {
    agentName: opts.agentName ?? "relayfile-agents",
    scopes: opts.scopes,
  });

  const client = handle.client();
  const workspaceId = handle.workspaceId;

  return {
    client,
    workspaceId,
    cloudWorkspaceId,
    credSource: creds.source,
    writeback: createWriteback(client, workspaceId),
    read: (path: string) => client.readFile(workspaceId, path),
    onEvent: (globs, handler, opts = {}) => {
      const patterns = globs.map(normalizeGlob);
      const baseDelay = opts.baseDelayMs ?? 1000;
      const maxDelay = opts.maxDelayMs ?? 30_000;
      const stableThreshold = opts.stableThresholdMs ?? 10_000;
      const reportError =
        opts.onError ??
        ((err: unknown, event: FilesystemEvent) =>
          console.error("[relayfile/agents] onEvent handler error:", err, "@", event.path));

      let conn: WebSocketConnection | null = null;
      let unsubscribed = false;
      let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
      let nextDelay = baseDelay;
      let connectedAt = 0;

      const dispatch = (event: FilesystemEvent) => {
        if (!patterns.some((p) => p.test(event.path))) return;
        Promise.resolve(handler(event)).catch((err) => reportError(err, event));
      };

      const scheduleReconnect = () => {
        if (unsubscribed) return;
        // If we held a stable connection long enough, reset the backoff so the
        // next reconnect-after-flap is fast. Sustained flap keeps doubling.
        if (connectedAt && Date.now() - connectedAt >= stableThreshold) {
          nextDelay = baseDelay;
        }
        const delay = Math.min(nextDelay, maxDelay);
        nextDelay = Math.min(nextDelay * 2, maxDelay);
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          void connectOnce();
        }, delay);
      };

      const connectOnce = async () => {
        if (unsubscribed) return;
        try {
          const token = await client.getToken();
          conn = client.connectWebSocket(workspaceId, {
            token,
            paths: globs,
            onEvent: dispatch,
          });
          connectedAt = Date.now();
          const offClose = conn.on("close", () => {
            offClose();
            scheduleReconnect();
          });
        } catch (err) {
          console.error(
            `[relayfile/agents] onEvent connect failed; retrying in ${nextDelay}ms:`,
            err,
          );
          scheduleReconnect();
        }
      };

      void connectOnce();

      return {
        unsubscribe: async () => {
          unsubscribed = true;
          if (reconnectTimer) clearTimeout(reconnectTimer);
          conn?.close();
        },
      };
    },
  };
}

/**
 * Convert a `/foo/bar/**` glob into a RegExp. Supports `**` only as a
 * trailing segment per the SDK's subscribe contract — anything else is
 * literal text. The match is on full paths (anchored both ends).
 */
function normalizeGlob(glob: string): RegExp {
  const trimmed = glob.replace(/\/+$/, "");
  if (trimmed.endsWith("/**")) {
    const prefix = trimmed.slice(0, -3);
    return new RegExp(`^${escapeRegex(prefix)}(/|$)`);
  }
  return new RegExp(`^${escapeRegex(trimmed)}$`);
}

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
