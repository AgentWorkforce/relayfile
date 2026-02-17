# Relayfile

A virtual filesystem service that syncs bidirectionally with external providers (e.g. Notion). Agents read and write files through a REST API; changes are automatically written back to the source provider, and incoming webhook events from providers are ingested and applied to the local file tree.

## Architecture

```
Provider (Notion, etc.)
    ↕ webhooks / writeback
Relayfile HTTP API
    ↕
In-memory Store (optional disk persistence)
    ↕
Agent SDK clients
```

**Core components:**

- **Store** (`internal/relayfile/`) — Versioned file tree with optimistic concurrency (`If-Match` revisions), event log, operation tracking, webhook envelope ingestion with dedup/coalescing, writeback queue with retry and dead-letter handling.
- **HTTP API** (`internal/httpapi/`) — RESTful endpoints for file CRUD, event streaming, sync status, operations, and admin replay. JWT auth with scoped permissions, HMAC-signed internal webhook route, per-agent rate limiting.
- **Provider Adapters** (`internal/relayfile/adapters.go`) — Pluggable adapter interface for parsing inbound webhooks and applying outbound writebacks. Ships with a Notion adapter.
- **TypeScript SDK** (`sdk/relayfile-sdk/`) — Typed client for the Relayfile API.

## Quick Start

```bash
# Run the server
go run ./cmd/relayfile

# With configuration
RELAYFILE_ADDR=:9090 \
RELAYFILE_JWT_SECRET=my-secret \
RELAYFILE_STATE_FILE=./data/state.json \
go run ./cmd/relayfile
```

## API Overview

All workspace endpoints are under `/v1/workspaces/{workspaceId}/` and require a Bearer JWT with appropriate scopes.

| Method | Path | Scope | Description |
|--------|------|-------|-------------|
| GET | `/health` | — | Health check |
| GET | `.../fs/tree` | `fs:read` | List directory tree |
| GET | `.../fs/file` | `fs:read` | Read a file |
| PUT | `.../fs/file` | `fs:write` | Write a file (optimistic locking) |
| DELETE | `.../fs/file` | `fs:write` | Delete a file |
| GET | `.../fs/events` | `fs:read` | Paginated event log |
| GET | `.../sync/status` | `sync:read` | Provider sync health |
| GET | `.../sync/ingress` | `sync:read` | Envelope ingestion stats |
| GET | `.../sync/dead-letter` | `sync:read` | Failed envelopes |
| POST | `.../sync/refresh` | `sync:trigger` | Trigger provider re-sync |
| GET | `.../ops` | `ops:read` | List writeback operations |
| POST | `.../ops/{opId}/replay` | `ops:replay` | Retry a failed operation |
| POST | `/v1/internal/webhook-envelopes` | HMAC | Ingest provider webhook |

## Configuration

All config is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAYFILE_ADDR` | `:8080` | Listen address |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | JWT signing secret |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | `dev-internal-secret` | Webhook HMAC secret |
| `RELAYFILE_STATE_FILE` | — | Path for persistent state (optional) |
| `RELAYFILE_MAX_WRITEBACK_ATTEMPTS` | `3` | Max writeback retries |
| `RELAYFILE_MAX_ENVELOPE_ATTEMPTS` | `3` | Max envelope processing retries |
| `RELAYFILE_SUPPRESSION_WINDOW` | `2m` | Echo suppression after writeback |
| `RELAYFILE_COALESCE_WINDOW` | `3s` | Dedup window for rapid updates |
| `RELAYFILE_RATE_LIMIT_MAX` | `0` (off) | Requests per window per agent |
| `RELAYFILE_RATE_LIMIT_WINDOW` | `1m` | Rate limit window |

## Testing

```bash
go test ./...
```
