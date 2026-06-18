import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { RelayfileSetup, type RelayFileClient } from "@relayfile/sdk";

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
  };
}
