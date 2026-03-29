#!/bin/sh
set -e

RELAYFILE=http://relayfile:8080
RELAYAUTH=http://relayauth:9091
WS=ws_demo

echo "==> Minting dev token via relayauth..."
TOKEN=$(curl -sS "$RELAYAUTH/sign" \
  -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"agent_name\":\"dev-agent\",\"scopes\":[\"fs:read\",\"fs:write\",\"sync:read\",\"ops:read\"]}" \
  | sed 's/.*"token":"\([^"]*\)".*/\1/')

echo "==> Seeding sample files into workspace $WS..."

curl -sS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/docs/welcome.md" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/octet-stream" \
  -d '# Welcome
Your relayfile workspace is running.
Write files here and agents will see them instantly.'

curl -sS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/github/repos/demo/pulls/1/metadata.json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/octet-stream" \
  -d '{"number":1,"title":"Add quickstart","state":"open","author":"dev"}'

curl -sS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/config/agents.json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/octet-stream" \
  -d '{"agents":[{"name":"dev-agent","scopes":["fs:read","fs:write"]}]}'

echo ""
echo "=== Ready ==="
echo "  relayfile : http://localhost:${RELAYFILE_PORT:-9090}"
echo "  relayauth : http://localhost:${RELAYAUTH_PORT:-9091}"
echo "  workspace : $WS"
echo "  token     : $TOKEN"
echo ""
echo "Try it:"
echo "  curl -H \"Authorization: Bearer $TOKEN\" http://localhost:${RELAYFILE_PORT:-9090}/v1/workspaces/$WS/fs/tree"
