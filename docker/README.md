# Docker Quickstart

Run relayfile + relayauth locally in under a minute.

## Start

```bash
cd docker
docker compose up --build
```

This starts:

| Service | URL | Description |
|---------|-----|-------------|
| relayfile | http://localhost:9090 | VFS API server (Go, in-memory) |
| relayauth | http://localhost:9091 | Token signing service (Node) |
| seed | (runs once) | Creates `ws_demo` workspace with sample files |

The `seed` container mints a dev token and writes three sample files. Watch its logs for the token and a ready-to-paste `curl` command.

## Usage

```bash
# Grab the token from seed logs
TOKEN=$(docker compose logs seed | grep 'token' | tail -1 | awk '{print $NF}')

# List files
curl -H "Authorization: Bearer $TOKEN" http://localhost:9090/v1/workspaces/ws_demo/fs/tree

# Read a file
curl -H "Authorization: Bearer $TOKEN" "http://localhost:9090/v1/workspaces/ws_demo/fs/file?path=/docs/welcome.md"

# Mint a new token
curl -X POST http://localhost:9091/sign \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_demo","agent_name":"my-agent","scopes":["fs:read"]}'
```

## Configuration

Copy `.env.example` to `.env` to override defaults. The in-memory backend means data resets on restart — perfect for development.

## Teardown

```bash
docker compose down
```
