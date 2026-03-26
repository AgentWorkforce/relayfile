# RelayFile API Reference

This document summarizes the current RelayFile HTTP surface with runnable `curl` examples. For the full schema, see `openapi/relayfile-v1.openapi.yaml`.

## Base Variables

Set these once for the examples below:

```bash
export RELAYFILE_BASE_URL=http://127.0.0.1:8080
export RELAYFILE_WORKSPACE=ws_live
export RELAYFILE_TOKEN="$(./scripts/generate-dev-token.sh ${RELAYFILE_WORKSPACE})"
export RELAYFILE_CORRELATION_ID="corr_$(date +%s)"
```

## Authentication Headers

Authenticated requests usually include:

```bash
-H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
-H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}"
```

For internal ingress endpoints, production deployments may require HMAC-style service authentication instead of a Bearer token. The local development examples below keep the commands simple and focus on the request shapes.

Endpoints that are intended for internal service-to-service ingress may require different auth or signing behavior in production. The examples here focus on local development and contract discovery.

## Health

### `GET /health`

Returns a simple liveness response. This endpoint does not require auth.

```bash
curl -sS "${RELAYFILE_BASE_URL}/health" | jq .
```

## Filesystem

### `GET /v1/workspaces/{workspaceId}/fs/tree`

List files and directories under a path.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/tree?path=/&depth=2" | jq .
```

### `GET /v1/workspaces/{workspaceId}/fs/file`

Read one file.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/file?path=/README.md" | jq .
```

### `PUT /v1/workspaces/{workspaceId}/fs/file`

Create or update one file.

```bash
curl -sS -X PUT \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  -H "Content-Type: application/json" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/file" \
  -d '{
    "path": "/docs/guide.md",
    "content": "# agent guide",
    "contentType": "text/markdown"
  }' | jq .
```

### `DELETE /v1/workspaces/{workspaceId}/fs/file`

Delete one file.

```bash
curl -sS -X DELETE \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/file?path=/docs/guide.md" | jq .
```

### `GET /v1/workspaces/{workspaceId}/fs/events`

Read the filesystem event feed for a workspace.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/events?path=/&limit=20" | jq .
```

### `GET /v1/workspaces/{workspaceId}/fs/query`

Run a structured metadata query.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/query?path=/documents&property.topic=investments&relation=db_investments&permission=scope:fs:read&limit=20" | jq .
```

### `POST /v1/workspaces/{workspaceId}/fs/bulk`

Write many files in one request.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  -H "Content-Type: application/json" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/bulk" \
  -d '{
    "files": [
      {
        "path": "/src/app.js",
        "content": "console.log(\"hello\");",
        "contentType": "application/javascript"
      },
      {
        "path": "/README.md",
        "content": "# demo workspace",
        "contentType": "text/markdown"
      }
    ]
  }' | jq .
```

### `GET /v1/workspaces/{workspaceId}/fs/export`

Export visible files from a workspace.

JSON export:

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/export?format=json" | jq .
```

Tar export:

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/export?format=tar" \
  -o relayfile-export.tar.gz
```

Patch export:

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/export?format=patch"
```

### `GET /v1/workspaces/{workspaceId}/fs/ws`

Open a WebSocket stream for real-time filesystem changes.

This endpoint upgrades to WebSocket rather than returning a normal REST body. It is included here because it is part of the same user-facing API surface. The token is passed as a query parameter.

```bash
wscat -c "ws://127.0.0.1:8080/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/ws?token=${RELAYFILE_TOKEN}"
```

## Sync

### `GET /v1/workspaces/{workspaceId}/sync/status`

Show workspace sync status.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/status" | jq .
```

### `GET /v1/workspaces/{workspaceId}/sync/ingress`

Inspect ingress queue and reliability counters for one workspace.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/ingress" | jq .
```

### `GET /v1/workspaces/{workspaceId}/sync/dead-letter`

List dead-letter envelopes.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/dead-letter?limit=20" | jq .
```

### `GET /v1/workspaces/{workspaceId}/sync/dead-letter/{envelopeId}`

Fetch one dead-letter envelope in detail.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/dead-letter/envelope_123" | jq .
```

