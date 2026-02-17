# RelayFile Spec v1.0

## 1. Document Status

- Status: Draft for implementation kickoff
- Date: 2026-02-17
- Scope: RelayFile standalone service + tight relay-cloud integration
- Audience: relay-cloud, relayfile, dashboard, and SDK implementers

## 2. Problem Statement

Agents need a filesystem-native way to consume and update external systems (starting with Notion) without directly handling provider credentials. Existing webhook streams are noisy and bursty, and current docs split critical capabilities across phases (filesystem-over-REST, write-back, and reliability controls). We need a production-ready design that:

- Exposes a filesystem over REST in v1
- Is robust under noisy webhook traffic
- Supports bidirectional sync (external -> filesystem and filesystem -> external)
- Keeps relay-cloud as control plane and trust boundary
- Provides an SDK-first integration surface for agents and cloud services

## 3. Goals

1. Provide a standalone `relayfile` service with a stable REST + SDK contract.
2. Make `relay-cloud` the central webhook ingress and policy authority.
3. Support high-throughput, queue-first webhook ingestion and async reconciliation.
4. Guarantee deterministic file write semantics with optimistic concurrency.
5. Support write-back to external providers with retries, DLQ, replay, and loop prevention.
6. Ensure no long-lived provider tokens are exposed to sandboxes/agents.
7. Support optional local mounted VFS access via a thin mount client.

## 4. Non-Goals (v1)

1. Full bi-directional sync for every provider on day one.
2. Offline-first local editing with deferred reconciliation.
3. Cross-workspace global search and analytics.
4. Generic CRDT editing across arbitrary providers.

## 5. Design Principles

1. Control plane and data plane split:
`relay-cloud` = auth, policy, ingress, audit authority.
`relayfile` = sync processing, VFS state, write-back execution.

2. Queue-first reliability:
No expensive sync logic in webhook request handlers.

3. Provider identity over filesystem path:
Canonical object identity uses provider IDs (for Notion, `notion_id`).

4. Fail-closed access:
Policy must explicitly allow provider/file write actions.

5. Deterministic conflict behavior:
All writes use revision/etag preconditions.

## 6. High-Level Architecture

```
External Provider Webhooks
        |
        v
relay-cloud ingress
  - signature verification
  - workspace resolution
  - event envelope persistence
  - enqueue
        |
        v
relayfile ingest workers
  - dedupe + coalesce
  - normalize + order checks
  - apply provider delta -> VFS projection
  - emit fs events
        |
        +--> relayfile REST API + SDK
        |
        +--> write-back workers (outbox)
                - provider API updates
                - retry/backoff
                - DLQ/replay
                - loop suppression
```

## 7. Component Responsibilities

## 7.1 relay-cloud (Control Plane)

1. Public webhook endpoints.
2. Signature validation and provider source checks.
3. Workspace/connection resolution.
4. Policy enforcement authority.
5. Auth token issuance/verification.
6. Persist immutable webhook envelopes and enqueue jobs.
7. Central audit APIs for UI and operations.

## 7.2 relayfile (Data Plane)

1. VFS metadata and revision state.
2. Blob/content storage interface.
3. Ingest worker pipeline (normalize, dedupe, coalesce, apply).
4. Write-back outbox and provider adapters.
5. Filesystem-over-REST contract.
6. Change stream endpoint for incremental consumers.
7. Operational endpoints for sync status and replay.

## 7.3 relayfile SDK (TypeScript first)

1. Strongly-typed REST client.
2. Built-in retry/backoff for transient read/write errors.
3. Conflict-aware write helpers.
4. Event stream cursor handling.

## 7.4 relayfile mount client (optional)

1. Mount local filesystem path via FUSE.
2. Map POSIX read/write/list ops to RelayFile REST.
3. Maintain a small local read cache.
4. Respect etag preconditions on writes.

## 8. Trust, Auth, and Policy Model

1. Sandboxes/agents authenticate with Relay-issued bearer tokens.
2. Token must include signed claims:
`workspace_id`, `agent_name`, `scopes`, `exp`, `aud`.
3. `x-agent-name` headers are ignored for authorization.
4. Provider secrets and refresh tokens remain server-side only.
5. Policy checks are fail-closed for all integration actions.
6. Scope minimums:
`fs:read`, `fs:write`, `sync:trigger`, `ops:read`.

