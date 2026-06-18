import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { RelayfileSetup, type RelayFileClient } from "@relayfile/sdk";

const CRED_FILE_PROBE_ORDER = [
  join(homedir(), ".agentworkforce", "relay", "cloud-auth.json"),
  join(homedir(), ".cloud", "credentials.json"),
  join(homedir(), ".relayfile", "cloud-credentials.json"),
] as const;

interface CloudCreds {
  accessToken: string;
  refreshToken: string;
  accessTokenExpiresAt: string;
  apiUrl: string;
  source: string;
}

function readCloudCreds(): CloudCreds {
  const envAccess = process.env.CLOUD_API_ACCESS_TOKEN;
  if (envAccess) {
    return {
      accessToken: envAccess,
      refreshToken: process.env.CLOUD_API_REFRESH_TOKEN ?? "",
      accessTokenExpiresAt:
        process.env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT ?? new Date(Date.now() + 60_000).toISOString(),
      apiUrl: process.env.CLOUD_API_URL ?? "https://agentrelay.com/cloud",
      source: "env",
    };
  }

  for (const path of CRED_FILE_PROBE_ORDER) {
    try {
      const raw = JSON.parse(readFileSync(path, "utf-8"));
      if (raw.accessToken && raw.refreshToken && raw.accessTokenExpiresAt) {
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
    "No Cloud credentials found. Set CLOUD_API_ACCESS_TOKEN, or run `agent-relay cloud login`, " +
      "or ensure one of: " +
      CRED_FILE_PROBE_ORDER.join(", "),
  );
}

export interface RelayfileWorkspace {
  workspaceId: string;
  cloudWorkspaceId: string;
  client: RelayFileClient;
  credSource: string;
}

export async function connectWorkspace(opts: {
  cloudWorkspaceId?: string;
  scopes?: string[];
} = {}): Promise<RelayfileWorkspace> {
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
