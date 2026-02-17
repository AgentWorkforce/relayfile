# relayfile

Queue-first virtual filesystem-over-REST that ingests noisy external webhooks, projects a file tree, and executes conflict-safe writeback with retries, dead-lettering, and replay.

## What this service does

- Exposes a workspace-scoped filesystem API (`/fs/tree`, `/fs/file`, `/fs/events`).
- Ingests webhook envelopes asynchronously through internal ingress.
- Handles noisy webhook traffic with dedupe, coalescing, staleness checks, and loop suppression.
- Executes outbound writeback with retries, dead-letter handling, and replay.
- Exposes operational APIs for sync status, ingress metrics, dead letters, and operation feeds.
- Ships an OpenAPI contract and a TypeScript SDK.

## Current API highlights

- Filesystem:
  - `GET /v1/workspaces/{workspaceId}/fs/tree`
  - `GET /v1/workspaces/{workspaceId}/fs/file`
  - `PUT /v1/workspaces/{workspaceId}/fs/file`
  - `DELETE /v1/workspaces/{workspaceId}/fs/file`
  - `GET /v1/workspaces/{workspaceId}/fs/events`
- Operations:
  - `GET /v1/workspaces/{workspaceId}/ops`
  - `GET /v1/workspaces/{workspaceId}/ops/{opId}`
  - `POST /v1/workspaces/{workspaceId}/ops/{opId}/replay`
- Admin:
  - `GET /v1/admin/backends` (active backend profile + queue/state backend types)
  - `GET /v1/admin/ingress` (workspace ingress backlog + reliability counters with summary/alerts, optional `alertProfile` presets plus threshold overrides with `effectiveAlertProfile`, filters, `maxAlerts`, `includeWorkspaces`, `includeAlerts`, and `cursor`/`limit` pagination)
  - `GET /v1/admin/sync` (cross-workspace sync provider status with aggregated failure-code, dead-letter, and health counters, alert thresholds/totals, optional `maxAlerts` and `includeAlerts`, plus `includeWorkspaces` and `cursor`/`limit` pagination)
  - `POST /v1/admin/replay/envelope/{envelopeId}`
  - `POST /v1/admin/replay/op/{opId}`
- Sync and ingress:
  - `GET /v1/workspaces/{workspaceId}/sync/status`
  - `GET /v1/workspaces/{workspaceId}/sync/ingress`
  - `GET /v1/workspaces/{workspaceId}/sync/dead-letter`
  - `POST /v1/workspaces/{workspaceId}/sync/dead-letter/{envelopeId}/replay`
  - `POST /v1/workspaces/{workspaceId}/sync/dead-letter/{envelopeId}/ack`
  - `POST /v1/workspaces/{workspaceId}/sync/refresh`

See `openapi/relayfile-v1.openapi.yaml` for contract details.

## Local run

```bash
go run ./cmd/relayfile
```

Durable local mode (state + queues persisted to disk):

```bash
RELAYFILE_STATE_FILE=.data/state.json \
RELAYFILE_ENVELOPE_QUEUE_FILE=.data/envelope-queue.json \
RELAYFILE_WRITEBACK_QUEUE_FILE=.data/writeback-queue.json \
go run ./cmd/relayfile
```

DSN-based backend mode:

```bash
RELAYFILE_STATE_BACKEND_DSN=file://$PWD/.data/state.json \
RELAYFILE_ENVELOPE_QUEUE_DSN=file://$PWD/.data/envelope-queue.json \
RELAYFILE_WRITEBACK_QUEUE_DSN=file://$PWD/.data/writeback-queue.json \
go run ./cmd/relayfile
```

Profile-based backend mode:

```bash
RELAYFILE_BACKEND_PROFILE=durable-local \
RELAYFILE_DATA_DIR=.data \
go run ./cmd/relayfile
```

Useful env vars:

