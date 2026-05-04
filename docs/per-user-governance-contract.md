# Per-User Governance Contract

**Status:** Draft for cross-repo implementation
**Date:** 2026-05-04
**Owners:** Relayfile, Relayauth, Relayfile Cloud, Relayfile Providers, Relayfile Adapters
**Audience:** Implementers of `relayfile`, `../relayauth`, `../cloud`, `../relayfile-providers`, and `../relayfile-adapters`.

This document specifies how Relayfile and Relayauth provide MCP-style per-user
governance through a filesystem interface. It responds to the production-agent
problem usually framed as "MCP vs CLI": CLI is efficient when the developer is
the user, but customer-facing agents need per-user authorization, tenant
isolation, revocation, explicit action boundaries, and structured audit.

Relayfile's answer is:

> MCP governs per-user tool calls. Relayfile governs per-user filesystem paths
> and writeback operations.

The product goal is to keep the agent interface small and file-native while
matching the governance properties that make MCP viable for multi-tenant
products.

---

## 1. Problem

A customer-facing agent must act for a specific human user inside a specific
tenant. For example:

```text
User Jane at Acme asks an agent to read a GitHub PR, create a Jira issue,
and notify a Slack channel.
```

The system must answer these questions for every action:

1. Which agent performed the action?
2. Which human user authorized or sponsored the action?
3. Which tenant and workspace owned the data?
4. Which provider connection supplied the provider credentials?
5. Which scoped capability allowed the operation?
6. Which provider-side action was executed?
7. How can the action be audited, replayed, denied, or revoked?

CLI-style ambient credentials do not answer these questions at the protocol
boundary. Direct MCP can answer them, but often at the cost of loading many
provider-specific tool schemas into the model context. Relayfile must answer
them without making the agent carry a large tool surface.

---

## 2. Goals

1. Support per-user provider connections for GitHub, Slack, Notion, Linear,
   Jira, and future providers.
2. Ensure provider secrets and refresh tokens remain server-side only.
3. Authorize Relayfile reads, writes, operations, and writebacks with
   Relayauth scopes.
4. Preserve tenant isolation with `orgId` and `workspaceId` on every token,
   provider connection, VFS request, writeback operation, and audit entry.
5. Mint separate scoped tokens for each agent and sub-agent.
6. Ensure sub-agent scopes are strict subsets of the parent agent's effective
   scopes.
7. Make provider-backed paths fail-closed by default.
8. Record structured audit events that trace every action to a human sponsor.
9. Support revocation of tokens, sessions, identities, provider connections,
   and workspace/provider policies.
10. Keep the agent-facing interface file-native: `ls`, `cat`, file writes,
    `_PERMISSIONS.md`, `.relay/state.json`, and SDK helpers.

---

## 3. Non-Goals

1. This spec does not require Relayfile to become an MCP server.
2. This spec does not require exposing provider OAuth tokens to agents.
3. This spec does not require a full CRDT or offline-first editing model.
4. This spec does not require every provider to support every action on day one.
5. This spec does not replace `docs/productized-cloud-mount-contract.md`; it
   adds the governance layer required for customer-facing deployments.

---

## 4. Identity Model

Every governed Relayfile action has three identities:

| Identity | Meaning | Source of Truth |
| --- | --- | --- |
| Agent identity | The agent process making the request | Relayauth `sub` |
| User identity | The human sponsor or authorizing user | Relayauth `sponsorId` and Cloud user record |
| Tenant identity | The organization/workspace boundary | Relayauth `org`, `wks`, Cloud workspace |

The canonical Relayauth access token for Relayfile must include:

