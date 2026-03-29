# 01 — Agent reads files

The simplest relayfile example: connect to a workspace and read its contents.

## What it shows

| Method | Purpose |
|--------|---------|
| `listTree` | Browse the virtual filesystem tree |
| `readFile` | Fetch a single file's content and metadata |
| `queryFiles` | Search files by provider or semantic properties |

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
export RELAYFILE_TOKEN="ey…"   # JWT with fs:read scope
export WORKSPACE_ID="ws_demo"

npx tsx index.ts
```

## Expected output

```
── listTree (depth 2) ──
  📁 /github
  📄 /github/pulls/42.json  rev=r_abc123
  …

── readFile /github/pulls/42.json ──
  contentType : application/json
  revision    : r_abc123
  provider    : github
  content     : {"title":"Add auth middleware",…}…

── queryFiles (provider=github) ──
  /github/pulls/42.json  props={"status":"open"}

── queryFiles (property status=open) ──
  matched 1 file(s)
  /github/pulls/42.json

Done.
```
