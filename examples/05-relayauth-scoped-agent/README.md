# 05 — Scoped agent permissions

Demonstrate how relayfile tokens restrict agent access with path-based scopes.

## What it shows

| Concept | Purpose |
|---------|---------|
| Path-scoped tokens | `fs:read:/github/*` limits reads to one subtree |
| Write rejection | A read-only scope blocks writes with 403 |
| Cross-path rejection | Reading outside the allowed path returns 403 |
| `RelayFileApiError` | Catch and inspect permission errors |

## Prerequisites

- Docker Engine or Docker Desktop with the Compose plugin
- Node.js 18+, `tsx`

Start the local stack from the repo root before running this example:

```bash
cd docker
docker compose up --build
```

This boots the relayfile API, relayauth, and the seeded `ws_demo`
workspace used throughout the examples.

## Run

```bash
# Generate scoped tokens with relayauth
export RELAYFILE_TOKEN_SCOPED=$(relayauth sign \
  --workspace ws_demo --agent reader \
  --scope "fs:read:/github/*")

export RELAYFILE_TOKEN_ADMIN=$(relayauth sign \
  --workspace ws_demo --agent admin \
  --scope "fs:read" --scope "fs:write")

export WORKSPACE_ID="ws_demo"

npx tsx index.ts
```

## Scope format

```
plane:resource:action:path
```

| Scope | Grants |
|-------|--------|
| `fs:read` | Read all files |
| `fs:write` | Write all files |
| `fs:read:/github/*` | Read only `/github/` subtree |
| `fs:write:/reports/*` | Write only `/reports/` subtree |
| `sync:trigger` | Ingest webhooks, trigger syncs |
| `ops:read` | View operation status |

## Token claims

A relayfile JWT contains:

```json
{
  "workspace_id": "ws_demo",
  "agent_name": "reader",
  "scopes": ["fs:read:/github/*"],
  "aud": "relayfile",
  "exp": 1743300000
}
```

The server checks scopes on every request — agents can only access
what their token explicitly allows.
