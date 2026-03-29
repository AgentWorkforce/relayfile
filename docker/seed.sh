#!/bin/sh
set -e

RELAYFILE=http://relayfile:8080
RELAYAUTH=http://relayauth:9091
WS=ws_demo

rf_put() {
  correlation_id="$1"
  file_path="$2"
  content="$3"

  curl -fsS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=$file_path" \
    -H "Authorization: Bearer $TOKEN" \
    -H "X-Correlation-Id: $correlation_id" \
    -H "Content-Type: application/octet-stream" \
    -d "$content" >/dev/null
}

echo "==> Minting dev token via relayauth..."
TOKEN=$(curl -sS "$RELAYAUTH/sign" \
  -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"agent_name\":\"dev-agent\",\"scopes\":[\"fs:read\",\"fs:write\",\"sync:read\",\"ops:read\"]}" \
  | sed 's/.*"token":"\([^"]*\)".*/\1/')

echo "==> Seeding sample files into workspace $WS..."

rf_put "seed-docs-welcome" "/docs/welcome.md" '# Welcome
Your relayfile workspace is running.
Write files here and agents will see them instantly.'

rf_put "seed-github-pr" "/github/repos/demo/pulls/1/metadata.json" '{"number":1,"title":"Add quickstart","state":"open","author":"dev"}'
rf_put "seed-config-agents" "/config/agents.json" '{"agents":[{"name":"dev-agent","scopes":["fs:read","fs:write"]}]}'

echo ""
echo "=== Ready ==="
echo "  relayfile : http://localhost:${RELAYFILE_PORT:-9090}"
echo "  relayauth : http://localhost:${RELAYAUTH_PORT:-9091}"
echo "  workspace : $WS"
echo "  token     : $TOKEN"
echo ""
echo "Try it:"
echo "  curl -H \"Authorization: Bearer $TOKEN\" -H \"X-Correlation-Id: demo-tree\" http://localhost:${RELAYFILE_PORT:-9090}/v1/workspaces/$WS/fs/tree"
