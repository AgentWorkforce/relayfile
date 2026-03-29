# 04 — Realtime events

Watch for file changes in a workspace using event polling.

## What it shows

| Concept | Purpose |
|---------|---------|
| `getEvents` | Fetch filesystem events with cursor-based pagination |
| Cursor tracking | Resume from where you left off — never miss an event |
| Provider filtering | Watch only events from a specific provider |

## Run

```bash
export RELAYFILE_TOKEN="ey…"   # JWT with fs:read scope
export WORKSPACE_ID="ws_demo"

npx tsx index.ts
```

## Event types

| Type | Trigger |
|------|---------|
| `file.created` | New file written or ingested |
| `file.updated` | Existing file content changed |
| `file.deleted` | File removed |
| `dir.created` | New directory created |
| `dir.deleted` | Directory removed |
| `sync.error` | Provider sync failure |

## Polling pattern

```typescript
let cursor: string | undefined;

while (true) {
  const feed = await client.getEvents(workspaceId, { cursor, limit: 50 });
  for (const evt of feed.events) {
    // handle event
  }
  cursor = feed.nextCursor;
  await sleep(2000);
}
```

Save `cursor` to disk if your agent restarts — you'll pick up exactly
where you left off with no duplicates.
