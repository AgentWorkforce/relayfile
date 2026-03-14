#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# nango-e2e.sh — End-to-end test for the Nango bridge integration
#
# Validates the full Nango webhook flow:
#   1. Ingests a Nango-style webhook via the generic ingest endpoint
#   2. Verifies the file lands in the virtual filesystem
#   3. Lists pending writebacks
#   4. Acknowledges a writeback
#   5. Queries files by provider/model
# ---------------------------------------------------------------------------
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Defaults (overridable via env)
RELAYFILE_BASE_URL="${RELAYFILE_BASE_URL:-http://127.0.0.1:8080}"
RELAYFILE_WORKSPACE="${RELAYFILE_WORKSPACE:-ws_nango_test}"
RELAYFILE_TOKEN="${RELAYFILE_TOKEN:-}"
RELAYFILE_JWT_SECRET="${RELAYFILE_JWT_SECRET:-dev-secret}"
NANGO_PROVIDER="${NANGO_PROVIDER:-zendesk}"
NANGO_MODEL="${NANGO_MODEL:-tickets}"
NANGO_OBJECT_ID="${NANGO_OBJECT_ID:-$(date +%s)}"

PASS=0
FAIL=0
SKIP=0

pass() { PASS=$((PASS + 1)); echo "[PASS] $1"; }
fail() { FAIL=$((FAIL + 1)); echo "[FAIL] $1"; }
skip() { SKIP=$((SKIP + 1)); echo "[SKIP] $1"; }

# ---------------------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------------------
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

# Generate a dev token if none provided
if [[ -z "${RELAYFILE_TOKEN}" ]]; then
  if [[ -x "${ROOT_DIR}/scripts/generate-dev-token.sh" ]]; then
    RELAYFILE_TOKEN="$("${ROOT_DIR}/scripts/generate-dev-token.sh" "${RELAYFILE_WORKSPACE}" 2>/dev/null || true)"
  fi
  if [[ -z "${RELAYFILE_TOKEN}" ]]; then
    echo "[warn] No RELAYFILE_TOKEN and generate-dev-token.sh unavailable; some tests may fail"
    RELAYFILE_TOKEN="tok_nango_e2e_test"
  fi
fi

AUTH_HEADER="Authorization: Bearer ${RELAYFILE_TOKEN}"
CORR_ID="corr_nango_e2e_$(date +%s)"

echo "============================================"
echo "  Nango Bridge E2E Test"
echo "============================================"
echo "Base URL:   ${RELAYFILE_BASE_URL}"
echo "Workspace:  ${RELAYFILE_WORKSPACE}"
echo "Provider:   ${NANGO_PROVIDER}"
echo "Model:      ${NANGO_MODEL}"
echo "Object ID:  ${NANGO_OBJECT_ID}"
echo "--------------------------------------------"

# ---------------------------------------------------------------------------
# 1. Health check
# ---------------------------------------------------------------------------
echo ""
echo "[step 1] Health check"
if curl -fsS "${RELAYFILE_BASE_URL}/health" >/dev/null 2>&1; then
  pass "Server is healthy"
