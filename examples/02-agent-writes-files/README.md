# 02 — Agent writes files

Write files into the relayfile virtual filesystem, then read them back.

## What it shows

| Method | Purpose |
|--------|---------|
| `writeFile` | Create or update a single file with metadata |
| `writeFile` + `baseRevision` | Optimistic concurrency via If-Match |
| `RevisionConflictError` | Handle 409 conflicts when a revision is stale |
| `bulkWrite` | Atomically write multiple files in one request |
| `readFile` | Verify written content |

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
export RELAYFILE_TOKEN="ey…"   # JWT with fs:read + fs:write scopes
export WORKSPACE_ID="ws_demo"

npx tsx index.ts
```

## Key concepts

**Optimistic locking** — pass the file's current `revision` as `baseRevision`
to ensure your write only succeeds if the file hasn't changed since you read it.
Use `"*"` to create-or-overwrite without checking.

**Bulk writes** — `bulkWrite` writes multiple files in a single request.
The response tells you how many succeeded and includes per-file errors.
