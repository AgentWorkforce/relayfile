# Agent Workspace Discovery And Readiness Spec

**Status:** Proposed
**Date:** 2026-05-07
**Related:** AgentWorkforce/relayfile#79
**Owners:** relayfile SDK, relayfile CLI, Agent Relay Cloud, agent skills

## Problem

Issue #79 documents two silent-failure classes in the agent workspace SDK:

1. Agents cannot reliably discover which provider roots are mounted.
2. `waitForConnection()` can report OAuth readiness before files are synced, so agents can read an empty mount without an actionable signal.

The problem becomes more important with JIT integrations because provider roots and schemas will no longer be limited to a small hardcoded set like `/notion` and `/github`.

## Goal

Give agents a first-class way to answer:

- Which integrations are connected?
- Which VFS roots are available?
- Are files synced enough to read?
- Which schemas and writeback paths apply?
- What should the agent do if the mount is empty or stale?

## Non-Goals

- Do not require initial sync to be instantaneous.
- Do not hide polling or eventual consistency.
- Do not require kernel FUSE behavior from the synced mirror.

## Cloud API Contract

### List Integrations

```http
GET /api/v1/workspaces/{workspaceId}/integrations
```

Response:

```ts
type WorkspaceIntegrationSummary = {
  provider: string;
  displayName: string;
  runtime: "nango" | "composio" | "pipedream";
  vfsRoot: string;
  connectionId: string | null;
  providerConfigKey?: string | null;
  mountedPath: string;
  ready: boolean;
  auth: {
    connected: boolean;
    connectedAt: string | null;
    lastAuthAt: string | null;
  };
  sync: {
    state: "unknown" | "queued" | "running" | "complete" | "failed";
    fileCount: number;
    lastSyncedAt: string | null;
    lagSeconds: number | null;
    lastError: string | null;
  };
  schemas: Array<{
    pathTemplate: string;
    direction: "read" | "write";
    schemaId: string;
  }>;
};
```

### Integration Status

```http
GET /api/v1/workspaces/{workspaceId}/integrations/{provider}/status
```

This endpoint should return the same fields for a single provider, including `vfsRoot`, `mountedPath`, `sync.fileCount`, and `sync.state`.

## SDK Contract

Add discovery helpers to `WorkspaceHandle`:

```ts
type WorkspaceIntegration = {
  provider: string;
  runtime: "nango" | "composio" | "pipedream";
  vfsRoot: string;
  mountedPath: string;
  ready: boolean;
  sync: {
    state: "unknown" | "queued" | "running" | "complete" | "failed";
    fileCount: number;
    lagSeconds: number | null;
    lastSyncedAt: string | null;
    lastError: string | null;
  };
  schemas: Array<{
    pathTemplate: string;
    direction: "read" | "write";
    schemaId: string;
  }>;
};

class WorkspaceHandle {
  listIntegrations(): Promise<WorkspaceIntegration[]>;

  listMountedPaths(): Promise<Array<{
    provider: string;
    vfsRoot: string;
    mountedPath: string;
    fileCount: number;
    syncState: WorkspaceIntegration["sync"]["state"];
  }>>;

  waitForIntegrationReady(
    provider: string,
    options?: {
      minFiles?: number;
      requireSyncComplete?: boolean;
      timeoutMs?: number;
      pollIntervalMs?: number;
      onProgress?: (status: WorkspaceIntegration) => void;
      signal?: AbortSignal;
    },
  ): Promise<WorkspaceIntegration>;

  waitForNotion(
    options?: Omit<Parameters<WorkspaceHandle["waitForIntegrationReady"]>[1], never>,
  ): Promise<WorkspaceIntegration>;
}
```

Backward compatibility:

- `waitForConnection(provider)` may continue to mean OAuth readiness.
- New agent code should prefer `waitForIntegrationReady(provider, { requireSyncComplete: true })`.
- `waitForNotion()` should either return the status object or have a `mode` option. The safest path is to return status while staying promise-compatible for callers that ignore the return value.

## CLI Contract

```bash
relayfile integration list --workspace my-agent --json
relayfile mounted-paths --workspace my-agent --json
relayfile wait notion --workspace my-agent --sync-complete --min-files 1 --timeout 5m
```

Human output:

```text
provider  root      sync      files  lag
notion    /notion   complete  214    4s
github    /github   running   42     18s
```

## Mount State Contract

The local `.relay/state.json` file should mirror cloud discovery fields:

```json
{
  "workspaceId": "rw_12345678",
  "localDir": "/Users/me/relayfile-mount",
  "providers": [
    {
      "provider": "notion",
      "vfsRoot": "/notion",
      "mountedPath": "/Users/me/relayfile-mount/notion",
      "status": "ready",
      "syncState": "complete",
      "fileCount": 214,
      "lagSeconds": 4
    }
  ]
}
```

Agent skills should read this file before calling provider APIs or assuming root names.

## Empty Mount Handling

Agents need a deterministic signal for empty mounts:

```ts
const notion = await workspace.waitForIntegrationReady("notion", {
  requireSyncComplete: true,
  minFiles: 1,
  timeoutMs: 5 * 60_000,
  onProgress: (status) => {
    console.log(`Notion sync ${status.sync.state}: ${status.sync.fileCount} files`);
  },
});
```

Failure should be typed:

```ts
class IntegrationSyncTimeoutError extends Error {
  provider: string;
  lastStatus: WorkspaceIntegration;
}

class IntegrationSyncFailedError extends Error {
  provider: string;
  lastError: string;
  lastStatus: WorkspaceIntegration;
}
```

## Phases

### Phase 1 - Cloud Discovery Completeness

- Ensure list/status endpoints return `vfsRoot`, `mountedPath`, `runtime`, `sync.state`, `sync.fileCount`, and schemas.
- Cover built-in integrations.

### Phase 2 - SDK Discovery Helpers

- Add `listIntegrations`.
- Add `listMountedPaths`.
- Add `waitForIntegrationReady`.
- Keep `waitForConnection` backward-compatible.

### Phase 3 - CLI And Mount State

- Add `relayfile mounted-paths`.
- Add `relayfile wait`.
- Mirror discovery fields into `.relay/state.json`.

### Phase 4 - Agent Skill Update

- Update relayfile agent skill to discover mounts instead of assuming provider roots.
- Gate provider reads on `sync.state` and `fileCount`.

## Acceptance Criteria

- An agent can resume in a workspace and discover every connected provider without prior setup context.
- An agent can wait until a provider has at least one synced file.
- Empty mount after OAuth readiness no longer looks like success.
- JIT provider roots appear through the same discovery surface.
- Issue #79 can be closed only after SDK, CLI, and skill behavior are covered.

## Workflow Inputs

Recommended workflow slices:

1. `cloud-integration-discovery-fields`
2. `sdk-list-integrations-and-mounted-paths`
3. `sdk-wait-for-integration-ready`
4. `cli-mounted-paths-and-wait`
5. `mount-state-provider-discovery`
6. `agent-skill-discovery-update`
