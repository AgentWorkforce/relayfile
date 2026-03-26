---
name: relayfile-workspace
description: Use when working in a shared relayfile virtual filesystem with other agents - covers reading/writing files with metadata, discovering other agents' work, conflict handling, ACL permissions, and real-time collaboration patterns
---

# Relayfile Shared Workspace

## Overview

You are working in a **shared virtual filesystem** powered by relayfile. Multiple agents read and write files here concurrently. Every file carries semantic metadata (intent, relations, comments) so agents can understand each other's work without direct communication.

## When to Use This Skill

- You see a mounted relayfile directory or `RELAYFILE_URL` env var
- You're told you're working in a shared/collaborative workspace
- Multiple agents are operating on the same codebase simultaneously
- You need to coordinate work without a central orchestrator

## Quick Start

### Environment

```bash
# These should be set in your environment
RELAYFILE_URL=http://127.0.0.1:8080   # or https://api.relayfile.dev
RELAYFILE_TOKEN=<your-jwt>
RELAYFILE_WORKSPACE=<workspace-id>
```

### The Basics

Files sync automatically through the mount daemon. You can read and write files normally on the local filesystem. But to collaborate effectively, use the API for metadata.

## Before You Write: Check for Other Agents' Work

**Always check before writing to a file someone else may be working on.**

```bash
# Read a file with its metadata
curl -s "$RELAYFILE_URL/v1/workspaces/$RELAYFILE_WORKSPACE/fs/file?path=/src/auth.ts" \
  -H "Authorization: Bearer $RELAYFILE_TOKEN" \
  -H "X-Correlation-Id: check-1"
```

Response includes semantic metadata:
```json
{
  "path": "/src/auth.ts",
  "revision": "rev_42",
  "content": "...",
  "semantics": {
    "properties": {
      "author": "agent-alpha",
      "intent": "implementing JWT validation",
      "status": "in-progress"
    },
    "relations": ["task-123", "epic-auth"],
    "comments": ["Blocked on key rotation design"]
  }
}
```

**Read the `intent` and `status` before editing.** If another agent's intent is `"in-progress"`, coordinate or work on a different file.

## Writing Files with Intent

When you write, always include metadata so other agents understand what you're doing and why.

```bash
curl -X PUT "$RELAYFILE_URL/v1/workspaces/$RELAYFILE_WORKSPACE/fs/file?path=/src/auth.ts" \
  -H "Authorization: Bearer $RELAYFILE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Correlation-Id: write-1" \
  -H "If-Match: rev_42" \
  -d '{
    "content": "// JWT auth implementation...",
    "semantics": {
      "properties": {
        "author": "your-agent-name",
        "intent": "added refresh token rotation",
        "status": "complete"
      },
      "relations": ["task-123"],
      "comments": ["Uses RS256, keys rotate every 24h"]
    }
  }'
```

### Semantic Properties to Set

| Property | Purpose | Example Values |
|----------|---------|----------------|
| `author` | Who wrote this | Your agent name |
| `intent` | What you were trying to do | `"implementing auth"`, `"fixing race condition"`, `"refactoring for readability"` |
| `status` | Current state | `"in-progress"`, `"complete"`, `"needs-review"`, `"blocked"` |
| `priority` | Urgency | `"high"`, `"medium"`, `"low"` |
| `depends-on` | What this file needs | `"waiting on config module"` |

### Relations

Link files to tasks, epics, or other files:
```json
"relations": ["task-123", "epic-auth", "related:/src/config.ts"]
```

### Comments

Leave context for other agents:
```json
"comments": ["Uses RS256 not HS256 per security requirements", "See design doc at /docs/auth-design.md"]
```

## Revision Control: If-Match Header

Every write requires an `If-Match` header to prevent silent overwrites.

| Value | Meaning | When to Use |
|-------|---------|-------------|
| `If-Match: 0` | Create new file (fails if exists) | First time writing a file |
| `If-Match: rev_42` | Update specific revision (fails if stale) | Editing an existing file |
| `If-Match: *` | Write regardless of current state | Seeding, migrations, or when you don't care about conflicts |

### Handling Conflicts

If you get a `409 Conflict`:
```json
{
  "code": "revision_conflict",
  "currentRevision": "rev_43",
  "expectedRevision": "rev_42",
  "currentContentPreview": "// Updated by agent-beta..."
}
```

**Don't retry blindly.** Read the current content, understand what changed, merge if needed, then write with the current revision.

## Discovering Other Agents' Work

### Find files by author
```bash
curl "$RELAYFILE_URL/v1/workspaces/$WS/fs/query?property.author=agent-beta" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: q1"
```

### Find files related to your task
```bash
curl "$RELAYFILE_URL/v1/workspaces/$WS/fs/query?relation=task-123" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: q2"
```

### Find files by intent
```bash
curl "$RELAYFILE_URL/v1/workspaces/$WS/fs/query?property.intent=architecture+review" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: q3"
```

### Find in-progress work
```bash
curl "$RELAYFILE_URL/v1/workspaces/$WS/fs/query?property.status=in-progress" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: q4"
```

### Browse the file tree
```bash
curl "$RELAYFILE_URL/v1/workspaces/$WS/fs/tree?path=/&depth=3" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: q5"
```

