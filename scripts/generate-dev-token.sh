#!/usr/bin/env bash
set -euo pipefail

workspace_id="${1:-${RELAYFILE_WORKSPACE:-ws_live}}"
secret="${RELAYFILE_JWT_SECRET:-dev-secret}"
agent_name="${RELAYFILE_AGENT_NAME:-compose-agent}"
exp_epoch="${RELAYFILE_TOKEN_EXP:-4102444800}"

b64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

header='{"alg":"HS256","typ":"JWT"}'
payload=$(cat <<JSON
{"workspace_id":"${workspace_id}","agent_name":"${agent_name}","scopes":["fs:read","fs:write","sync:read","sync:trigger","ops:read","ops:replay","admin:read","admin:replay"],"exp":${exp_epoch},"aud":"relayfile"}
JSON
)

h=$(printf '%s' "${header}" | b64url)
p=$(printf '%s' "${payload}" | b64url)
s=$(printf '%s' "${h}.${p}" | openssl dgst -sha256 -hmac "${secret}" -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')

echo "${h}.${p}.${s}"