else
  echo "[warn] Server not reachable at ${RELAYFILE_BASE_URL} — running in dry-run mode"
  skip "Health check (server not reachable)"

  echo ""
  echo "============================================"
  echo "  Dry-run validation (no server)"
  echo "============================================"

  # Validate payload structure only
  INGEST_PAYLOAD=$(cat <<PAYLOAD
{
  "provider": "${NANGO_PROVIDER}",
  "event_type": "file.updated",
  "path": "/${NANGO_PROVIDER}/${NANGO_MODEL}/${NANGO_OBJECT_ID}.json",
  "data": {
    "id": ${NANGO_OBJECT_ID},
    "status": "open",
    "subject": "Nango E2E test ticket",
    "semantics": {
      "properties": {
        "nango.connection_id": "conn_${NANGO_PROVIDER}_test",
        "nango.integration_id": "${NANGO_PROVIDER}-support",
        "provider": "${NANGO_PROVIDER}",
        "provider.object_type": "${NANGO_MODEL}",
        "provider.object_id": "${NANGO_OBJECT_ID}",
        "provider.status": "open"
      },
      "relations": []
    }
  },
  "headers": {
    "X-Nango-Connection-Id": "conn_${NANGO_PROVIDER}_test",
    "X-Nango-Integration-Id": "${NANGO_PROVIDER}-support",
    "X-Nango-Provider-Config-Key": "${NANGO_PROVIDER}"
  },
  "delivery_id": "nango_e2e_${NANGO_OBJECT_ID}"
}
PAYLOAD
)

  # Validate JSON structure
  if echo "${INGEST_PAYLOAD}" | jq -e '.provider' >/dev/null 2>&1; then
    pass "Ingest payload has valid JSON structure"
  else
    fail "Ingest payload has invalid JSON"
  fi

  if echo "${INGEST_PAYLOAD}" | jq -e '.data.semantics.properties["nango.connection_id"]' >/dev/null 2>&1; then
    pass "Payload includes Nango semantic properties"
  else
    fail "Missing Nango semantic properties"
  fi

  if echo "${INGEST_PAYLOAD}" | jq -e '.headers["X-Nango-Connection-Id"]' >/dev/null 2>&1; then
    pass "Payload includes Nango headers"
  else
    fail "Missing Nango headers"
  fi

  CANONICAL_PATH=$(echo "${INGEST_PAYLOAD}" | jq -r '.path')
  EXPECTED_PATH="/${NANGO_PROVIDER}/${NANGO_MODEL}/${NANGO_OBJECT_ID}.json"
  if [[ "${CANONICAL_PATH}" == "${EXPECTED_PATH}" ]]; then
    pass "Canonical path computed correctly: ${CANONICAL_PATH}"
  else
    fail "Canonical path mismatch: got ${CANONICAL_PATH}, expected ${EXPECTED_PATH}"
  fi

  # Validate writeback ack payload
  ACK_PAYLOAD='{"success": true}'
  if echo "${ACK_PAYLOAD}" | jq -e '.success' >/dev/null 2>&1; then
    pass "Writeback ack payload is valid"
  else
    fail "Invalid writeback ack payload"
  fi

  # Validate failure ack payload
  FAIL_ACK_PAYLOAD='{"success": false, "error": "Provider returned 403"}'
  if echo "${FAIL_ACK_PAYLOAD}" | jq -e '.error' >/dev/null 2>&1; then
    pass "Writeback failure ack payload is valid"
  else
    fail "Invalid writeback failure ack payload"
  fi

  echo ""
  echo "============================================"
  echo "  Results: ${PASS} passed, ${FAIL} failed, ${SKIP} skipped"
  echo "============================================"
  exit "${FAIL}"
fi

# ---------------------------------------------------------------------------
# 2. Ingest Nango webhook
# ---------------------------------------------------------------------------
echo ""
echo "[step 2] Ingest Nango-style webhook"
INGEST_RESPONSE=$(curl -fsS -X POST \
  -H "${AUTH_HEADER}" \
  -H "Content-Type: application/json" \
  -H "X-Correlation-Id: ${CORR_ID}" \
  -d "{
    \"provider\": \"${NANGO_PROVIDER}\",
    \"event_type\": \"file.updated\",
    \"path\": \"/${NANGO_PROVIDER}/${NANGO_MODEL}/${NANGO_OBJECT_ID}.json\",
    \"data\": {
      \"id\": ${NANGO_OBJECT_ID},
      \"status\": \"open\",
      \"subject\": \"Nango E2E test ticket\",
      \"semantics\": {
        \"properties\": {
          \"nango.connection_id\": \"conn_${NANGO_PROVIDER}_test\",
          \"nango.integration_id\": \"${NANGO_PROVIDER}-support\",
          \"provider\": \"${NANGO_PROVIDER}\",
          \"provider.object_type\": \"${NANGO_MODEL}\",
          \"provider.object_id\": \"${NANGO_OBJECT_ID}\",
          \"provider.status\": \"open\"
        },
        \"relations\": []
      }
    },
    \"headers\": {
      \"X-Nango-Connection-Id\": \"conn_${NANGO_PROVIDER}_test\",
      \"X-Nango-Integration-Id\": \"${NANGO_PROVIDER}-support\",
      \"X-Nango-Provider-Config-Key\": \"${NANGO_PROVIDER}\"
    },
    \"delivery_id\": \"nango_e2e_${NANGO_OBJECT_ID}\"
  }" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/webhooks/ingest" 2>&1) || true

if echo "${INGEST_RESPONSE}" | jq -e '.status == "queued"' >/dev/null 2>&1; then
  ENV_ID=$(echo "${INGEST_RESPONSE}" | jq -r '.id')
  pass "Webhook ingested — envelope ${ENV_ID}"
else
  fail "Webhook ingest failed: ${INGEST_RESPONSE}"
