#!/bin/sh
set -e

RELAYFILE=http://relayfile:8080
RELAYAUTH=http://relayauth:9091
WS=ws_demo

echo "==> Minting dev token via relayauth..."
TOKEN=$(curl -fsS "$RELAYAUTH/sign" \
  -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"agent_name\":\"dev-agent\",\"scopes\":[\"relayfile:fs:read:*\",\"relayfile:fs:write:*\",\"relayfile:sync:read:*\",\"relayfile:ops:read:*\"]}" \
  | sed 's/.*"token":"\([^"]*\)".*/\1/')

echo "==> Seeding sample files into workspace $WS..."

curl -fsS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/docs/welcome.md" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "If-Match: 0" \
  -H "X-Correlation-Id: seed-$(date +%s)-1" \
  -d '{"contentType":"text/markdown","content":"# Welcome\nYour relayfile workspace is running.\nWrite files here and agents will see them instantly."}'

curl -fsS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/github/repos/demo/pulls/1/metadata.json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "If-Match: 0" \
  -H "X-Correlation-Id: seed-$(date +%s)-2" \
  -d '{"contentType":"application/json","content":"{\"number\":1,\"title\":\"Add quickstart\",\"state\":\"open\",\"author\":\"dev\"}"}'

curl -fsS -X PUT "$RELAYFILE/v1/workspaces/$WS/fs/file?path=/config/agents.json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "If-Match: 0" \
  -H "X-Correlation-Id: seed-$(date +%s)-3" \
  -d '{"contentType":"application/json","content":"{\"agents\":[{\"name\":\"dev-agent\",\"scopes\":[\"relayfile:fs:read:*\",\"relayfile:fs:write:*\"]}]}"}'

echo ""
echo "=== Ready ==="
echo "  relayfile : http://localhost:${RELAYFILE_PORT:-9090}"
echo "  relayauth : http://localhost:${RELAYAUTH_PORT:-9091}"
echo "  workspace : $WS"
echo "  token     : $TOKEN"
echo ""
echo "Try it:"
echo "  curl -H \"Authorization: Bearer $TOKEN\" -H \"X-Correlation-Id: quickstart-tree\" http://localhost:${RELAYFILE_PORT:-9090}/v1/workspaces/$WS/fs/tree"