```json
{
  "sub": "agent_review_bot",
  "org": "org_acme",
  "wks": "ws_acme_support",
  "sponsorId": "user_jane",
  "sponsorChain": ["user_jane", "agent_lead", "agent_review_bot"],
  "scopes": [
    "relayfile:fs:read:/github/repos/acme/api/pulls/*",
    "relayfile:fs:write:/github/repos/acme/api/pulls/*/reviews/*",
    "relayfile:ops:read:/github/*"
  ],
  "aud": ["relayfile"],
  "exp": 1772004000,
  "jti": "tok_example"
}
```

Relayfile must reject a token when:

1. The token audience does not include `relayfile`.
2. The token workspace does not match the requested workspace.
3. The token organization does not match the workspace's organization.
4. The token is expired, revoked, malformed, or signed by an unknown key.
5. No scope authorizes the requested operation.

---

## 5. Provider Connection Model

Provider connections are owned by a user in a tenant and workspace.

```ts
interface ProviderConnection {
  id: string;
  orgId: string;
  workspaceId: string;
  userId: string;
  provider: string;
  providerAccountId: string;
  providerConfigKey: string;
  grantedOAuthScopes: string[];
  status: "pending" | "ready" | "revoked" | "error";
  createdAt: string;
  updatedAt: string;
  revokedAt?: string;
  revokedBy?: string;
  lastRefreshAt?: string;
}
```

Rules:

1. Provider access tokens and refresh tokens must remain server-side.
2. Agents must never receive provider OAuth credentials.
3. A writeback operation must resolve a provider connection by
   `orgId`, `workspaceId`, `provider`, and user/sponsor policy.
4. A provider connection may be user-owned or tenant-admin delegated.
5. A tenant-admin delegated connection must be explicit in audit metadata.
6. A revoked provider connection must deny new reads, writes, refreshes, and
   writebacks for paths that require that connection.

---

## 6. Scope Mapping

Relayfile converts filesystem and operation requests into Relayauth scopes.

| Relayfile Request | Required Scope |
| --- | --- |
| Read `/github/repos/acme/api/pulls/42/metadata.json` | `relayfile:fs:read:/github/repos/acme/api/pulls/42/metadata.json` |
| Write `/github/repos/acme/api/pulls/42/reviews/review.json` | `relayfile:fs:write:/github/repos/acme/api/pulls/42/reviews/review.json` |
| Delete `/tmp/generated.md` | `relayfile:fs:delete:/tmp/generated.md` |
| Read operation log for GitHub | `relayfile:ops:read:/github/*` |
| Replay a failed GitHub writeback | `relayfile:ops:replay:/github/*` |
| Trigger provider refresh | `relayfile:sync:trigger:/github/*` |
| Manage provider integration | `relayfile:integration:manage:/github/*` |

Relayauth filesystem matching rules apply:

1. `*` matches all paths.
2. `/prefix/*` matches descendants under the prefix.
3. Exact paths match only themselves.
4. Mid-path wildcards and `..` are invalid.

Relayfile must use normalized absolute paths before scope evaluation.

---

## 7. Writeback Policy

`_PERMISSIONS.md` remains the human/agent-readable contract, but server
enforcement must use a machine-readable writeback policy emitted by adapters.

```ts
interface WritebackPolicy {
  pathPattern: string;
  provider: string;
  providerAction: string;
  relayfileScope: string;
  requiredOAuthScopes: string[];
  payloadSchemaRef: string;
  connectionResolution: "sponsor-user" | "tenant-delegated" | "explicit";
  idempotencyKeyFields: string[];
  conflictBehavior: "etag-required" | "append-only" | "replace";
}
```

Example:

```json
{
  "pathPattern": "/github/repos/{owner}/{repo}/pulls/{number}/reviews/review.json",
  "provider": "github",
  "providerAction": "pull_request.review.create",
  "relayfileScope": "relayfile:fs:write:/github/repos/*",
  "requiredOAuthScopes": ["pull_requests:write"],
  "payloadSchemaRef": "github.pull_request_review.v1",
  "connectionResolution": "sponsor-user",
  "idempotencyKeyFields": ["workspaceId", "path", "baseRevision"],
  "conflictBehavior": "etag-required"
}
```

