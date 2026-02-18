# Production Profile Validation Runbook

This runbook validates RelayFile startup and behavior with
`RELAYFILE_BACKEND_PROFILE=production` backed by Postgres.

## Prerequisites

1. Postgres is reachable.
2. `RELAYFILE_PRODUCTION_DSN` (or `RELAYFILE_POSTGRES_DSN`) is set.
3. JWT secret and internal HMAC secret are set for non-dev testing.

Example DSN:

```bash
export RELAYFILE_PRODUCTION_DSN='postgres://localhost:5438/relayfile?sslmode=disable'
```

## Boot Validation

1. Start service:

```bash
RELAYFILE_BACKEND_PROFILE=production \
go run ./cmd/relayfile
```

2. Health probe:

```bash
curl -sS http://127.0.0.1:8080/health | jq .
```

Expected: `{"status":"ok"}`.

3. Backend wiring probe:

```bash
TOKEN="$(./scripts/generate-dev-token.sh ws_live)"
curl -sS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_backend_$(date +%s)" \
  http://127.0.0.1:8080/v1/admin/backends | jq .
```

Expected:
- `backendProfile` is `production`
- `stateBackend` is `postgres`
- both queue backends are `postgres`

## Functional Validation

1. Ingest an internal envelope:

```bash
./scripts/send-internal-envelope.sh ws_live notion
```

2. Confirm files appear:

```bash
curl -sS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_tree_$(date +%s)" \
  "http://127.0.0.1:8080/v1/workspaces/ws_live/fs/tree?path=/&depth=3" | jq '.entries | length'
```

3. Query semantics surface:

```bash
curl -sS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_query_$(date +%s)" \
  "http://127.0.0.1:8080/v1/workspaces/ws_live/fs/query?path=/notion&limit=20" | jq '.items | length'
```

4. Write and confirm operation:

```bash
FILE_PATH="/notion/demo.md"
BASE="0"
WRITE=$(curl -sS -X PUT \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_write_$(date +%s)" \
  -H "If-Match: ${BASE}" \
  -H "Content-Type: application/json" \
  -d '{"contentType":"text/markdown","content":"hello from production validation"}' \
  "http://127.0.0.1:8080/v1/workspaces/ws_live/fs/file?path=${FILE_PATH}")
echo "$WRITE" | jq .
OP_ID=$(echo "$WRITE" | jq -r '.opId')
curl -sS \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "X-Correlation-Id: corr_op_$(date +%s)" \
  "http://127.0.0.1:8080/v1/workspaces/ws_live/ops/${OP_ID}" | jq .
```

Expected: operation reaches `succeeded` or `dead_lettered` with explicit error.

## Failure/Recovery Drills

1. DB interruption:
1. stop Postgres
2. call `/v1/admin/backends` and check queue depth behavior and errors in service logs
3. restart Postgres and confirm service recovers

2. Restart persistence:
1. ingest envelopes
2. restart RelayFile
3. verify pending operations/envelopes are still present via admin endpoints

3. Dead-letter replay:
1. trigger a writeback failure
2. inspect `/v1/workspaces/{workspaceId}/sync/dead-letter`
3. call replay/ack endpoints and verify state changes

## Pass Criteria

1. Service starts with `production` profile and reports Postgres backends.
2. Filesystem reads/writes, query, and ops endpoints are functional.
3. RelayFile recovers after Postgres interruption and process restart.
4. Dead-letter replay/ack paths behave deterministically.
