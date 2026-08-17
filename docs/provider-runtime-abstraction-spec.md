# Provider Runtime Abstraction Spec

**Status:** Proposed
**Date:** 2026-05-07
**Related:** AgentWorkforce/cloud#456, AgentWorkforce/cloud#457
**Owners:** Agent Relay Cloud, relayfile providers, relayfile CLI, relayfile SDK

## Problem

Hosted Agent Relay currently treats provider-backed relayfile integrations as a Nango-shaped system. The public setup path creates a Nango connect session, stores `connectionId` and `providerConfigKey`, and runs Nango record sync into relayfile.

That is correct for the current cloud-mount path, but it is too narrow for the product. Users should be able to bring provider connections from Nango, Composio, Pipedream, or future runtimes while keeping the same agent-facing filesystem contract.

## Goal

Make Nango one runtime driver behind a provider-runtime interface, not the integration product boundary.

After this work, hosted Agent Relay should be able to attach a workspace integration with:

```json
{
  "workspaceId": "rw_12345678",
  "provider": "notion",
  "runtime": "nango",
  "connectionId": "conn_existing_123",
  "providerConfigKey": "notion-relay"
}
```

or:

```json
{
  "workspaceId": "rw_12345678",
  "provider": "slack",
  "runtime": "composio",
  "connectionId": "ca_existing_123"
}
```

or:

```json
{
  "workspaceId": "rw_12345678",
  "provider": "google-drive",
  "runtime": "pipedream",
  "connectionId": "apn_existing_123",
  "externalUserId": "user_123"
}
```

## Non-Goals

- Do not replace Nango for the existing `notion-relay`, `github-relay`, `linear-relay`, and `slack-relay` paths.
- Do not make relayfile store OAuth tokens.
- Do not require every runtime to support every provider.
- Do not block the Nango-specific JIT work in cloud#457.

## Data Model

`workspace_integrations` needs to become runtime-aware. The exact migration can be additive:

```ts
type WorkspaceIntegrationRuntime = "nango" | "composio" | "pipedream";

type WorkspaceIntegrationRecord = {
  workspaceId: string;
  provider: string;
  runtime: WorkspaceIntegrationRuntime;
  connectionId: string;
  providerConfigKey?: string | null;
  externalUserId?: string | null;
  installationId?: string | null;
  schemaId?: string | null;
  metadata: Record<string, unknown>;
};
```

Recommended DB additions:

```sql
ALTER TABLE workspace_integrations
  ADD COLUMN runtime text NOT NULL DEFAULT 'nango',
  ADD COLUMN external_user_id text,
  ADD COLUMN schema_id text;
```

Keep `provider_config_key` for Nango and for any runtime that has an equivalent auth config key. Runtime-specific values that do not deserve top-level columns should stay in `metadata_json`.

## Runtime Driver Contract

Cloud should route all provider operations through a driver interface:

```ts
export type RuntimeKind = "nango" | "composio" | "pipedream";

export type RuntimeConnection = {
  workspaceId: string;
  provider: string;
  runtime: RuntimeKind;
  connectionId: string;
  providerConfigKey?: string | null;
  externalUserId?: string | null;
  metadata?: Record<string, unknown>;
};

export type ConnectSessionRequest = {
  workspaceId: string;
  provider: string;
  redirectUrl?: string;
};

export type ConnectSessionResponse = {
  connectUrl?: string;
  connectionId?: string;
  expiresAt?: string;
};

export type RuntimeProxyRequest = {
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  endpoint: string;
  headers?: Record<string, string>;
  query?: Record<string, string>;
  body?: unknown;
};

export interface ProviderRuntimeDriver {
  kind: RuntimeKind;

  createConnectSession(
    request: ConnectSessionRequest,
  ): Promise<ConnectSessionResponse>;

  importConnection(connection: RuntimeConnection): Promise<void>;

  healthCheck(connection: RuntimeConnection): Promise<boolean>;

  proxy(
    connection: RuntimeConnection,
    request: RuntimeProxyRequest,
  ): Promise<unknown>;

  disconnect(connection: RuntimeConnection): Promise<void>;
}
```