Rules:

1. A writeback path must have a matching `WritebackPolicy`.
2. Relayfile must deny writes to provider-backed paths when no policy matches.
3. Relayfile must validate the payload before enqueueing writeback.
4. Relayfile must verify the Relayauth scope before enqueueing writeback.
5. The writeback worker must verify the provider connection is active before
   executing the provider API call.
6. The writeback worker must include the policy ID, provider action, agent ID,
   sponsor ID, provider connection ID, workspace ID, and correlation ID in the
   operation record.

---

## 8. Request Flow

### 8.1 Read Flow

```text
Agent reads /github/repos/acme/api/pulls/42/metadata.json
  -> mount or SDK sends Relayfile API request with Relayauth token
  -> Relayfile verifies token signature, audience, org, workspace, expiry
  -> Relayfile checks relayfile:fs:read:/github/repos/acme/api/pulls/42/metadata.json
  -> Relayfile checks provider connection and sync status
  -> Relayfile returns file content
  -> Relayauth/Relayfile audit records token validation and scope check
```

### 8.2 Writeback Flow

```text
Agent writes /github/repos/acme/api/pulls/42/reviews/review.json
  -> mount detects local write and uploads content
  -> Relayfile verifies token and checks relayfile:fs:write:<path>
  -> Relayfile resolves WritebackPolicy for the path
  -> Relayfile validates payload schema
  -> Relayfile resolves provider connection for sponsor user or tenant delegate
  -> Relayfile queues writeback with correlation metadata
  -> writeback worker executes provider action server-side
  -> operation is acked, dead-lettered, or moved to conflict state
  -> audit records scope check, provider action, result, and sponsor chain
```

---

## 9. Delegation and Agent Invites

Every invited agent must receive its own Relayauth identity and token.

```ts
interface AgentInviteRequest {
  agentName: string;
  requestedScopes: string[];
  ttlSeconds: number;
  relaycastScopes?: string[];
}
```

Rules:

1. The child token's scopes must be a subset of the parent token's effective
   scopes.
2. The child token's expiry must be less than or equal to the parent token's
   expiry.
3. The child sponsor chain must append the child agent to the parent chain.
4. Attempted escalation must fail and emit an audit event.
5. The invite payload must not reuse the lead agent's Relayfile token.

This supersedes the v1 caveat in `docs/agent-workspace-golden-path.md` that
allows lead-token reuse.

---

## 10. Audit Contract

Relayfile and Relayauth must produce enough structured audit data to answer:

```text
Which human authorized this provider action, through which agent, against
which tenant workspace, using which provider connection, and which scope
allowed or denied it?
```

Every governed Relayfile operation must carry:

```ts
interface GovernanceAuditContext {
  requestId: string;
  correlationId: string;
  orgId: string;
  workspaceId: string;
  agentId: string;
  sponsorId: string;
  sponsorChain: string[];
  tokenId: string;
  requestedScope: string;
  matchedScope?: string;
  path: string;
  provider?: string;
  providerConnectionId?: string;
  providerAction?: string;
  writebackPolicyId?: string;
  result: "allowed" | "denied" | "error";
  reason?: string;
}
```

Required events:

1. Token issued.
2. Token refreshed.
3. Token revoked.
4. Scope checked.
5. Scope denied.
6. Provider connection created.
7. Provider connection revoked.
8. Relayfile file read.
9. Relayfile file write accepted.
10. Relayfile file write denied.
11. Writeback queued.
12. Writeback executed.
13. Writeback failed.
14. Writeback replayed.

Relayauth remains the authority for token, identity, scope, role, policy, and
revocation audit. Relayfile remains the authority for VFS, sync, writeback,
conflict, dead-letter, and replay audit. Both systems must share
`correlationId`.

---

## 11. Revocation

The governed system must support these revocation surfaces:

