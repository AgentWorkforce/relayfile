# Existing Provider Connection Import Spec

**Status:** Proposed
**Date:** 2026-05-07
**Related:** `docs/provider-runtime-abstraction-spec.md`
**Owners:** Agent Relay Cloud, relayfile providers, relayfile CLI, relayfile SDK

## Problem

Users may already have provider connections in Nango, Composio, or Pipedream. Today hosted Agent Relay primarily exposes a new Nango connect-session flow. That is not enough for users who want to reuse existing connections.

Relayfile core can carry `connectionId` metadata, but the hosted product needs an explicit import flow that verifies the connection and links it to a workspace.

## Goal

Allow a user to attach an existing provider connection to a workspace without re-authorizing OAuth when the runtime can validate and use the connection.

## Non-Goals

- Do not import raw OAuth tokens into relayfile.
- Do not migrate connections across Nango/Composio/Pipedream accounts automatically.
- Do not promise that hosted Agent Relay can use a connection from a runtime account it cannot access.

## User Flows

### Import Existing Nango Connection

```bash
relayfile integration import notion \
  --workspace my-agent \
  --runtime nango \
  --connection-id conn_existing_123 \
  --provider-config-key notion-relay
```

Requirements:

- Agent Relay must have access to the Nango project containing `conn_existing_123`.
- Cloud validates the connection with Nango.
- Cloud stores `runtime=nango`, `connectionId`, and `providerConfigKey`.
- Cloud triggers initial sync.

### Import Existing Composio Connection

```bash
relayfile integration import slack \
  --workspace my-agent \
  --runtime composio \
  --connection-id ca_existing_123
```

Requirements:

- Agent Relay must have a Composio API key that can read `ca_existing_123`.
- Cloud validates with `getConnectedAccount` or equivalent health check.
- Cloud stores `runtime=composio` and `connectionId`.

### Import Existing Pipedream Connection

```bash
relayfile integration import google-drive \
  --workspace my-agent \
  --runtime pipedream \
  --connection-id apn_existing_123 \
  --external-user-id user_123
```

Requirements:

- Agent Relay must have Pipedream Connect project credentials that can read the account.
- Cloud validates the account.
- Cloud stores `runtime=pipedream`, `connectionId`, and `externalUserId`.

## API Contract

```http
POST /api/v1/workspaces/{workspaceId}/integrations/import
```

Request:

```ts
type ImportIntegrationRequest = {
  provider: string;
  runtime: "nango" | "composio" | "pipedream";
  connectionId: string;
  providerConfigKey?: string;
  externalUserId?: string;
  schemaId?: string;
  metadata?: Record<string, unknown>;
};
```

Response:

```ts
type ImportIntegrationResponse = {
  workspaceId: string;
  provider: string;
  runtime: "nango" | "composio" | "pipedream";
  connectionId: string;
  vfsRoot: string;
  ready: boolean;
  sync: {
    state: "queued" | "running" | "complete" | "failed";
    fileCount: number;
    lastError: string | null;
  };
};
```

## SDK Contract

```ts
await workspace.importIntegration({
  provider: "notion",
  runtime: "nango",
  connectionId: "conn_existing_123",
  providerConfigKey: "notion-relay",
});

await workspace.importIntegration({
  provider: "slack",
  runtime: "composio",
  connectionId: "ca_existing_123",
});

await workspace.importIntegration({
  provider: "google-drive",
  runtime: "pipedream",
  connectionId: "apn_existing_123",
  externalUserId: "user_123",
});
```

## Validation Rules

### Nango

- `connectionId` is required.
- `providerConfigKey` is required unless cloud can infer it from provider catalog.
- Cloud must verify the connection exists and is active.
- If the connection is in a different Nango project, return `connection_not_accessible`.

### Composio

- `connectionId` is required and maps to connected account ID.
- Cloud must verify the connected account exists and is healthy.
- If an integration/auth config ID is needed, it goes in metadata.

### Pipedream

- `connectionId` is required and maps to account ID.
- `externalUserId` is required for proxy paths that need it.
- Cloud must verify the account exists and is healthy.

## Error Codes

```ts
type ImportErrorCode =
  | "unknown_runtime"
  | "unknown_provider"
  | "connection_not_found"
  | "connection_not_accessible"
  | "connection_unhealthy"
  | "missing_provider_config_key"
  | "missing_external_user_id"
  | "schema_not_found"
  | "sync_enqueue_failed";
```

## Security Requirements

- Never log Authorization headers, OAuth tokens, refresh tokens, or runtime API keys.
- Treat `connectionId` as sensitive in diagnostics unless the user explicitly requests verbose local output.
- Import requires workspace write/admin access.
- Disconnect must call the runtime driver only when Agent Relay owns or can safely revoke that connection. For imported connections, default to unlink-only unless `--revoke` is explicit.

## Disconnect Semantics

```bash
relayfile integration disconnect notion --workspace my-agent
```

Default:

- Remove workspace link.
- Remove local mirror subtree.
- Do not revoke the upstream runtime connection.

Explicit revoke:

```bash
relayfile integration disconnect notion --workspace my-agent --revoke
```

Runtime driver may revoke/delete the upstream connection if supported.

## Phases

### Phase 1 - Import API

- Add endpoint.
- Add validation and error codes.
- Implement Nango import.

### Phase 2 - CLI And SDK

- Add `relayfile integration import`.
- Add `workspace.importIntegration`.
- Add tests for successful and failed import.

### Phase 3 - Composio And Pipedream

- Add runtime driver validation.
- Add metadata handling.
- Add docs and examples.

### Phase 4 - Sync And Disconnect Semantics

- Trigger initial sync after import.
- Add unlink vs revoke semantics.
- Ensure local mirror handles imported providers.

## Acceptance Criteria

- A user can import an existing Nango connection and see files after sync.
- A user can import an existing Composio connected account.
- A user can import an existing Pipedream account with external user ID.
- Import failures are actionable and typed.
- Disconnect does not accidentally revoke an external connection unless explicitly requested.

## Workflow Inputs

Recommended workflow slices:

1. `integration-import-api-nango`
2. `integration-import-cli-sdk`
3. `integration-import-composio`
4. `integration-import-pipedream`
5. `integration-import-sync-trigger`
6. `integration-disconnect-unlink-vs-revoke`