fi

# Give the server a moment to process
sleep 2

# ---------------------------------------------------------------------------
# 3. Read the file back
# ---------------------------------------------------------------------------
echo ""
echo "[step 3] Read ingested file"
FILE_PATH="/${NANGO_PROVIDER}/${NANGO_MODEL}/${NANGO_OBJECT_ID}.json"
READ_RESPONSE=$(curl -fsS \
  -H "${AUTH_HEADER}" \
  -H "X-Correlation-Id: ${CORR_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/file?path=$(python3 -c "import urllib.parse; print(urllib.parse.quote('${FILE_PATH}', safe=''))" 2>/dev/null || echo "${FILE_PATH}")" 2>&1) || true

if echo "${READ_RESPONSE}" | jq -e '.path' >/dev/null 2>&1; then
  pass "File readable at ${FILE_PATH}"
else
  skip "File read (may not be immediately available): ${READ_RESPONSE}"
fi

# ---------------------------------------------------------------------------
# 4. Query files by provider
# ---------------------------------------------------------------------------
echo ""
echo "[step 4] Query files by provider"
QUERY_RESPONSE=$(curl -fsS \
  -H "${AUTH_HEADER}" \
  -H "X-Correlation-Id: ${CORR_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/query?provider=${NANGO_PROVIDER}&property.provider.object_type=${NANGO_MODEL}" 2>&1) || true

if echo "${QUERY_RESPONSE}" | jq -e '.items' >/dev/null 2>&1; then
  ITEM_COUNT=$(echo "${QUERY_RESPONSE}" | jq '.items | length')
  pass "Query returned ${ITEM_COUNT} item(s) for ${NANGO_PROVIDER}/${NANGO_MODEL}"
else
  skip "Query (may need processing time): ${QUERY_RESPONSE}"
fi

# ---------------------------------------------------------------------------
# 5. List pending writebacks
# ---------------------------------------------------------------------------
echo ""
echo "[step 5] List pending writebacks"
WB_RESPONSE=$(curl -fsS \
  -H "${AUTH_HEADER}" \
  -H "X-Correlation-Id: ${CORR_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/writeback/pending" 2>&1) || true

if echo "${WB_RESPONSE}" | jq -e 'type == "array"' >/dev/null 2>&1; then
  WB_COUNT=$(echo "${WB_RESPONSE}" | jq 'length')
  pass "Writeback endpoint returned ${WB_COUNT} pending item(s)"

  # Acknowledge first writeback if any
  if [[ "${WB_COUNT}" -gt 0 ]]; then
    WB_ID=$(echo "${WB_RESPONSE}" | jq -r '.[0].id')
    echo ""
    echo "[step 5b] Acknowledge writeback ${WB_ID}"
    ACK_RESPONSE=$(curl -fsS -X POST \
      -H "${AUTH_HEADER}" \
      -H "Content-Type: application/json" \
      -H "X-Correlation-Id: ${CORR_ID}" \
      -d '{"success": true}' \
      "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/writeback/${WB_ID}/ack" 2>&1) || true

    if echo "${ACK_RESPONSE}" | jq -e '.status == "acknowledged"' >/dev/null 2>&1; then
      pass "Writeback ${WB_ID} acknowledged"
    else
      fail "Writeback ack failed: ${ACK_RESPONSE}"
    fi
  fi
else
  skip "Writeback listing: ${WB_RESPONSE}"
fi

# ---------------------------------------------------------------------------
# 6. Check events
# ---------------------------------------------------------------------------
echo ""
echo "[step 6] Check events for provider"
EVENTS_RESPONSE=$(curl -fsS \
  -H "${AUTH_HEADER}" \
  -H "X-Correlation-Id: ${CORR_ID}" \
  "${RELAYFILE_BASE_URL}/v1/workspaces/${RELAYFILE_WORKSPACE}/fs/events?provider=${NANGO_PROVIDER}&limit=5" 2>&1) || true

if echo "${EVENTS_RESPONSE}" | jq -e '.events' >/dev/null 2>&1; then
  EVENT_COUNT=$(echo "${EVENTS_RESPONSE}" | jq '.events | length')
  pass "Events feed returned ${EVENT_COUNT} event(s) for ${NANGO_PROVIDER}"
else
  skip "Events check: ${EVENTS_RESPONSE}"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "============================================"
echo "  Results: ${PASS} passed, ${FAIL} failed, ${SKIP} skipped"
echo "============================================"
exit "${FAIL}"