| Revocation | Owner | Effect |
| --- | --- | --- |
| Token revocation | Relayauth | Presented token is denied after revocation propagation |
| Session revocation | Relayauth | All tokens in the session are denied |
| Identity suspension | Relayauth | Agent cannot authenticate or authorize actions |
| Role/policy change | Relayauth | Effective scopes narrow on next authorization check |
| Provider connection revocation | Cloud/Provider layer | Provider-backed reads/writes requiring the connection are denied |
| Workspace integration disconnect | Cloud/Relayfile | Provider root becomes unavailable or read-only per policy |
| Writeback cancellation | Relayfile | Pending operation is marked canceled/denied before provider execution |

Revocation must be fail-closed. If Relayfile cannot determine whether a token,
identity, workspace, provider connection, or policy is active, provider-backed
writeback must be denied.

---

## 12. Repository Ownership

### 12.1 `relayfile` repository

Owns the filesystem data plane and this cross-repo contract.

Required changes:

1. Add Relayauth verification and scope enforcement at every REST and mount
   boundary.
2. Normalize every VFS path before scope checks.
3. Make provider-backed paths fail-closed when no Relayauth scope or
   writeback policy matches.
4. Add `relayfile:ops:*`, `relayfile:sync:*`, and `relayfile:integration:*`
   scope checks for non-file APIs.
5. Store governance metadata on operation logs, writeback ops, conflicts,
   dead letters, and replay records.
6. Extend SDK and mount clients to preserve correlation IDs and surface
   governance denial reasons.
7. Generate agent-readable `_PERMISSIONS.md` from the same policy source used
   for server enforcement.

### 12.2 `../relayauth` repository

Owns identity, token, scope, RBAC, policy, revocation, and audit semantics.

Required changes:

1. Ensure the token format supports `org`, `wks`, `sponsorId`,
   `sponsorChain`, `jti`, `aud`, `exp`, and delegated `parentTokenId`.
2. Ensure scope matching supports all Relayfile scope families used here:
   `fs`, `ops`, `sync`, and `integration`.
3. Provide APIs for child-token issuance with subset enforcement.
4. Emit audit events for attempted delegation escalation.
5. Expose token/session/identity revocation APIs that Relayfile can honor.
6. Keep public packages platform-agnostic; Cloudflare-specific storage and
   Durable Object implementation belongs in `../cloud/packages/relayauth`.

### 12.3 `../cloud` repository

Owns the hosted control plane for workspaces, users, provider connections,
OAuth connect sessions, and Cloudflare-specific Relayauth deployment.

Required changes:

1. Persist `ProviderConnection` records with `orgId`, `workspaceId`, `userId`,
   `provider`, `providerAccountId`, `grantedOAuthScopes`, and revocation state.
2. Bind workspace integrations to provider connection IDs rather than only
   provider names.
3. Resolve provider credentials server-side during writeback by sponsor user or
   explicit tenant-delegated policy.
4. Ensure `connect-session` records the connecting user and tenant.
5. Add provider-connection revocation and disconnect endpoints.
6. Store and expose audit events for provider connection lifecycle.
7. Implement Cloudflare storage adapters for any new Relayauth token,
   identity, revocation, or audit interfaces.

If Relayfile Cloud ownership moves to `../relayfile-cloud`, the hosted
workspace/provider-connection requirements in this section move with that
service. The current active route surface referenced by the productized mount
contract lives in `../cloud`.

### 12.4 `../relayfile-providers` repository

Owns provider-token retrieval and provider API proxy execution.

Required changes:

1. Accept a typed provider execution context:

   ```ts
   interface ProviderExecutionContext {
     orgId: string;
     workspaceId: string;
     userId: string;
     sponsorId: string;
     providerConnectionId: string;
     provider: string;
     requestedOAuthScopes: string[];
     correlationId: string;
   }
   ```