## 9. Data Model (Logical)

## 9.1 Core Tables

1. `webhook_envelopes`
- Immutable ingress records.
- Key fields: `id`, `workspace_id`, `provider`, `delivery_id`, `payload_hash`, `received_at`, `headers`, `payload`.
- Unique key: `(workspace_id, provider, delivery_id)`.

2. `provider_objects`
- Canonical source objects.
- Key fields: `workspace_id`, `provider`, `provider_object_id`, `type`, `parent_id`, `title`, `last_provider_edit_at`.
- Notion: `provider_object_id = notion_id`.

3. `fs_nodes`
- Projected filesystem tree.
- Key fields: `node_id`, `workspace_id`, `path`, `node_type`, `provider`, `provider_object_id`, `current_revision`.
- Unique key: `(workspace_id, path)`.

4. `fs_revisions`
- Per-node immutable revisions.
- Key fields: `revision_id`, `node_id`, `content_hash`, `size`, `created_at`, `origin` (`provider_sync` | `agent_write` | `system`).

5. `fs_blobs`
- Content-addressed storage map.
- Key fields: `content_hash`, `storage_key`, `content_type`, `size`.

6. `writeback_ops`
- Outbound operations.
- Key fields: `op_id`, `workspace_id`, `provider`, `node_id`, `base_revision`, `target_revision`, `status`, `attempt_count`, `next_attempt_at`, `last_error`.

7. `sync_cursors`
- Per-provider progress tracking.
- Key fields: `workspace_id`, `provider`, `cursor`, `watermark_ts`, `updated_at`.

8. `event_links`
- Correlation between inbound/outbound events and revisions.
- Key fields: `correlation_id`, `webhook_envelope_id`, `revision_id`, `writeback_op_id`.

## 9.2 Audit Tables

1. `integration_audit_log` in relay-cloud remains authoritative for access and policy decisions.
2. relayfile keeps operational audit for processing and reconciliation.
3. Both share `correlation_id` for cross-system traceability.

## 10. API Contract (Filesystem Over REST)

All endpoints are versioned and workspace-scoped:
`/v1/workspaces/{workspaceId}/...`

## 10.1 Tree

`GET /fs/tree?path=/notion&depth=2&cursor=...`

Response:

```json
{
  "path": "/notion",
  "entries": [
    {
      "path": "/notion/Engineering",
      "type": "dir",
      "revision": "rev_abc",
      "provider": "notion",
      "providerObjectId": "notion_page_123"
    }
  ],
  "nextCursor": null
}
```

## 10.2 File Read

`GET /fs/file?path=/notion/Engineering/Auth.md`

Response headers:
- `ETag: "rev_abc"`

Response body:

```json
{
  "path": "/notion/Engineering/Auth.md",
  "revision": "rev_abc",
  "contentType": "text/markdown",
  "content": "# Auth Design ...",
  "provider": "notion",
  "providerObjectId": "notion_page_123"
}
```

## 10.3 File Write

`PUT /fs/file?path=/notion/Engineering/Auth.md`

Headers:
- `If-Match: "rev_abc"` (required)

Request:

```json
{
  "contentType": "text/markdown",
  "content": "# Updated design ..."
}
```

Accepted response:

```json
{
  "opId": "op_123",
  "status": "queued",
  "targetRevision": "rev_new",
  "writeback": {
    "provider": "notion",
    "state": "pending"
  }
}
```

Conflict response (`409`):

```json
{
  "error": "revision_conflict",
  "path": "/notion/Engineering/Auth.md",
  "expectedRevision": "rev_abc",
  "currentRevision": "rev_def",
  "currentContentPreview": "# Someone else edited ..."
}
```

## 10.4 Delete

`DELETE /fs/file?path=/notion/Engineering/Auth.md`

Headers:
- `If-Match: "rev_abc"` required

## 10.5 Change Feed

`GET /fs/events?cursor=evt_123&limit=200`

Returns ordered events to allow incremental consumers:

```json
{
  "events": [
    {
      "eventId": "evt_124",
      "type": "file.updated",
      "path": "/notion/Engineering/Auth.md",
      "revision": "rev_def",
      "origin": "provider_sync",
      "correlationId": "corr_789",
      "timestamp": "2026-02-17T10:00:00Z"
    }
  ],
  "nextCursor": "evt_124"
}
```