### `POST /v1/workspaces/{workspaceId}/sync/dead-letter/{envelopeId}/replay`

Replay one dead-letter envelope.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/dead-letter/envelope_123/replay" | jq .
```

### `POST /v1/workspaces/{workspaceId}/sync/dead-letter/{envelopeId}/ack`

Mark one dead-letter envelope as acknowledged.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/dead-letter/envelope_123/ack" | jq .
```

### `POST /v1/workspaces/{workspaceId}/sync/refresh`

Trigger a sync refresh for the workspace.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/sync/refresh" | jq .
```

## Webhooks

### `POST /v1/workspaces/{workspaceId}/webhooks/ingest`

Submit a provider-agnostic webhook into a workspace.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: webhook_test_1" \
  -H "Content-Type: application/json" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/webhooks/ingest" \
  -d '{
    "provider": "salesforce",
    "event_type": "file.updated",
    "path": "/salesforce/Account_123",
    "data": {
      "content": "Account details",
      "contentType": "text/plain"
    },
    "delivery_id": "sf_evt_123"
  }' | jq .
```

### `POST /v1/internal/webhook-envelopes`

Submit an internal webhook envelope. In production this is intended for trusted internal callers.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  -H "Content-Type: application/json" \
  "${RELAYFILE_BASE_URL}/v1/internal/webhook-envelopes" \
  -d '{
    "workspace_id": "'"${RELAYFILE_WORKSPACE}"'",
    "provider": "internal",
    "event_type": "file.updated",
    "path": "/docs/guide.md",
    "data": {
      "content": "# internal update",
      "contentType": "text/markdown"
    },
    "delivery_id": "internal_evt_123"
  }' | jq .
```

### `GET /v1/workspaces/{workspaceId}/writeback/pending`

List outbound writeback items waiting for an external provider.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/writeback/pending" | jq .
```

### `POST /v1/workspaces/{workspaceId}/writeback/{itemId}/ack`

Acknowledge a processed writeback item.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/writeback/writeback_123/ack" | jq .
```

## Operations

### `GET /v1/workspaces/{workspaceId}/ops`

List operations for a workspace.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/ops?limit=20" | jq .
```

### `GET /v1/workspaces/{workspaceId}/ops/{opId}`

Fetch one operation.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/ops/op_123" | jq .
```

### `POST /v1/workspaces/{workspaceId}/ops/{opId}/replay`

Replay one operation.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${RELAYFILE_CORRELATION_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/ops/op_123/replay" | jq .
```

## Admin

### `GET /v1/admin/backends`

Show active backend configuration and storage types.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  "${RELAYFILE_BASE_URL}/v1/admin/backends" | jq .
```

### `GET /v1/admin/ingress`

Inspect cross-workspace ingress backlog, counters, and alerts.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  "${RELAYFILE_BASE_URL}/v1/admin/ingress?includeWorkspaces=true&includeAlerts=true&limit=20" | jq .
```

### `GET /v1/admin/sync`

Inspect cross-workspace sync health, dead-letter totals, and alert state.

```bash
curl -sS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  "${RELAYFILE_BASE_URL}/v1/admin/sync?includeWorkspaces=true&includeAlerts=true&limit=20" | jq .
```

### `POST /v1/admin/replay/envelope/{envelopeId}`

Replay an envelope from the admin plane.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  "${RELAYFILE_BASE_URL}/v1/admin/replay/envelope/envelope_123" | jq .
```

### `POST /v1/admin/replay/op/{opId}`

Replay an operation from the admin plane.

```bash
curl -sS -X POST \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  "${RELAYFILE_BASE_URL}/v1/admin/replay/op/op_123" | jq .
```

## Notes On Auth

- Most workspace and admin endpoints use Bearer token auth.
- The OpenAPI contract defines scope-aware auth such as `fs:read` and `fs:write`.
- Internal ingress may use service-to-service signing in production.
- `X-Correlation-Id` is recommended for traceability and auditability across requests.
