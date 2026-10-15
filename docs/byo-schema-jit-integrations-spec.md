# BYO Schema And JIT Integrations Spec

**Status:** Proposed
**Date:** 2026-05-07
**Related:** AgentWorkforce/cloud#457, `docs/canonical-file-schema-ownership-boundary.md`
**Owners:** Agent Relay Cloud, relayfile core, relayfile adapters, relayfile SDK

## Problem

Cloud#457 defines a Nango-specific programmatic JIT sync path:

```bash
relayfile sync push my-sync.ts --provider-config-key acme-tickets
```

That is a good v2 for Nango syncs, but the product needs a more general shape: users should be able to bring their own schema and map provider records into relayfile files, regardless of whether the runtime is Nango, Composio, Pipedream, or a future integration runtime.

## Product Principle

A JIT integration is:

```text
runtime + connection + schema + mapping + writeback contract
```

Nango is one runtime. The user-facing object is the integration definition.

## Goal

Allow users to define provider-backed file surfaces with JSON Schema and mapping code, then attach them to a workspace.

Example:

```bash
relayfile integration push ./acme-tickets.integration.ts \
  --workspace support-bot \
  --provider acme-tickets \
  --runtime nango \
  --connection-id conn_existing_123
```

The result:

```text
./relayfile-mount/acme-tickets/tickets/TCK-123.json
./relayfile-mount/acme-tickets/tickets/TCK-123/comments/new.json
```

Agents see normal files. The custom integration owns how records become files and how writeback files become provider actions.

## Non-Goals

- Do not allow arbitrary code to run in Agent Relay lambdas without sandboxing.
- Do not make relayfile core provider-aware.
- Do not make user schemas silently override canonical schemas for built-in providers.
- Do not require BYO schemas for built-in integrations.

## Integration Definition

The source artifact should be TypeScript first, with JSON output as the transport format.

```ts
import { defineIntegration } from "@relayfile/integration-runtime";

export default defineIntegration({
  id: "acme-tickets",
  provider: "acme-tickets",
  runtime: "nango",

  connection: {
    providerConfigKey: "acme-tickets",
  },

  files: [
    {
      path: "/acme-tickets/tickets/{id}.json",
      schema: {
        type: "object",
        required: ["id", "title", "status"],
        additionalProperties: false,
        properties: {
          id: { type: "string" },
          title: { type: "string" },
          status: { enum: ["open", "closed"] },
          assignee: { type: ["string", "null"] },
        },
      },
      sync: {
        model: "Ticket",
        map: (record) => ({
          path: `/acme-tickets/tickets/${record.id}.json`,
          contentType: "application/json",
          content: {
            id: record.id,
            title: record.title,
            status: record.status,
            assignee: record.assignee ?? null,
          },
        }),
      },
    },
  ],

  writebacks: [
    {
      path: "/acme-tickets/tickets/{id}/comments/new.json",
      schema: {
        type: "object",
        required: ["body"],
        additionalProperties: false,
        properties: {
          body: { type: "string", minLength: 1 },
        },
      },
      action: async ({ params, content, runtime }) => {
        await runtime.proxy({
          method: "POST",
          endpoint: `/tickets/${params.id}/comments`,
          body: content,
        });
      },
    },
  ],
});
```

## Compiled Manifest

Cloud should persist a compiled manifest, not arbitrary source code:

```ts
type JitIntegrationManifest = {
  manifestVersion: "2026-05-07";
  id: string;
  provider: string;
  runtime: "nango" | "composio" | "pipedream";
  files: Array<{
    pathTemplate: string;
    schemaId: string;
    schema: object;
    sync?: {
      model?: string;
      endpoint?: string;
      method?: string;
    };
  }>;
  writebacks: Array<{
    pathTemplate: string;
    schemaId: string;
    schema: object;
    action: {
      runtime: string;
      endpointTemplate: string;
      method: string;
    };
  }>;
};
```

Generated functions should be deployed to the runtime-specific sandbox:

- Nango: programmatic sync deployment per cloud#457.
- Composio: action/proxy executor or hosted adapter worker.
- Pipedream: Connect/account proxy plus workflow/action invocation.