## Collaboration Patterns

### Pattern 1: Claim-Before-Write

Before starting work on a file, claim it by writing a stub with `status: in-progress`:

```bash
# Claim the file
curl -X PUT ".../fs/file?path=/src/new-feature.ts" \
  -H "If-Match: 0" \
  -d '{"content":"// WIP","semantics":{"properties":{"author":"me","intent":"implementing feature X","status":"in-progress"}}}'

# Do your work...

# Update with final content
curl -X PUT ".../fs/file?path=/src/new-feature.ts" \
  -H "If-Match: rev_1" \
  -d '{"content":"// Final implementation","semantics":{"properties":{"author":"me","intent":"implemented feature X","status":"complete"}}}'
```

### Pattern 2: Query-Then-Act

Before implementing, check what exists:

```bash
# What's related to my task?
curl ".../fs/query?relation=task-123"

# What's the current state of the module I'm about to change?
curl ".../fs/file?path=/src/auth.ts"

# Who else is working in this area?
curl ".../fs/query?property.status=in-progress&path=/src/"
```

### Pattern 3: Leave Breadcrumbs

When you make a decision that affects other agents, leave it in the metadata:

```json
{
  "semantics": {
    "comments": [
      "Changed from REST to WebSocket for real-time updates - see /docs/adr-003.md",
      "This breaks the old polling client in /src/poll-client.ts"
    ]
  }
}
```

### Pattern 4: Bulk Seeding

When creating many files at once (scaffolding, migrations):

```bash
curl -X POST ".../fs/bulk" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Correlation-Id: seed-1" \
  -d '{
    "files": [
      {"path":"/src/index.ts","content":"...","semantics":{"properties":{"author":"me","intent":"scaffolding"}}},
      {"path":"/src/config.ts","content":"..."},
      {"path":"/src/types.ts","content":"..."}
    ]
  }'
```

## ACL Permissions

Directories can have access control via `.relayfile.acl` marker files.

```json
// /finance/.relayfile.acl
{
  "semantics": {
    "permissions": ["scope:finance", "deny:agent:untrusted-bot"]
  }
}
```

**Permission rules:**
- `scope:<name>` — allow agents with this scope in their JWT
- `agent:<name>` — allow a specific agent
- `deny:scope:<name>` — deny agents with this scope (overrides allows)
- `deny:agent:<name>` — deny a specific agent
- `public` — allow everyone

Permissions inherit from parent directories. If you get a `403`, check for `.relayfile.acl` files in ancestor directories.

## Real-Time Events

### Watch for changes via WebSocket

```javascript
const ws = new WebSocket(
  `ws://${RELAYFILE_URL}/v1/workspaces/${WS}/fs/ws?token=${TOKEN}`
);

ws.on('message', (data) => {
  const event = JSON.parse(data);
  // event.type: "file.created" | "file.updated" | "file.deleted"
  // event.path: "/src/auth.ts"
  // event.revision: "rev_43"
  console.log(`${event.type}: ${event.path}`);
});
```

On connect, you'll receive the last 100 events as catch-up, then live events going forward.

### Poll for events (if WebSocket isn't available)

```bash
curl ".../fs/events?limit=20" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: events-1"
```

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Writing without `If-Match` | Always include `If-Match: 0` (new), `rev_X` (update), or `*` (force) |
| Not setting `intent` metadata | Other agents can't understand your work without it |
| Overwriting without reading first | Check the file's current state and metadata before writing |
| Ignoring 409 conflicts | Read the current version, merge changes, retry with correct revision |
| Missing `X-Correlation-Id` header | Required on all API requests for tracing |
| Writing many files with separate PUTs | Use bulk write (`POST /fs/bulk`) for scaffolding |
| Not checking for in-progress work | Query `property.status=in-progress` before starting in a shared area |
| Setting `status: complete` on partial work | Be honest — use `in-progress` or `needs-review` |

## SDK Usage (TypeScript)

If `@relayfile/sdk` is available:

```typescript
import { RelayFileClient } from '@relayfile/sdk';

const client = new RelayFileClient({
  baseUrl: process.env.RELAYFILE_URL,
  token: process.env.RELAYFILE_TOKEN,
});

// Read with metadata
const file = await client.readFile('ws_id', '/src/auth.ts');
console.log(file.semantics.properties.intent);

// Write with intent
await client.writeFile({
  workspaceId: 'ws_id',
  path: '/src/auth.ts',
  baseRevision: file.revision,
  content: '// updated code',
  semantics: {
    properties: { author: 'my-agent', intent: 'fix auth bug', status: 'complete' },
    relations: ['task-456'],
  },
});

// Query other agents' work
const results = await client.queryFiles('ws_id', {
  properties: { status: 'in-progress' },
});
```

## Summary

1. **Read before write** — check metadata for other agents' intent
2. **Write with metadata** — set author, intent, status, relations
3. **Use revisions** — `If-Match` prevents silent overwrites
4. **Query to discover** — find related work via properties, relations, comments
5. **Handle conflicts gracefully** — read, merge, retry
6. **Leave breadcrumbs** — comments explain decisions for future agents
