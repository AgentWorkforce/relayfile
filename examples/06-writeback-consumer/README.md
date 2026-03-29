# 06 — Writeback consumer

Poll pending writebacks and push changes back to the external provider (GitHub).

## What it shows

| Concept | Purpose |
|---------|---------|
| `listPendingWritebacks` | Fetch items waiting to be written back to a provider |
| `readFile` | Read VFS file content for the writeback payload |
| `ackWriteback` | Acknowledge success or failure after provider push |
| `WritebackConsumer` | Poll relayfile, dispatch pending items, and ack outcomes |
| `GitHubWritebackHandler` | Map VFS paths to GitHub API calls (issues, PRs, comments) |

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
export RELAYFILE_TOKEN="ey…"   # JWT with fs:read + ops:read scopes
export WORKSPACE_ID="ws_demo"
export GITHUB_TOKEN="ghp_…"    # PAT with repo scope

npx tsx index.ts
```

## How it works

```
VFS file changed ──► listPendingWritebacks ──► readFile
                                                  │
                  GitHub API ◄── handler routes ◄─┘
                       │
                  ackWriteback (success / failure)
```

`WritebackConsumer` polls for pending writebacks, reads the file content,
dispatches to `GitHubWritebackHandler` based on the VFS path, then
acknowledges the result. Failed items are retried automatically up to
`MAX_WRITEBACK_ATTEMPTS` before being dead-lettered.
