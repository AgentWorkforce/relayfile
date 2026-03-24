# RelayFile — Site Content

---

## Hero

**Headline:**
Real-time filesystem for humans and agents

**Subhead:**
A revision-controlled, programmable filesystem that syncs everywhere. Mount it locally, in the cloud, or in a sandbox — everyone sees the same files.

**CTA:**
- [Get Started](/docs/guides/getting-started) — primary
- [View on GitHub](https://github.com/anthropics/relayfile) — secondary

---

## Problem — "Files are the universal interface"

Every tool, every agent, every developer works with files. They're the one interface everything already understands.

But sharing files across machines, sandboxes, and agents is broken:

- **Git is async.** Push, pull, resolve conflicts, push again. It's a workflow, not a sync layer.
- **FUSE volumes are slow.** Network-mounted filesystems add latency to every read and write. One stall blocks everything.
- **Cloud drives have no revision control.** Google Drive and Dropbox sync files, but silently overwrite conflicts and offer no programmatic API worth building on.
- **There's no real-time, programmable filesystem that just works everywhere.**

Agents make this worse. They run in sandboxes, spin up and tear down, and need to collaborate with each other and with humans — all through files. The current tools weren't built for this.

---

## Solution — "RelayFile"

### Mount anywhere

`relayfile mount` syncs a local directory to a shared workspace. Run it on your laptop, in a Docker container, or in a cloud sandbox. Agents and humans see the same files.

```bash
# On your laptop
relayfile mount project-x ./src

# In a cloud sandbox
relayfile mount project-x ./src
```

Edit a file on one machine, it appears on the other in about a second. No git ceremony. No manual sync.

### Revision-controlled

Every write is tracked with a revision. Conflicts are detected, not silently overwritten. You get a full event history of who changed what and when.

```json
{
  "path": "/docs/design.md",
  "revision": "rev_a1b2c3",
  "origin": "agent_write",
  "timestamp": "2026-03-24T14:30:00Z"
}
```

If two writers collide, you get a clear `409 Conflict` with both revisions — not a silent last-write-wins.

### Programmable

REST API for reads, writes, and structured queries. Webhook ingestion for external sources. Writeback queues for bidirectional sync. Build workflows on top of files, not around them.

- Read and write files over HTTP
- Query files by metadata, relations, and permissions
- Subscribe to change events with cursors
- Ingest webhooks from any provider
- Push changes back to external systems via writeback

---

## Use Cases

### Multi-agent coding

Backend and frontend agents work on the same codebase simultaneously. Agent A writes an API handler; Agent B picks up the types and builds the UI component. Changes sync in real-time — no coordination layer required beyond the filesystem.

### Human + agent collaboration

Watch an agent work in real-time on your local machine. Open a file it just wrote. Edit it to course-correct. The agent picks up your change on its next read — immediately, not after a commit cycle.

```bash
# You mount the workspace
relayfile mount project-x ./src

# The agent mounts the same workspace in its sandbox
relayfile mount project-x ./src

# You both see the same files, in real-time
```

### Cross-machine development

Mount the same workspace on your laptop and your cloud dev environment. Edit locally with your favorite editor; run builds and tests in the cloud. No `git push` / `git pull` ceremony between iterations.

### Tool integration

Notion pages, GitHub files, Salesforce records, Linear tickets — all projected into one filesystem via webhook ingestion and writeback queues. Your agent reads a Notion doc the same way it reads a local markdown file. It writes back changes the same way too.

---

## How It Works

```
Developer laptop                          Cloud sandbox (Agent A)
       |                                          |
       v                                          v
  relayfile mount                           relayfile mount
       |                                          |
       +------------>  RelayFile  <---------------+
                          API
                           ^
                           |
                    Cloud sandbox (Agent B)
                           |
                      relayfile mount
```

1. Each client runs `relayfile mount`, which polls the RelayFile API for changes and pushes local edits.
2. The RelayFile server holds the canonical state — revisions, content, metadata, event history.
3. Writes use optimistic concurrency (`If-Match` with revision IDs). Conflicts are surfaced, never swallowed.
4. External systems feed in via webhook ingestion. Changes flow back out via writeback queues.

---

## API Preview

**Write a file:**

```bash
TOKEN="$(relayfile token)"

curl -X PUT \
  "https://api.relayfile.dev/v1/workspaces/ws_123/fs/file?path=/docs/design.md" \
  -H "Authorization: Bearer $TOKEN" \
  -H "If-Match: \"rev_abc\"" \
  -H "Content-Type: application/json" \
  -d '{
    "contentType": "text/markdown",
    "content": "# Updated design document\n\nNew content here."
  }'
```

**Read a file:**

```bash
curl -s \
  "https://api.relayfile.dev/v1/workspaces/ws_123/fs/file?path=/docs/design.md" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

```json
{
  "path": "/docs/design.md",
  "revision": "rev_def",
  "contentType": "text/markdown",
  "content": "# Updated design document\n\nNew content here."
}
```

**List change events:**

```bash
curl -s \
  "https://api.relayfile.dev/v1/workspaces/ws_123/fs/events?cursor=evt_100&limit=10" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

```json
{
  "events": [
    {
      "eventId": "evt_101",
      "type": "file.updated",
      "path": "/docs/design.md",
      "revision": "rev_def",
      "origin": "agent_write",
      "timestamp": "2026-03-24T14:30:00Z"
    }
  ],
  "nextCursor": "evt_101"
}
```

---

## Architecture

**RelayFile server** — a Go service exposing the filesystem-over-REST API, webhook ingestion, writeback queues, and event streams.

**Key properties:**

- **One workspace, one state** — each workspace has a single canonical state. No distributed consensus needed for reads or writes within a workspace.
- **Queue-first ingestion** — webhook payloads are accepted fast (target: p95 < 200ms) and processed asynchronously. Deduplication, coalescing, and staleness checks happen in workers, not in the request path.
- **Optimistic concurrency everywhere** — all writes carry a revision precondition. No silent overwrites, no merge heuristics, no surprise data loss.
- **Pluggable backends** — in-memory for dev, local files for durable-local, Postgres for production. Same API, same behavior.
- **Mount daemon** — `relayfile-mount` is a single Go binary. Bake it into any Docker image or run it directly. It polls for remote changes and pushes local edits with conflict detection.

**Production stack:**

- Postgres for state, envelope queues, and writeback queues
- JWT-based auth with workspace-scoped, agent-scoped tokens
- Correlation IDs across all ingress, processing, and writeback for end-to-end tracing

---

## Footer

[GitHub](https://github.com/anthropics/relayfile) · [Documentation](/docs/guides/getting-started) · [API Reference](/docs/api-reference)

Built by [Agent Workforce](https://agentworkforce.dev)
