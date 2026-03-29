# 06 — Writeback consumer

Poll pending writebacks and push changes back to the external provider (GitHub).

## What it shows

| Concept | Purpose |
|---------|---------|
| `listPendingWritebacks` | Fetch items waiting to be written back to a provider |
| `readFile` | Read VFS file content for the writeback payload |
| `ackWriteback` | Acknowledge success or failure after provider push |
| GitHub writeback handler | Map VFS paths to GitHub API calls (issues, PRs, comments) |

## Prerequisites

- **Docker** — needed to run the relayfile server locally (`docker compose up`)
- Node.js 18+, `tsx`

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

The consumer polls for pending writebacks, reads the file content,
dispatches to the appropriate GitHub handler based on the VFS path,
then acknowledges the result. Failed items are retried automatically
up to `MAX_WRITEBACK_ATTEMPTS` before being dead-lettered.
