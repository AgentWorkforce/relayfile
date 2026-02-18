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
  - `GET /v1/workspaces/{workspaceId}/fs/query` (structured metadata filters)
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

Semantic primitives (provider-agnostic):

- `semantics.properties` (key/value attributes)
- `semantics.relations` (cross-object IDs)
- `semantics.permissions` (ACL/entitlement references)
- `semantics.comments` (comment/reference IDs)

Permission policy (enforced on `fs/file`, `fs/tree`, `fs/query`, file updates/deletes):

- `scope:<name>`: allow if bearer token has scope `<name>`
- `agent:<name>`: allow if JWT `agent_name` matches `<name>`
- `workspace:<id>`: allow if workspace ID matches `<id>`
- `public` / `any` / `*`: allow all
- `deny:scope:<name>` / `deny:agent:<name>` / `deny:workspace:<id>`: explicit deny (takes precedence over allow)

Hierarchical inheritance:

- Place a policy marker file named `.relayfile.acl` in a directory.
- Its `semantics.permissions` rules are inherited by descendant files.
- Child file rules are appended after inherited rules.

If a file has `semantics.permissions` but none of the entries are recognized policy rules, entries are treated as metadata only (not enforced).

Example structured query:

```bash
TOKEN="$(./scripts/generate-dev-token.sh ws_live)"
curl -sS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_query_$(date +%s)" \
  "http://127.0.0.1:8080/v1/workspaces/ws_live/fs/query?path=/notion&property.topic=investments&relation=db_investments&permission=scope:fs:read&limit=20" | jq .
```

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

Postgres DSN mode:

```bash
RELAYFILE_STATE_BACKEND_DSN=postgres://localhost:5432/relayfile?sslmode=disable \
RELAYFILE_ENVELOPE_QUEUE_DSN=postgres://localhost:5432/relayfile?sslmode=disable \
RELAYFILE_WRITEBACK_QUEUE_DSN=postgres://localhost:5432/relayfile?sslmode=disable \
go run ./cmd/relayfile
```

Postgres adapter integration tests (requires running Postgres):

```bash
RELAYFILE_TEST_POSTGRES_DSN=postgres://localhost:5432/relayfile?sslmode=disable \
go test ./internal/relayfile -run PostgresIntegration -count=1
```

Profile-based backend mode:

```bash
RELAYFILE_BACKEND_PROFILE=durable-local \
RELAYFILE_DATA_DIR=.data \
go run ./cmd/relayfile
```

```bash
RELAYFILE_BACKEND_PROFILE=production \
RELAYFILE_PRODUCTION_DSN=postgres://localhost:5432/relayfile?sslmode=disable \
go run ./cmd/relayfile
```

Production profile validation runbook:

```bash
cat docs/production-validation.md
```

Contract surface check (OpenAPI <-> SDK):

```bash
./scripts/check-contract-surface.sh
```

## Docker Compose live run

Bring up Postgres + RelayFile + continuous mount sync (shared local mirror in `./.livefs`):

```bash
cp compose.env.example .env
# set RELAYFILE_NOTION_TOKEN in .env for real Notion writeback
docker compose up --build -d
docker compose logs -f relayfile mountsync
```

Open the local control dashboard:

```text
http://127.0.0.1:8080/dashboard
```

Use the token from `./scripts/generate-dev-token.sh ws_live` in the dashboard bearer-token field.

If you change `RELAYFILE_WORKSPACE` or `RELAYFILE_JWT_SECRET` in `.env`, generate a matching token and set it as `RELAYFILE_TOKEN`:

```bash
./scripts/generate-dev-token.sh ws_live
```

Send a live internal webhook envelope to materialize content into the mounted virtual filesystem:

```bash
./scripts/send-internal-envelope.sh /notion/AgentGuide.md \"# agent walkthrough\"
```

Import real Notion pages into RelayFile for local virtual-FS testing (without a Notion bridge service):

```bash
# uses RELAYFILE_NOTION_TOKEN from .env
./scripts/import-notion-pages.sh --database-id <notion_database_id> --max-pages 20
```

Search mode (if you don't want a fixed database id):

```bash
./scripts/import-notion-pages.sh --query \"project docs\" --max-pages 20
```

This importer reads from Notion API and emits signed internal envelopes to RelayFile so pages appear in `./.livefs`.
It is intended for local testing only.

Run fully automated live E2E (stack up, ingress seed, agent traverse/edit, ops + backend verification):

```bash
./scripts/live-e2e.sh --follow-logs
```

Run your agent against `./.livefs` to traverse/edit files.

Notes:

- `RELAYFILE_NOTION_TOKEN` is used for outbound writeback calls to Notion.
- Inbound file materialization still requires webhook envelopes (from your upstream integration or `scripts/send-internal-envelope.sh`).

Useful env vars:

- `RELAYFILE_ADDR` (default `:8080`)
- `RELAYFILE_JWT_SECRET`
- `RELAYFILE_INTERNAL_HMAC_SECRET`
- `RELAYFILE_MAX_BODY_BYTES`
- `RELAYFILE_STATE_FILE`
- `RELAYFILE_STATE_BACKEND_DSN` (`memory://`, `file:///...`, `postgres://...`)
- `RELAYFILE_BACKEND_PROFILE` (`inmemory`, `durable-local`, `production`)
- `RELAYFILE_DATA_DIR` (profile data directory, default `.relayfile`)
- `RELAYFILE_ENVELOPE_QUEUE_SIZE`
- `RELAYFILE_ENVELOPE_QUEUE_FILE`
- `RELAYFILE_WRITEBACK_QUEUE_FILE`
- `RELAYFILE_ENVELOPE_QUEUE_DSN` (`memory://`, `file:///...`, `postgres://...`)
- `RELAYFILE_WRITEBACK_QUEUE_DSN` (`memory://`, `file:///...`, `postgres://...`)
- `RELAYFILE_PRODUCTION_DSN` (required for `RELAYFILE_BACKEND_PROFILE=production`; fallback env alias: `RELAYFILE_POSTGRES_DSN`)
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