2. Deny provider API execution if the provider connection is revoked, missing,
   or missing required OAuth scopes.
3. Never allow agents to supply raw provider tokens.
4. Emit provider errors in a structured form suitable for Relayfile dead-letter
   records.

### 12.5 `../relayfile-adapters` repository

Owns provider-to-VFS mapping and provider writeback action definitions.

Required changes:

1. Emit machine-readable `WritebackPolicy` definitions for every writable path.
2. Generate `_PERMISSIONS.md` from the same policy definitions.
3. Include provider action names and payload schema references in writeback
   outputs.
4. Preserve provider object IDs in VFS metadata so audit and replay can refer
   to provider-side objects, not only paths.

### 12.6 `../relaycast` repository

Relaycast does not own Relayfile authorization, but agent invite and
coordination flows must preserve identity.

Required changes:

1. Relaycast invite payloads should carry child agent identity metadata, not
   reused parent Relayfile tokens.
2. Relaycast events should preserve `correlationId` when a message coordinates
   a Relayfile operation.

---

## 13. Acceptance Criteria

### 13.1 Per-user provider action

Given:

1. Jane belongs to Acme.
2. Jane connects GitHub and Slack through Relayfile Cloud.
3. A review agent receives a Relayauth token scoped to Acme's GitHub PRs and
   Slack support channel.

When the agent reads a GitHub PR and writes a Slack message:

1. Relayfile allows only paths covered by the token.
2. Relayfile executes provider calls with Jane's Acme provider connections.
3. The agent never receives provider OAuth tokens.
4. Audit records identify Jane, the agent, Acme, the workspace, the provider
   connection, the path, the requested scope, and the provider action.

### 13.2 Cross-tenant denial

Given a token for `org_acme` and `ws_acme`, any request against `ws_globex` or
Globex provider paths must be denied, even if the path string resembles an
allowed Acme path.

### 13.3 Sub-agent narrowing

Given a lead token with:

```text
relayfile:fs:read:/github/*
relayfile:fs:write:/github/*/reviews/*
```

When the lead invites a sub-agent requesting:

```text
relayfile:fs:read:/github/*
relayfile:fs:write:/slack/*
```

Then Relayauth must reject issuance and emit an attempted escalation audit
event.

### 13.4 Provider revocation

Given Jane revokes her Slack connection, new writes to Slack-backed paths that
require Jane's connection must be denied. Pending writebacks using that
connection must be canceled, denied, or dead-lettered with a revocation reason.

### 13.5 Missing policy denial

Given a provider-backed write path with no matching `WritebackPolicy`,
Relayfile must deny the write before enqueueing provider work, even if the
agent has broad `relayfile:fs:write:*`.

---

## 14. Open Questions

1. Should reads from provider-backed cached files require an active provider
   connection, or should previously synced content remain readable until a
   workspace policy revokes it?
2. Should tenant-admin delegated provider connections be visible to agents as a
   separate provider root, or only as metadata in `_PERMISSIONS.md`?
3. Should provider OAuth scopes be modeled as Relayauth scopes, or kept as
   provider-connection metadata checked by the provider layer?
4. What is the maximum acceptable revocation propagation time for mounted local
   mirrors?
5. Should provider-backed paths be unreadable or visibly stubbed after
   revocation?
6. Should Relayfile expose a MCP compatibility server later, backed by the same
   governance model, for clients that require MCP?

---

## 15. Positioning

The external claim should be:

> Relayfile provides MCP-style per-user governance through a filesystem
> interface. Users authorize provider connections once; Relayauth mints scoped
> agent tokens; Relayfile enforces path-level capabilities and server-side
> writeback; every operation is attributable to a human, tenant, provider
> connection, and agent.

The claim should not be:

> Relayfile has no context cost.

Relayfile reduces integration-specific tool context by replacing many tool
schemas with one filesystem contract, but agents still need path conventions,
sync state, writeback policies, and denial handling.
