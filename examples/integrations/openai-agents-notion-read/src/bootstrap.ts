import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { RelayFileClient, RelayfileSetup } from "@relayfile/sdk";

const CRED_FILE_PROBE_ORDER = [
  join(homedir(), ".agentworkforce", "relay", "cloud-auth.json"),
  join(homedir(), ".cloud", "credentials.json"),
  join(homedir(), ".relayfile", "cloud-credentials.json"),
] as const;

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
  cloudWorkspaceId: string | null;
  client: RelayFileClient;
  credSource: string;
}

function readCloudCreds(): CloudCreds {
  const envAccess = process.env.CLOUD_API_ACCESS_TOKEN;
  if (envAccess) {
    const envRefresh = process.env.CLOUD_API_REFRESH_TOKEN ?? "";
    // Without a refresh token the SDK can't roll the access token. Mark it
    // far-future so fromCloudTokens never enters the refresh path with an
    // empty refresh credential (which would 401).
    const expiresAt =
      process.env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT ??
      (envRefresh ? new Date(Date.now() + 60_000).toISOString() : FAR_FUTURE_ISO);
    return {
      accessToken: envAccess,
      refreshToken: envRefresh,
      accessTokenExpiresAt: expiresAt,
      apiUrl: process.env.CLOUD_API_URL ?? "https://agentrelay.com/cloud",
      source: "env",
    };
  }

  for (const path of CRED_FILE_PROBE_ORDER) {
    try {
      const raw = JSON.parse(readFileSync(path, "utf-8"));
      if (raw.accessToken && raw.refreshToken && raw.accessTokenExpiresAt) {
        if (Date.parse(raw.accessTokenExpiresAt) < Date.now()) {
          console.warn(
            `[relayfile-example] creds from ${path} expired at ${raw.accessTokenExpiresAt} — ` +
              `run \`agent-relay cloud login\` to refresh.`,
          );
        }
        return {
          accessToken: raw.accessToken,
          refreshToken: raw.refreshToken,
          accessTokenExpiresAt: raw.accessTokenExpiresAt,
          apiUrl: raw.apiUrl ?? "https://agentrelay.com/cloud",
          source: path,
        };
      }
    } catch {
      continue;
    }
  }

  throw new Error(
    "No Cloud credentials found. Set CLOUD_API_ACCESS_TOKEN (and optionally " +
      "CLOUD_API_REFRESH_TOKEN), or pre-mint RELAYFILE_TOKEN + " +
      "RELAYFILE_WORKSPACE_ID, or run `agent-relay cloud login`. Probed paths: " +
      CRED_FILE_PROBE_ORDER.join(", "),
  );
}

export async function connectWorkspace(opts: {
  cloudWorkspaceId?: string;
  scopes?: string[];
} = {}): Promise<RelayfileWorkspace> {
  // Fast-path: caller pre-minted a relayfile data-plane token. No cloud hop,
  // no /join, no refresh — just construct the client. Truly CI-clean.
  const preToken = process.env.RELAYFILE_TOKEN;
  const preWorkspace = process.env.RELAYFILE_WORKSPACE_ID;
  if (preToken && preWorkspace) {
    return {
      workspaceId: preWorkspace,
      cloudWorkspaceId: null,
      client: new RelayFileClient({
        token: preToken,
        baseUrl: process.env.RELAYFILE_BASE_URL ?? undefined,
      }),
      credSource: "env:RELAYFILE_TOKEN",
    };
  }

  const creds = readCloudCreds();
  const cloudWorkspaceId =
    opts.cloudWorkspaceId ?? process.env.CLOUD_WORKSPACE_ID;

  if (!cloudWorkspaceId) {
    throw new Error(
      "CLOUD_WORKSPACE_ID is required (env var or opts.cloudWorkspaceId). " +
        "List candidates with: curl -H 'Authorization: Bearer <token>' " +
        "$CLOUD_API_URL/api/v1/workspaces",
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

  // Iron rule: the workspaceId we send to /join may be the cloud app-UUID,
  // but every downstream data-plane call uses workspace.workspaceId (the
  // rw_ shard id /join returns). That ID split was the root of relayfile#306.
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
