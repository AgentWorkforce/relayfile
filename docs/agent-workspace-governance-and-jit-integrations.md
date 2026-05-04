# Agent Workspace Governance and JIT Integration Contract

**Status:** Draft for stacked PR review
**Date:** 2026-05-04
**Owners:** Relayfile, Relayauth, Cloud, Relayfile Adapters, Relayfile Providers, Sage
**Audience:** Implementers of `relayfile`, `../relayauth`, `../cloud`, `../relayfile-adapters`, `../relayfile-providers`, and `../sage`.

This document consolidates three threads:

1. The programmatic Cloud + Relayfile + Relayauth interface introduced by
   [cloud#406](https://github.com/AgentWorkforce/cloud/pull/406),
   [relayfile#70](https://github.com/AgentWorkforce/relayfile/pull/70), and
   [relayfile#71](https://github.com/AgentWorkforce/relayfile/pull/71).
2. The missing enterprise governance work tracked by
   [relayauth#37](https://github.com/AgentWorkforce/relayauth/issues/37).
3. The agent usability and readiness gaps tracked by
   [relayfile#79](https://github.com/AgentWorkforce/relayfile/issues/79).

It also adds a new contract for dynamic just-in-time integrations: an end
customer can define which provider objects they want, Relayfile Cloud can
generate a bounded integration plan, validate it with the customer's connected
account, run a Nango dry run inside a sandbox, and then promote the result into
a governed Relayfile VFS projection.

The core product claim is:

> Relayfile gives agents a file-native interface, while Cloud and Relayauth
> provide MCP-style per-user governance. Static integrations and just-in-time
> customer-defined integrations must share the same authorization, readiness,
> audit, and writeback contracts.

---

## 1. Current Programmatic Interface

The current v1 flow is Cloud-first. Programmatic clients should not call Nango
or Relayauth directly for provider setup. They call Cloud, and Cloud mints the
Relayfile token through Relayauth.

### 1.1 SDK login and token refresh

`cloudApiUrl` is optional and defaults to `https://agentrelay.com/cloud`.

```ts
import { RelayfileSetup } from "@relayfile/sdk";

const setup = await RelayfileSetup.login({
  onLoginUrl: async (url) => {
    await sendToUser(`Sign in to Relayfile Cloud: ${url}`);
  },
  onTokens: async (tokens) => {
    await saveTokenSet(tokens);
  },
});
```

On resume, callers should restore the saved Cloud token set:

```ts
const setup = RelayfileSetup.fromCloudTokens(await loadTokenSet(), {
  onTokens: async (tokens) => {
    await saveTokenSet(tokens);
  },
});
```

Headless services may use:

```ts
const setup = new RelayfileSetup({
  accessToken: async () => process.env.RELAYFILE_CLOUD_TOKEN!,
});
```

This is not the preferred interactive product path; it is for CI or service
accounts that already have a Cloud bearer token.

### 1.2 Workspace create or join

```ts
const workspace = await setup.createWorkspace({
  name: "acme-notion-room",
  agentName: "lead-agent",
  scopes: [
    "relayfile:fs:read:/notion/*",
    "relayfile:fs:write:/notion/*",
  ],
});
```

Cloud calls Relayauth during `join` and returns a Relayfile JWT plus the
Relayfile and Relaycast connection details.

### 1.3 Connect Notion

```ts
const notion = await workspace.connectNotion();

if (notion.connectLink) {
  await sendToUser(`Connect Notion: ${notion.connectLink}`);
}

await workspace.waitForNotion({
  connectionId: notion.connectionId,
  timeoutMs: 5 * 60_000,
});
```

The raw Cloud API shape is:

```http
POST /api/v1/workspaces/{workspaceId}/integrations/connect-session
Authorization: Bearer <relayfile-jwt>

{ "allowedIntegrations": ["notion"] }
```

The response must include a `connectLink` and `connectionId`. The status check
is:

```http
GET /api/v1/workspaces/{workspaceId}/integrations/notion/status?connectionId=<id>
Authorization: Bearer <relayfile-jwt>
```

### 1.4 Mount environment

```ts
const env = workspace.mountEnv({
  localDir: "/workspace/relayfile",
  remotePath: "/",
});
```

Agents read connected Notion data under the provider root returned by the Cloud
catalog. For Notion v1 this root is `/notion`.

---

## 2. Known Gaps to Close

### 2.1 Governance gaps

[relayauth#37](https://github.com/AgentWorkforce/relayauth/issues/37) tracks
the missing governance layer. The required properties are:

1. Per-agent Relayauth identities and tokens.
2. Child-token issuance with strict subset checks against the parent token.
3. Sponsor chains that trace every agent action back to a human.
4. Token, session, identity, and provider-connection revocation.
5. Scope families for `relayfile:fs`, `relayfile:ops`, `relayfile:sync`, and
   `relayfile:integration`.
6. Structured audit events that Relayfile can correlate with VFS and writeback
   operation logs.

### 2.2 Agent workspace usability gaps

[relayfile#79](https://github.com/AgentWorkforce/relayfile/issues/79) tracks
the missing SDK/readiness layer. The required properties are:

1. `workspace.listIntegrations()`.
2. `workspace.listMountedPaths()`.
3. `waitForConnection()` returning mounted path and sync status, not only a
   boolean.
4. A sync progress callback or polling helper.
5. A resume-safe way to discover already-connected integrations.
6. Invites with expiry and independently revocable per-agent tokens.

These are not cosmetic. Without them, an agent can connect Notion but still
guess `/notion` by convention and race an empty mount immediately after OAuth.

---

## 3. Unified Integration Status Contract

Static integrations and just-in-time integrations must expose the same status
shape.

```ts
interface WorkspaceIntegrationStatus {
  provider: string;
  kind: "static" | "jit";
  displayName: string;
  connectionId: string | null;
  providerConfigKey: string | null;
  vfsRoot: string;
  mountedPath: string;
  oauth: {
    connected: boolean;
    connectedAt: string | null;
    lastAuthAt: string | null;
    userId?: string;
    orgId?: string;
  };
  sync: {
    state: "pending" | "cataloging" | "syncing" | "ready" | "error" | "degraded";
    lagSeconds: number;
    watermarkTs: string | null;
    firstMaterializedAt: string | null;
    fileCount: number;
    lastError: string | null;
    webhookHealthy: boolean | null;
  };
  permissions: {
    readable: boolean;
    writable: boolean;
    requiredScopes: string[];
  };
}
```

The SDK must expose:

```ts
await workspace.listIntegrations();
await workspace.listMountedPaths();
await workspace.waitForIntegration("notion", {
  until: "materialized",
  onSyncProgress: (status) => report(status),
});
```

`waitForNotion()` may remain as a convenience wrapper, but it must resolve to
an integration status object rather than `void`.

---

## 4. Per-Agent Governance Contract

Every invited agent must receive its own Relayauth identity and token. The
current v1 `agentInvite()` behavior, where the lead token can be reused, must
be replaced for governed workspaces.

```ts
const reviewer = await workspace.createAgentInvite({
  agentName: "review-agent",
  scopes: ["relayfile:fs:read:/notion/*"],
  ttlSeconds: 3600,
});
```

Required behavior:

1. Cloud calls Relayauth to mint a child identity or child token.
2. Relayauth verifies the requested scopes are a subset of the parent effective
   scopes.
3. The child token expiry is no later than the parent token expiry.
4. The sponsor chain is extended.
5. The invite includes `expiresAt`, `agentId`, and revocation metadata.
6. Attempted escalation emits an audit event and fails closed.

---

## 5. Dynamic Just-in-Time Integrations

### 5.1 Problem

Today the provider catalog is largely predeclared:

1. `../cloud` defines Nango provider config keys and the integration catalog.
2. `../relayfile-adapters` defines VFS mappings, webhook normalization, and
   writeback paths.
3. `../sage/nango-integrations` currently owns concrete Nango sync definitions
   for product integrations such as `github-sage`, `linear-sage`,
   `notion-sage`, and `slack-sage`.
4. Cloud-side workers route Nango webhooks and materialize records into
   Relayfile.

That works for first-party integrations, but it does not let an end customer
say:

```text
For my connected HubSpot account, sync companies, contacts, and deals with
these fields and these writeback actions.
```

The customer should not need a code PR to Relayfile Adapters for every object
selection. Relayfile should be able to compile a customer-defined integration
plan, verify it against the customer's own connected account, run it through a
Nango dry run in a sandbox, and promote it only after the generated projection
is safe and inspectable.

Long term, Sage should consume the projected VFS and discovery APIs. It should
not remain the owner of provider config keys, Nango sync definitions, or object
selection policy.

### 5.1.1 Current ownership to migrate

The current split is:

| Concern | Current Location | Target Owner |
| --- | --- | --- |
| Static provider IDs and Nango config keys | `../cloud/packages/web/lib/integrations/providers.ts` | Cloud |
| Nango config-key environment overrides | `../cloud/packages/web/lib/integrations/nango-service.ts` | Cloud |
| Workspace connection records | Cloud `workspace_integrations` storage | Cloud |
| Nango sync definitions | `../sage/nango-integrations/*/syncs` | Cloud-managed generated artifacts |
| VFS path/resource/writeback mapping DSL | `../relayfile-adapters/docs/MAPPING_YAML_SPEC.md` and adapter-core runtime | Relayfile Adapters |
| Provider proxy/connection health primitives | `../relayfile-providers/packages/nango` | Relayfile Providers |

The migration goal is to move provider object definitions out of Sage and into
Cloud-managed integration specs backed by Relayfile Adapter mapping semantics.

### 5.2 JIT integration lifecycle

```text
Customer intent
  -> DraftIntegration manifest
  -> Provider connection or connect session
  -> Sandbox compilation
  -> Nango dry run
  -> VFS preview
  -> Human/customer approval
  -> Promote to workspace integration
  -> Initial sync and writeback policies
```

### 5.3 Consolidated integration spec

Static and JIT integrations should converge on a single `IntegrationSpec`
shape. Static specs may live in source control. JIT specs are workspace-scoped
runtime artifacts.

```ts
interface IntegrationSpec {
  id: string;
  kind: "static" | "jit";
  version: string;
  provider: string;
  providerConfigKey?: string;
  displayName: string;
  vfsRoot: string;
  connectionPolicy: "sponsor-user" | "tenant-delegated" | "explicit";
  requiredOAuthScopes: string[];
  objects: IntegrationObjectSpec[];
  writebacks: IntegrationWritebackSpec[];
  permissions: {
    readScopes: string[];
    writeScopes: string[];
  };
}

interface IntegrationObjectSpec {
  name: string;
  nangoModel?: string;
  syncName?: string;
  listEndpoint?: string;
  getEndpoint?: string;
  pathTemplate: string;
  fields?: string[];
  cursor?: string;
  materializeAs: "metadata-json" | "content-md" | "record-json";
}

interface IntegrationWritebackSpec {
  name: string;
  match: string;
  endpoint: string;
  payloadSchemaRef?: string;
  dryRunSafe: boolean;
}
```

Provider config keys are Cloud-resolved. Product code should pass
workspace/provider intent and must not hardcode raw Nango config keys unless it
is Cloud integration infrastructure.

### 5.4 Draft manifest

```ts
interface DraftIntegrationManifest {
  name: string;
  displayName: string;
  provider: string;
  providerConfigKey?: string;
  connectionId?: string;
  vfsRoot?: string;
  source:
    | { kind: "openapi"; url: string }
    | { kind: "docs"; url: string; crawlPaths?: string[] }
    | { kind: "samples"; files: string[] }
    | { kind: "nango-template"; templateId: string };
  objects: Array<{
    name: string;
    listEndpoint?: string;
    getEndpoint?: string;
    nangoModel?: string;
    pathTemplate: string;
    fields?: string[];
    cursor?: string;
    materializeAs: "metadata-json" | "content-md" | "record-json";
  }>;
  writebacks?: Array<{
    name: string;
    match: string;
    endpoint: string;
    payloadSchemaRef?: string;
    dryRunSafe: boolean;
  }>;
}
```

The manifest is intentionally close to the existing
`../relayfile-adapters/docs/MAPPING_YAML_SPEC.md` model. The difference is
that JIT manifests are tenant/workspace scoped, generated or edited at runtime,
and cannot be promoted without sandbox validation.

### 5.5 Draft APIs

Cloud owns the hosted API:

```http
POST /api/v1/workspaces/{workspaceId}/integration-drafts
GET  /api/v1/workspaces/{workspaceId}/integration-drafts/{draftId}
POST /api/v1/workspaces/{workspaceId}/integration-drafts/{draftId}/connect
POST /api/v1/workspaces/{workspaceId}/integration-drafts/{draftId}/dryrun
GET  /api/v1/workspaces/{workspaceId}/integration-drafts/{draftId}/preview
POST /api/v1/workspaces/{workspaceId}/integration-drafts/{draftId}/promote
```

The SDK should expose:

```ts
const draft = await workspace.createIntegrationDraft(manifest);
const connect = await draft.connect();
await sendToUser(connect.connectLink);
const dryrun = await draft.dryRun({ maxObjects: 25 });
const preview = await draft.preview();
await draft.promote({ approvedBy: "user_jane" });
```

### 5.6 Sandbox dry run

Cloud must run every JIT draft through an isolated sandbox before promotion.

The sandbox should use Nango dry-run semantics. The CLI form available locally
is:

```bash
nango dryrun <sync-or-action-name> <connection_id> \
  --environment <env> \
  --metadata '<json>' \
  --checkpoint '<json>' \
  --validate
```

Cloud may call an equivalent API instead of shelling out, but the behavior and
artifact shape must remain the same.

The dry run must:

1. Use the customer's selected provider connection.
2. Invoke Nango dry-run behavior through the Nango CLI or equivalent API.
3. Limit network access to Nango, the target provider, Relayfile Cloud, and
   configured package registries needed for the run.
4. Cap object count, runtime, memory, and output size.
5. Redact provider tokens and secrets from logs.
6. Produce a deterministic artifact bundle:
   - normalized mapping YAML
   - generated provider config metadata
   - object model schemas
   - sample VFS tree
   - dry-run logs
   - validation errors
   - required OAuth scopes
   - proposed writeback policies

Promotion is forbidden unless the dry run succeeds or a workspace admin
explicitly approves a degraded promotion mode.

The dry-run result must include a typed diff:

```ts
interface IntegrationDryRunResult {
  draftId: string;
  status: "succeeded" | "failed" | "degraded";
  recordsFetched: number;
  projectedFiles: Array<{ path: string; contentType: string; size: number }>;
  writebacksAvailable: string[];
  requiredOAuthScopes: string[];
  validationErrors: Array<{ code: string; message: string; path?: string }>;
  artifactId: string;
}
```

### 5.7 VFS preview

Before promotion, the customer and agent must be able to inspect a preview:

```text
/.relay/previews/{draftId}/tree.json
/.relay/previews/{draftId}/sample/<provider>/...
/.relay/previews/{draftId}/_PERMISSIONS.md
/.relay/previews/{draftId}/dryrun.log
```

Preview files are read-only and must not trigger provider writeback.

### 5.8 Promotion

Promotion creates a workspace-scoped integration record:

```ts
interface JitIntegrationRecord {
  id: string;
  kind: "jit";
  workspaceId: string;
  orgId: string;
  createdByUserId: string;
  provider: string;
  providerConfigKey: string;
  connectionId: string;
  vfsRoot: string;
  manifestHash: string;
  mappingArtifactId: string;
  dryRunArtifactId: string;
  status: "pending" | "syncing" | "ready" | "error" | "revoked";
}
```

The `vfsRoot` must be collision-safe. If a provider root already exists,
Cloud should default to:

```text
/custom/{integrationSlug}
```

The promoted JIT integration must then appear in:

```ts
await workspace.listIntegrations();
await workspace.listMountedPaths();
GET /api/v1/workspaces/{workspaceId}/integrations
GET /api/v1/workspaces/{workspaceId}/sync
```

Activation state must include:

1. Integration spec version.
2. Provider config key.
3. Connection ID.
4. Dry-run artifact ID.
5. Deployed integration or generated artifact ID.
6. Enabled object set.
7. Manifest hash.

### 5.9 Writeback policy for JIT integrations

JIT writebacks must use the same machine-readable policy model as static
adapters. A writeback is allowed only when:

1. The promoted manifest defines the writeback.
2. The writeback was included in the dry-run artifact.
3. The caller's Relayauth token includes the required Relayfile scope.
4. The provider connection is active.
5. The payload validates against the promoted schema.

If `dryRunSafe=false`, the dry run must validate schema and endpoint shape but
must not execute the provider mutation.

---

## 6. Repository Ownership

### 6.1 `relayfile`

Owns the cross-repo product contract and the agent-facing SDK surface.

Required changes:

1. Add this spec and keep it linked from the README.
2. Add SDK types and helpers for integration listing, mounted path discovery,
   materialized readiness, sync progress, per-agent invites, and JIT drafts.
3. Extend mount state files to include mounted paths, integration kind, file
   count, first materialization timestamp, and preview roots.
4. Enforce writeback policy consistently for static and JIT integrations.

### 6.2 `../cloud`

Owns hosted workspace control plane, provider connection state, static catalog,
JIT draft APIs, sandbox orchestration, Nango dry runs, and promotion.

Required changes:

1. Add integration draft persistence.
2. Add sandbox dry-run workers.
3. Store generated mapping/dry-run artifacts.
4. Promote validated drafts into workspace integration records.
5. Return static and JIT integrations through the same list/status/sync APIs.
6. Record provider connection ownership by user, org, and workspace.

### 6.3 `../relayauth`

Owns identity, scopes, child-token delegation, revocation, and audit. See
[relayauth#37](https://github.com/AgentWorkforce/relayauth/issues/37).

Required changes:

1. Support `relayfile:fs`, `relayfile:ops`, `relayfile:sync`, and
   `relayfile:integration` scope families.
2. Provide child-token issuance with subset enforcement.
3. Emit delegation, scope, denial, and revocation audit events.

### 6.4 `../relayfile-adapters`

Owns static and generated mapping semantics.

Required changes:

1. Promote the mapping YAML spec into the shared contract for generated JIT
   adapters.
2. Add validation for JIT manifests and generated mapping artifacts.
3. Generate `_PERMISSIONS.md` and machine-readable writeback policies from the
   same source.

### 6.5 `../relayfile-providers`

Owns provider execution and Nango/Composio/Nango-like provider clients.

Required changes:

1. Add a provider execution context that includes org, workspace, user,
   sponsor, provider connection, required OAuth scopes, and correlation ID.
2. Deny execution for missing, revoked, or under-scoped provider connections.
3. Expose a dry-run execution path that Cloud can call from the sandbox.

### 6.6 `../sage`

Owns agent product usage of the workspace and should stop hardcoding provider
object assumptions once discovery and JIT integrations are available.

Required changes:

1. Discover integrations via `workspace.listIntegrations()` or Cloud list APIs.
2. Prefer mounted path/status data over static provider assumptions.
3. Request JIT drafts when the customer asks for objects not covered by the
   static catalog.
4. Present dry-run previews and approval prompts to the customer.

---

## 7. Acceptance Criteria

### 7.1 Programmatic Notion connection

Given a fresh Cloud login, a caller can create a workspace, connect Notion,
wait for materialized readiness, discover the mounted path, and mount the
workspace without hardcoding `/notion`.

### 7.2 Resume safety

Given an existing workspace, `joinWorkspace()` plus `listIntegrations()` must
tell the agent which providers are connected, which roots are mounted, which
are ready, and which are still syncing.

### 7.3 Empty mount prevention

`waitForNotion({ until: "materialized" })` must not resolve until either:

1. At least one file is materialized under the Notion mounted path.
2. The integration reaches `ready` with a confirmed empty result set.
3. The timeout elapses and the returned status explains the ongoing sync state.

### 7.4 Per-agent revocation

An invited agent can be individually revoked without rotating the lead agent's
token or the whole workspace token set.

### 7.5 JIT integration dry run

Given a customer-defined object manifest and a customer provider connection,
Cloud can run a sandboxed Nango dry run, produce a preview VFS tree, and deny
promotion when schemas, paths, OAuth scopes, or provider calls fail validation.

### 7.6 JIT integration promotion

After approval, the promoted JIT integration appears in integration listing,
sync status, mounted path discovery, `_PERMISSIONS.md`, and writeback policy
enforcement.

---

## 8. Open Questions

1. Should JIT integration roots live under `/custom/{slug}` or may customers
   request first-class roots like `/hubspot` when no static provider exists?
2. Should Nango dry-run output be stored forever, retained for a fixed window,
   or retained only until promotion?
3. Should generated JIT mappings be portable back into
   `../relayfile-adapters` as static adapters after repeated use?
4. Should writeback mutations ever execute during dry run, or should mutation
   validation always be schema-only?
5. Which service owns sandbox execution long term: Cloud, Relayfile Cloud, or a
   shared Agent Relay sandbox service?
6. Should the JIT compiler be allowed to use an LLM to infer object mappings,
   or must v1 require explicit customer/admin manifests?

---

## 9. Positioning

Use this external framing:

> Relayfile gives agents one filesystem contract. Cloud and Relayauth provide
> per-user governance. Static integrations are prebuilt; JIT integrations let
> customers define the objects they need, validate them against their own
> connected account, preview the resulting VFS, and promote only after a
> sandboxed dry run succeeds.

Avoid this framing:

> Relayfile has no integration context cost.

Relayfile reduces tool-schema context, but it still needs discovery, readiness,
sync state, provider policies, and generated mapping artifacts. Those must be
machine-readable and agent-discoverable.