- `RELAYFILE_ADDR` (default `:8080`)
- `RELAYFILE_JWT_SECRET`
- `RELAYFILE_INTERNAL_HMAC_SECRET`
- `RELAYFILE_MAX_BODY_BYTES`
- `RELAYFILE_STATE_FILE`
- `RELAYFILE_STATE_BACKEND_DSN` (`memory://`, `file:///...`)
- `RELAYFILE_BACKEND_PROFILE` (`inmemory`, `durable-local`)
- `RELAYFILE_DATA_DIR` (profile data directory, default `.relayfile`)
- `RELAYFILE_ENVELOPE_QUEUE_SIZE`
- `RELAYFILE_ENVELOPE_QUEUE_FILE`
- `RELAYFILE_WRITEBACK_QUEUE_FILE`
- `RELAYFILE_ENVELOPE_QUEUE_DSN` (`memory://`, `file:///...`)
- `RELAYFILE_WRITEBACK_QUEUE_DSN` (`memory://`, `file:///...`)
- `RELAYFILE_WRITEBACK_QUEUE_SIZE`
- `RELAYFILE_ENVELOPE_WORKERS`
- `RELAYFILE_WRITEBACK_WORKERS`
- `RELAYFILE_PROVIDER_MAX_CONCURRENCY`
- `RELAYFILE_COALESCE_WINDOW`
- `RELAYFILE_SUPPRESSION_WINDOW`
- `RELAYFILE_MAX_ENVELOPE_ATTEMPTS`
- `RELAYFILE_MAX_WRITEBACK_ATTEMPTS`

Optional Notion writeback integration env vars:

- `RELAYFILE_NOTION_TOKEN`
- `RELAYFILE_NOTION_TOKEN_FILE`
- `RELAYFILE_NOTION_TOKEN_CACHE_TTL`
- `RELAYFILE_NOTION_BASE_URL`
- `RELAYFILE_NOTION_API_VERSION`
- `RELAYFILE_NOTION_USER_AGENT`
- `RELAYFILE_NOTION_MAX_RETRIES`
- `RELAYFILE_NOTION_RETRY_BASE_DELAY`
- `RELAYFILE_NOTION_RETRY_MAX_DELAY`

Mount client env vars:

- `RELAYFILE_BASE_URL`
- `RELAYFILE_TOKEN`
- `RELAYFILE_WORKSPACE`
- `RELAYFILE_REMOTE_PATH`
- `RELAYFILE_MOUNT_PROVIDER`
- `RELAYFILE_LOCAL_DIR`
- `RELAYFILE_MOUNT_STATE_FILE`
- `RELAYFILE_MOUNT_INTERVAL`
- `RELAYFILE_MOUNT_INTERVAL_JITTER`
- `RELAYFILE_MOUNT_TIMEOUT`

## SDK

TypeScript SDK lives in `sdk/relayfile-sdk`.

- Client: `sdk/relayfile-sdk/src/client.ts`
- Types: `sdk/relayfile-sdk/src/types.ts`

## Mount client alpha

`relayfile-mount` provides polling-based local mirror + writeback sync.

Run once:

```bash
go run ./cmd/relayfile-mount --once \
  --base-url http://127.0.0.1:8080 \
  --workspace ws_123 \
  --remote-path /notion \
  --local-dir ./mount \
  --token "$RELAYFILE_TOKEN"
```

Run continuously:

```bash
go run ./cmd/relayfile-mount \
  --base-url http://127.0.0.1:8080 \
  --workspace ws_123 \
  --remote-path /notion \
  --local-dir ./mount \
  --token "$RELAYFILE_TOKEN" \
  --interval 2s
```

## Current status

Implemented:

- Filesystem-over-REST with optimistic concurrency.
- Webhook queue ingestion with noisy-event defenses.
- Writeback with retry/backoff, dead-lettering, replay safety, and provider attribution.
- Provider-level sync and ingress observability.
- OpenAPI + SDK contract alignment for current endpoints.

Remaining major work:

- Production persistence/queue backends (DB/queue/object storage).
- Full provider client implementations and auth/token lifecycle integration.
- Mount client hardening (true FUSE integration, inode cache semantics, conflict translation).
- Broader end-to-end/load/failure test suites and release packaging.
