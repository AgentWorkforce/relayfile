# 03 — Webhook to VFS

Ingest external webhooks into the relayfile virtual filesystem.

## What it shows

| Concept | Purpose |
|---------|---------|
| `computeCanonicalPath` | Map provider objects to deterministic file paths |
| `ingestWebhook` | Push external events (GitHub, Slack, etc.) into the VFS |
| `readFile` | Read back the ingested data as a file |

## Prerequisites

- **Docker** — needed to run the relayfile server locally (`docker compose up`)
- Node.js 18+, `tsx`

## Run

```bash
export RELAYFILE_TOKEN="ey…"   # JWT with sync:trigger + fs:read scopes
export WORKSPACE_ID="ws_demo"

npx tsx index.ts
```

## How it works

```
GitHub webhook ──► ingestWebhook ──► /github/pulls/42.json
                                         │
                   readFile ◄────────────┘
```

`computeCanonicalPath("github", "pulls", "42")` always returns
`/github/pulls/42.json` — every agent reading or writing that PR
agrees on the same path, making collaboration deterministic.

The webhook payload is queued as an **envelope**. The server deduplicates
by `delivery_id`, coalesces rapid updates, and writes the final content
to the canonical path.