Nango's existing connect-session logic becomes `NangoRuntimeDriver`. Composio and Pipedream can start with `importConnection`, `healthCheck`, and `proxy`; hosted OAuth creation can follow.

## API Contract

### Create Hosted Connect Session

```http
POST /api/v1/workspaces/{workspaceId}/integrations/connect-session
```

Request:

```json
{
  "provider": "notion",
  "runtime": "nango"
}
```

Response:

```json
{
  "runtime": "nango",
  "provider": "notion",
  "connectLink": "https://connect.nango.dev/...",
  "connectionId": "conn_123",
  "expiresAt": "2026-05-07T14:00:00Z"
}
```

Backward compatibility: existing clients sending `allowedIntegrations` should still route to Nango.

### Import Existing Connection

```http
POST /api/v1/workspaces/{workspaceId}/integrations/import
```

Request:

```json
{
  "provider": "slack",
  "runtime": "composio",
  "connectionId": "ca_existing_123",
  "metadata": {
    "integrationId": "auth_config_123"
  }
}
```

Response:

```json
{
  "workspaceId": "rw_12345678",
  "provider": "slack",
  "runtime": "composio",
  "connectionId": "ca_existing_123",
  "ready": true
}
```

### List Integrations

The existing list endpoint should return runtime metadata:

```json
{
  "integrations": [
    {
      "provider": "notion",
      "runtime": "nango",
      "connectionId": "conn_123",
      "providerConfigKey": "notion-relay",
      "vfsRoot": "/notion",
      "ready": true,
      "sync": {
        "state": "complete",
        "fileCount": 214,
        "lastSyncedAt": "2026-05-07T13:00:00Z"
      }
    }
  ]
}
```

## CLI Contract

Existing setup remains:

```bash
relayfile setup --provider notion
```

New runtime-aware forms:

```bash
relayfile integration connect notion --runtime nango

relayfile integration import slack \
  --runtime composio \
  --connection-id ca_existing_123

relayfile integration import google-drive \
  --runtime pipedream \
  --connection-id apn_existing_123 \
  --external-user-id user_123
```

## Phases

### Phase 1 - Data Model And Driver Boundary

- Add `runtime`, `external_user_id`, and `schema_id` fields.
- Add driver interface and `NangoRuntimeDriver`.
- Route existing Nango calls through the driver.
- Preserve existing API and CLI behavior.

Acceptance:

- Existing `relayfile setup --provider notion` still works.
- Existing Nango rows default to `runtime='nango'`.
- Tests prove no direct Nango calls remain outside the Nango driver except low-level driver implementation.

### Phase 2 - Import Existing Connections

- Add `POST /integrations/import`.
- Add `relayfile integration import`.
- Implement import/health-check for Nango, Composio, and Pipedream.

Acceptance:

- Nango import validates `connectionId + providerConfigKey`.
- Composio import validates connected account ID.
- Pipedream import validates account ID and requires external user ID when proxy calls need it.

### Phase 3 - Runtime-Aware Sync And Writeback

- Add runtime-aware sync dispatch.
- Extend writeback routing to use runtime metadata.
- Keep relayfile core runtime-agnostic.

Acceptance:

- A Nango-backed Notion workspace and a Composio-backed Slack workspace can coexist.
- Agent-facing VFS behavior is identical: files are under provider roots and writes enter the writeback queue.

## Workflow Inputs

Recommended workflow slices:

1. `provider-runtime-driver-boundary`
2. `workspace-integrations-runtime-migration`
3. `integration-import-api`
4. `integration-import-cli`
5. `runtime-aware-sync-and-writeback`
6. `nango-regression-suite`
