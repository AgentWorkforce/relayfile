#!/usr/bin/env bash
set -euo pipefail

base_url="${RELAYFILE_BASE_URL:-http://127.0.0.1:${RELAYFILE_PORT:-8080}}"
workspace_id="${RELAYFILE_WORKSPACE:-ws_live}"
secret="${RELAYFILE_INTERNAL_HMAC_SECRET:-dev-internal-secret}"

path="${1:-/notion/LiveTest.md}"
content="${2:-# live webhook $(date -u +%Y-%m-%dT%H:%M:%SZ)}"
content_type="${RELAYFILE_CONTENT_TYPE:-text/markdown}"
object_id="${RELAYFILE_OBJECT_ID:-page_$(echo "${path}" | tr '/.' '_' | tr -cd '[:alnum:]_')}"

envelope_id="env_$(date +%s)"
delivery_id="del_$(date +%s%N)"
correlation_id="corr_env_$(date +%s)"
received_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

escape_json() {
  local raw="$1"
  raw="${raw//\\/\\\\}"
  raw="${raw//\"/\\\"}"
  raw="${raw//$'\n'/\\n}"
  raw="${raw//$'\r'/\\r}"
  raw="${raw//$'\t'/\\t}"
  printf '%s' "${raw}"
}

path_json=$(escape_json "${path}")
content_json=$(escape_json "${content}")
content_type_json=$(escape_json "${content_type}")
object_id_json=$(escape_json "${object_id}")
workspace_id_json=$(escape_json "${workspace_id}")

body=$(cat <<JSON
{"envelopeId":"${envelope_id}","workspaceId":"${workspace_id_json}","provider":"notion","deliveryId":"${delivery_id}","receivedAt":"${received_at}","payload":{"type":"notion.page.upsert","path":"${path_json}","objectId":"${object_id_json}","contentType":"${content_type_json}","content":"${content_json}"},"correlationId":"${correlation_id}"}
JSON
)

timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
signature=$(printf '%s\n%s' "${timestamp}" "${body}" | openssl dgst -sha256 -hmac "${secret}" -hex | awk '{print $2}')

curl -fsS -X POST "${base_url}/v1/internal/webhook-envelopes" \
  -H "Content-Type: application/json" \
  -H "X-Correlation-Id: ${correlation_id}" \
  -H "X-Relay-Timestamp: ${timestamp}" \
  -H "X-Relay-Signature: ${signature}" \
  --data "${body}"

echo