## 10.6 Writeback Operation Status

`GET /ops/{opId}`

```json
{
  "opId": "op_123",
  "status": "succeeded",
  "attemptCount": 2,
  "lastError": null,
  "providerResult": {
    "providerRevision": "notion_rev_555"
  }
}
```

## 11. Webhook Ingestion Contract (relay-cloud -> relayfile)

## 11.1 Ingress Behavior

1. Verify signature.
2. Resolve workspace and provider connection.
3. Persist `webhook_envelope`.
4. Enqueue processing job.
5. Return `202` quickly.

## 11.2 Event Envelope Shape

```json
{
  "envelopeId": "whenv_123",
  "workspaceId": "ws_123",
  "provider": "notion",
  "deliveryId": "provider_delivery_abc",
  "receivedAt": "2026-02-17T10:00:00Z",
  "headers": { "x-signature": "..." },
  "payload": { "type": "sync", "syncName": "content-metadata" },
  "correlationId": "corr_789"
}
```

## 12. Noisy Webhook Strategy

1. Idempotency:
Drop duplicates using `(workspace_id, provider, delivery_id)`.

2. Coalescing windows:
Batch events by `(workspace, provider, object)` over short windows (for example 2-5 seconds) to reduce churn.

3. Staleness checks:
Ignore events older than current object watermark.

4. Backpressure:
Bound worker concurrency per workspace and provider.

5. Poison handling:
After max retries, move to DLQ with replay support.

## 13. Sync and Write-Back Semantics

## 13.1 External -> VFS

1. Worker loads envelope.
2. Normalizes payload to provider object deltas.
3. Applies delta to canonical provider object row.
4. Re-projects filesystem path/tree.
5. Writes new revision if content changed.
6. Emits `fs.events`.

## 13.2 VFS -> External

1. API accepts write with precondition.
2. Creates new local revision and queued `writeback_op`.
3. Worker sends provider update via adapter.
4. On success:
`status = succeeded`, provider revision stored.
5. On transient failure:
retry with exponential backoff.
6. On permanent failure:
`status = dead_lettered`, conflict surfaced via operation status and event.

## 13.3 Loop Prevention

1. Every write-back includes provenance metadata:
`origin=relayfile`, `op_id`, `correlation_id`.
2. Inbound events with matching provenance are suppressed or marked no-op.
3. Suppression window must be bounded and auditable.

## 14. Provider Adapter Contract

Each provider adapter must implement:

1. `normalizeWebhook(payload, headers) -> ProviderEvent[]`
2. `fetchObject(providerObjectId) -> ProviderObject`
3. `renderContent(providerObjectId) -> FileContent`
4. `writeContent(providerObjectId, content, precondition?) -> ProviderWriteResult`
5. `getOrderingKey(event) -> string`
6. `getIdempotencyKey(event) -> string`

Initial provider:
1. Notion (metadata sync + page content fetch + page update write-back).

## 15. SDK Specification (TypeScript)

Package: `@agent-relay/relayfile-sdk`

Core interface:

```ts
type WriteFileInput = {
  workspaceId: string;
  path: string;
  baseRevision: string;
  content: string;
  contentType?: string;
};

interface RelayFileClient {
  listTree(workspaceId: string, opts?: { path?: string; depth?: number; cursor?: string }): Promise<TreeResponse>;
  readFile(workspaceId: string, path: string): Promise<FileReadResponse>;
  writeFile(input: WriteFileInput): Promise<WriteQueuedResponse>;
  deleteFile(workspaceId: string, path: string, baseRevision: string): Promise<WriteQueuedResponse>;
  getEvents(workspaceId: string, cursor?: string, limit?: number): Promise<EventFeedResponse>;
  getOp(workspaceId: string, opId: string): Promise<OpStatusResponse>;
}
```

SDK behavior requirements:

1. Retries `429/5xx` with jittered backoff.
2. Exposes typed `RevisionConflictError` for `409`.
3. Supports cancellation via `AbortSignal`.

## 16. Mount Client Specification

Binary: `relayfile-mount`

Responsibilities:

1. Map `readdir` to `GET /fs/tree`.
2. Map `read` to `GET /fs/file`.
3. Map `write+flush` to `PUT /fs/file` with current revision.
4. Maintain local inode-to-path mapping cache.
5. Maintain ETag cache per file.

