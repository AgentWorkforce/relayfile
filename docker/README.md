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
| relayauth | http://localhost:9091 | RS256 token signing service and JWKS endpoint (Node) |
| seed | (runs once) | Creates `ws_demo` workspace with sample files |

The `seed` container mints a dev token and writes three sample files. Watch its logs for the token and a ready-to-paste `curl` command.

## Usage

```bash
# Grab the token from seed logs
TOKEN=$(docker compose logs seed | grep 'token' | tail -1 | awk '{print $NF}')

# List files
curl -H "Authorization: Bearer $TOKEN" \
  -H "X-Correlation-Id: docker-tree" \
  http://localhost:9090/v1/workspaces/ws_demo/fs/tree

# Read a file
curl -H "Authorization: Bearer $TOKEN" \
  -H "X-Correlation-Id: docker-read" \
  "http://localhost:9090/v1/workspaces/ws_demo/fs/file?path=/docs/welcome.md"

# Mint a new token
curl -X POST http://localhost:9091/sign \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_demo","agent_name":"my-agent","scopes":["fs:read"]}'
```

## Configuration

Copy `.env.example` to `.env` to override defaults. The in-memory backend means data resets on restart — perfect for development.

The default stack verifies tokens with the bundled `relayauth` dev issuer via
`RELAYAUTH_JWKS_URL`. To run relayfile against relaycontrol token
introspection instead (no RelayAuth), set these on the `relayfile` service; a
commented-out block is in `docker-compose.yml`:

| Variable | Purpose |
|---|---|
| `RELAYFILE_AUTH_MODE` | `jwks` (default), `introspection` or `dual` (`dual` requires `RELAYAUTH_JWKS_URL`). |
| `RELAYCONTROL_INTERNAL_URL` | relaycontrol base URL; `/internal/v1/auth/introspect` is appended. |
| `RELAYCONTROL_INTROSPECT_SECRET` or `RELAYCONTROL_INTROSPECT_SECRET_FILE` | Introspection service credential (exactly one). |
| `RELAYFILE_PUBLIC_ORIGIN` | relayfile's public origin, sent as `aud`. |
| `RELAYFILE_INTROSPECTION_TIMEOUT` | Per-call timeout (default `3s`). |
| `RELAYFILE_INTROSPECTION_CACHE_TTL` | Positive cache TTL (default and cap `60s`; `0` disables). |

relaycontrol v1 desktop-user tokens carry no `fs:*` scopes, so relayfile
answers them with 403 until the relayfile-scoped token class exists. Details:
[`docs/integrations/SELF-HOST.md`](../docs/integrations/SELF-HOST.md#auth-without-relayauth-relaycontrol-introspection).

## Teardown

```bash
docker compose down
```
