#!/usr/bin/env bash
set -euo pipefail

workspace_id="${1:-${RELAYFILE_WORKSPACE:-ws_live}}"
agent_name="${RELAYFILE_AGENT_NAME:-compose-agent}"
relayauth_url="${RELAYAUTH_URL:-http://127.0.0.1:9091}"

payload=$(cat <<JSON
{"workspace_id":"${workspace_id}","agent_name":"${agent_name}","scopes":["fs:read","fs:write","sync:read","sync:trigger","ops:read","ops:replay","admin:read","admin:replay"]}
JSON
)

response=$(curl -fsS "${relayauth_url}/sign" \
  -H "Content-Type: application/json" \
  -d "${payload}")

printf '%s\n' "${response}" | sed 's/.*"token":"\([^"]*\)".*/\1/'