v1 constraints:

1. Single-writer assumptions per mounted path (no offline merge queue).
2. On conflict, return filesystem error and preserve local temp buffer.

## 17. Security and Compliance

1. No provider refresh tokens in agents/sandboxes.
2. Server-side encryption for all provider credentials.
3. Signed audit trail for all integration and write-back actions.
4. PII-safe logging:
content bodies redacted by default in logs.
5. Scoped API tokens with expiry and audience checks.

## 18. SLOs and Capacity Targets

1. Webhook ingress p95: `< 200ms`.
2. API tree/list p95 for 5k nodes: `< 150ms`.
3. Cached file read p95: `< 100ms`.
4. Write request ack p95: `< 250ms`.
5. Write-back success:
99% within 30s for healthy provider.
6. Data durability:
no acknowledged write loss under single-node failure.

## 19. Operational Model

## 19.1 Queues

1. `webhook_ingest`
2. `sync_apply`
3. `writeback`
4. `dlq_sync`
5. `dlq_writeback`

## 19.2 Replay

Administrative replay endpoints:

1. `POST /v1/admin/replay/envelope/{envelopeId}`
2. `POST /v1/admin/replay/op/{opId}`

## 19.3 Observability

Required metrics:

1. ingress qps, dedupe rate, coalesce ratio
2. worker lag, queue depth, retry rate, DLQ depth
3. API latency by endpoint and workspace tier
4. provider-specific failure codes

Required tracing:

1. `correlation_id` propagated across relay-cloud and relayfile.

## 20. Testing Strategy

1. Unit tests:
adapter normalization, revision checks, conflict generation.

2. Integration tests:
webhook -> envelope -> queue -> VFS projection.

3. End-to-end tests:
agent write -> writeback -> provider echo -> no-loop convergence.

4. Load tests:
noisy webhook bursts and hot-path read/write latency.

5. Failure tests:
provider 429/500 storms, queue outage, partial DB outage.

## 21. Rollout Plan

## Phase 1 (Foundation, 2 weeks)

1. RelayFile service skeleton.
2. VFS REST read/write APIs with revisions.
3. relay-cloud ingress envelope + queue-first behavior.
4. Notion read path from metadata + on-demand content.

Exit criteria:
Tree/read/write/delete/events endpoints live in staging.

## Phase 2 (Reliability, 2 weeks)

1. Idempotency, coalescing, and ordering safeguards.
2. Writeback outbox, retry policy, DLQ and replay.
3. Loop suppression and full correlation tracing.

Exit criteria:
Noisy webhook replay converges without corruption.

## Phase 3 (Hardening, 2 weeks)

1. SDK release and relay-cloud integration adapters.
2. SLO instrumentation and alerting.
3. Mount client alpha.

Exit criteria:
Pilot workspace can run end-to-end with operational dashboards.

## 22. Launch Blockers (Mandatory)

1. Filesystem-over-REST is v1 scope.
2. Canonical provider identity model (Notion: `notion_id`).
3. Queue-first webhook ingress.
4. Claim-based agent identity, no header trust.
5. No raw provider token exposure to sandboxes.
6. Fail-closed integration policy.
7. Optimistic concurrency on writes.
8. Durable write-back with retries + DLQ + replay.
9. Loop prevention with provenance markers.
10. End-to-end auditability via correlation IDs.

## 23. Open Decisions

1. Storage backend:
Postgres + object store vs Postgres-only for v1.

2. Queue technology:
Redis streams vs hosted queue for durability targets.

3. Notion DB rows representation:
JSON row files vs materialized markdown views.

4. Mount write behavior:
write-through only vs short buffered writes.

## 24. References

1. `../relay-cloud/docs/notion-vfs-spec.md`
2. `../relay-cloud/docs/daytona-token-service-spec.md`
3. `../relay-cloud/docs/daytona-integration-spec.md`
4. `../relay-cloud/packages/cloud/src/api/proxy.ts`
5. `../relay-cloud/packages/cloud/src/services/integration-access.ts`
6. `../relay-cloud/packages/cloud/src/services/provider-registry.ts`
7. `../relay-cloud/packages/cloud/src/api/generic-webhooks.ts`
8. `../relay-cloud/packages/cloud/src/webhooks/router.ts`