## Schema Registry

Built-in schemas live in `schemas/` in the relayfile repo.

User schemas need a workspace-scoped registry:

```ts
type WorkspaceSchema = {
  id: string;
  workspaceId: string;
  provider: string;
  pathTemplate: string;
  direction: "read" | "write";
  schema: object;
  version: number;
  status: "draft" | "active" | "deprecated";
};
```

Rules:

- Built-in schemas are global and versioned with relayfile.
- BYO schemas are workspace-scoped unless explicitly promoted.
- BYO schemas cannot replace a built-in provider/path without an explicit override flag.
- Writeback schemas are required before a BYO writeback path becomes active.

## API Contract

### Push Integration

```http
POST /api/v1/workspaces/{workspaceId}/jit-integrations
```

Request:

```json
{
  "provider": "acme-tickets",
  "runtime": "nango",
  "connectionId": "conn_existing_123",
  "manifest": {
    "manifestVersion": "2026-05-07",
    "id": "acme-tickets",
    "files": [],
    "writebacks": []
  }
}
```

Response:

```json
{
  "integrationId": "jit_acme_tickets",
  "provider": "acme-tickets",
  "runtime": "nango",
  "status": "deploying",
  "diagnosticsUrl": "/api/v1/workspaces/rw_123/jit-integrations/jit_acme_tickets/diagnostics"
}
```

### Validate Schema

```http
POST /api/v1/workspaces/{workspaceId}/schemas/validate
```

Request:

```json
{
  "path": "/acme-tickets/tickets/TCK-123.json",
  "direction": "read",
  "content": {
    "id": "TCK-123",
    "title": "Broken login",
    "status": "open"
  }
}
```

## CLI Contract

```bash
relayfile integration validate ./acme-tickets.integration.ts

relayfile integration push ./acme-tickets.integration.ts \
  --workspace support-bot \
  --runtime nango \
  --connection-id conn_existing_123

relayfile integration diagnostics acme-tickets --workspace support-bot
```

Keep Nango-specific sugar as a compatibility alias:

```bash
relayfile sync push my-sync.ts --provider-config-key acme-tickets
```

This should compile down to the same JIT integration machinery with `runtime=nango`.

## Security Requirements

- Source code must be compiled and sandboxed before execution.
- Runtime secrets must never be exposed to the CLI or agent.
- Manifests must be validated before activation.
- Writeback actions must be declared and schema-validated.
- Diagnostics must redact secrets, connection IDs where appropriate, and Authorization headers.

## Phases

### Phase 1 - Manifest And Schema Registry

- Define manifest JSON.
- Add workspace schema registry.
- Add validation endpoint and tests.

### Phase 2 - Nango JIT Compatibility

- Implement cloud#457 on top of the manifest shape.
- `relayfile sync push` becomes a Nango-specific alias.
- Nango compile/deploy errors surface through diagnostics.

### Phase 3 - BYO Read Paths

- Activate custom read file paths.
- Sync records into relayfile using user schemas.
- List integrations returns BYO roots and schemas.

### Phase 4 - BYO Writeback Paths

- Activate custom writeback paths.
- Validate writes against user schemas before enqueue.
- Route actions through runtime driver.

### Phase 5 - Non-Nango Runtimes

- Add Composio driver support.
- Add Pipedream driver support.
- Prove one integration on each runtime.

## Acceptance Criteria

- A user can push a Nango-backed custom integration and see files under a custom provider root.
- A malformed schema fails before deployment.
- A malformed sync implementation returns actionable diagnostics.
- A write to an invalid writeback file is rejected with schema details.
- Agent-facing filesystem behavior is identical for built-in and BYO integrations.

## Workflow Inputs

Recommended workflow slices:

1. `jit-manifest-schema`
2. `workspace-schema-registry`
3. `jit-validate-cli`
4. `nango-jit-push-compat`
5. `jit-read-path-runtime`
6. `jit-writeback-runtime`
7. `composio-jit-driver`
8. `pipedream-jit-driver`
