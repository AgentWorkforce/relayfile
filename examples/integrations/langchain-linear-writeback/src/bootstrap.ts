// Shared Relayfile bootstrap template. Byte-identical across all examples
// under examples/integrations/ — copy this file directly if you're using one
// of these as a starting point for your own project.

import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { RelayfileSetup, type RelayFileClient } from "@relayfile/sdk";

// Only file we read — written by `agent-relay cloud login`.
const CLOUD_AUTH_FILE = join(homedir(), ".agentworkforce", "relay", "cloud-auth.json");

const FAR_FUTURE_ISO = new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString();

interface CloudCreds {
  accessToken: string;
  refreshToken: string;
  accessTokenExpiresAt: string;
  apiUrl: string;
  source: string;
}

export interface RelayfileWorkspace {
  workspaceId: string;
  cloudWorkspaceId: string;
  client: RelayFileClient;
  credSource: string;
}

function readCloudCreds(): CloudCreds {
  // 1) Env overrides — CI / non-interactive.
  const envAccess = process.env.CLOUD_API_ACCESS_TOKEN;
  if (envAccess) {
    const refreshToken = process.env.CLOUD_API_REFRESH_TOKEN ?? "";
    // Without a refresh token the SDK cannot roll the access token. Pin expiry
    // far-future so it never attempts a refresh with an empty credential.
    return {
      accessToken: envAccess,
      refreshToken,
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
    // fall through to error below
  }

  throw new Error(
    "No Cloud credentials. Run `agent-relay cloud login`, or set " +
      "CLOUD_API_ACCESS_TOKEN (optionally CLOUD_API_REFRESH_TOKEN, CLOUD_API_URL).",
  );
}

export async function connectWorkspace(opts: {
  cloudWorkspaceId?: string;
  scopes?: string[];
} = {}): Promise<RelayfileWorkspace> {
  const creds = readCloudCreds();
  const cloudWorkspaceId = opts.cloudWorkspaceId ?? process.env.CLOUD_WORKSPACE_ID;

  if (!cloudWorkspaceId) {
    throw new Error(
      "CLOUD_WORKSPACE_ID env var (your app-UUID) is required. Find it on " +
        "https://agentrelay.com/cloud.",
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

  // Iron rule: send your app-UUID to /join, but every downstream data-plane
  // call uses workspace.workspaceId (the `rw_` shard id /join returns). That
  // ID split was the root of relayfile#306.
  const handle = await setup.joinWorkspace(cloudWorkspaceId, {
    agentName: "integration-verifier-example",
    scopes: opts.scopes,
  });

  return {
    workspaceId: handle.workspaceId,
    cloudWorkspaceId,
    client: handle.client(),
    credSource: creds.source,
  };
}
