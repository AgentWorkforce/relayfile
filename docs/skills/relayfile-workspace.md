# Skill: relayfile-workspace

**Version:** 1.0
**Install path:** `.agents/skills/relayfile-workspace.md`
**Trigger:** Agent is working in a directory that has a `.relay/state.json` file, or `$RELAYFILE_LOCAL_DIR` is set, or the agent needs to read/write provider-backed files (GitHub PRs, Notion pages, Linear issues, Slack messages).

---

## What This Skill Provides

This skill teaches an agent how to use a Relayfile Cloud VFS synced mirror safely. It covers:

- Discovering the mount path
- Checking sync state before acting
- Reading provider-backed files instead of calling provider APIs
- Writing files through the safe edit pattern
- Handling conflicts, writeback failures, and dead-letter operations
- Multi-agent coordination conventions

---

## When to Load This Skill

Load this skill when:

- The agent's task involves reading GitHub PRs, Notion pages, Linear issues, or Slack messages
- The agent needs to write back to a provider (post a review, update an issue, send a message)
- `$RELAYFILE_LOCAL_DIR` is set in the environment
- A `.relay/state.json` file exists in or above the current working directory

Do not load this skill when working with local files unrelated to a provider.

---

## Core Rule: Prefer Files Over Provider APIs

**Always read and write through the local VFS mirror. Never call provider APIs directly.**

The VFS exists precisely so agents do not need provider credentials, OAuth tokens, or knowledge of provider-specific API shapes. A file read is simpler, reproducible, and works from the last sync even when offline.

---

## Phase 1: Discover the Mount

Find the mount root before doing anything else. Use this priority order:

```
1. $RELAYFILE_LOCAL_DIR  (always wins when set)
2. relayfile status --json | jq -r '.localDir'
3. Walk upward from CWD — first directory containing .relay/state.json
```

If none resolves, fail loudly:

```
relayfile mount not found. Run 'relayfile setup' or set RELAYFILE_LOCAL_DIR.
```

Store the resolved path as `MOUNT` for all subsequent operations.

---

## Phase 2: Read Sync State

Before any non-trivial read or any write, read `$MOUNT/.relay/state.json`.

Key fields:

| Field | What to check |
|---|---|
| `providers[*].status` | Must be `"ready"` before trusting data for that provider |
| `providers[*].lagSeconds` | Warn if > 60; do not block unless > 300 |
| `pendingWriteback` | If > 0, a previous write is in flight |
| `pendingConflicts` | If > 0, inspect `.relay/conflicts/` before writing |

**Helper — enforceProviderReady(provider):**

```typescript
// SDK
await handle.enforceProviderReady("notion", { timeoutMs: 60_000 });

// Shell equivalent
while [ "$(jq -r '.providers[] | select(.provider=="notion") | .status' $MOUNT/.relay/state.json)" != "ready" ]; do
  sleep 5
done
```

Do not skip this check. A `cataloging` or `syncing` provider has incomplete data.

---

## Phase 3: Read Files

Use standard file read operations.

```bash
# Read a GitHub PR
cat $MOUNT/github/repos/acme/api/pulls/42/metadata.json

# Read a Notion page
cat $MOUNT/notion/pages/product-roadmap/content.md

# List a subtree
ls $MOUNT/linear/issues/

# Check what's writable for a provider
cat $MOUNT/github/_PERMISSIONS.md
```

**Do not trust `mtime` for ordering.** Use `state.json` → `lastReconcileAt` and `providers[*].lagSeconds` instead.

---

## Phase 4: Write Files (Safe Edit Pattern)

Every write to a provider-backed path must follow this sequence:

### 4.1 Check _PERMISSIONS.md

```bash
cat $MOUNT/<provider>/_PERMISSIONS.md
```

Confirm the target path is declared as a writeback path (not read-only) and note the expected schema.

### 4.2 Validate payload locally

For JSON writeback paths, validate against the schema in `_PERMISSIONS.md` before saving. A schema error causes the file to land in `.relay/conflicts/<path>.invalid.<ts>` — catching it locally avoids that round-trip.

### 4.3 Read → Mutate → Write

```bash
# 1. Read current state
current=$(cat $MOUNT/github/repos/acme/api/pulls/42/reviews/review.json 2>/dev/null || echo '{}')

# 2. Compute new content
new_content='{"body":"LGTM!","event":"APPROVE"}'

# 3. Write atomically
printf '%s' "$new_content" > $MOUNT/github/repos/acme/api/pulls/42/reviews/review.json
```

The mount watcher debounces 100 ms and queues the upload automatically. Do not call any provider API.

### 4.4 Wait for writeback acknowledgement

Poll `state.json` until `pendingWriteback == 0` or the path appears in `.relay/conflicts/` or `.relay/dead-letter/`.

**SDK:**
```typescript
await handle.waitForWriteback(
  "github/repos/acme/api/pulls/42/reviews/review.json",
  { timeoutMs: 30_000 }
);
```

**Shell:**
```bash
TARGET_PATH="github/repos/acme/api/pulls/42/reviews/review.json"
TIMEOUT=30
elapsed=0
while [ $elapsed -lt $TIMEOUT ]; do
  pending=$(jq '.pendingWriteback' $MOUNT/.relay/state.json)
  in_conflict=$(ls $MOUNT/.relay/conflicts/ 2>/dev/null | grep -c "review.json" || echo 0)
  in_deadletter=$(ls $MOUNT/.relay/dead-letter/ 2>/dev/null | wc -l)
  if [ "$pending" -eq 0 ] && [ "$in_conflict" -eq 0 ]; then
    echo "Write acknowledged"
    break
  fi
  sleep 2; elapsed=$((elapsed + 2))
done
```

### 4.5 Handle conflict on retry

If a conflict appears at `.relay/conflicts/<path>.<rev>.local`:

1. Read the remote version (current file at original path).
2. Read your local version (the `.local` file in conflicts/).
3. Merge intent into the original path.
4. Write and wait for ack again (steps 4.3–4.4).
5. Do not delete the conflict file. The system moves it to `resolved/` on success.

---

## Phase 5: Handle Dead-Letter Failures

A dead-lettered write has permanently failed (schema error, authorization denied, repeated 5xx).

```bash
# Inspect
cat $MOUNT/.relay/dead-letter/<opId>.json
# Fields: opId, path, code, message, attempts, replayUrl

# Fix the root cause, then replay
relayfile ops replay <opId>

# List all dead-lettered ops
relayfile ops list
```

After a successful replay, the `.relay/dead-letter/<opId>.json` file is deleted automatically.

---

## Path Reference

```
$MOUNT/<provider>/...                  # provider-backed files (source of truth)
$MOUNT/<provider>/_PERMISSIONS.md     # writeback rules and schema for this provider
$MOUNT/.relay/state.json               # sync state (READ ONLY — do not write)
$MOUNT/.relay/conflicts/               # unresolved conflicts (do not write, do not delete)
$MOUNT/.relay/conflicts/resolved/      # cleared conflicts (audit trail)
$MOUNT/.relay/dead-letter/             # permanently failed writebacks (do not write)
$MOUNT/.relay/permissions-denied.log   # append-only denial log (read only)
```

**Never write to any path under `.relay/`.**
**Never delete or rename top-level provider directories.**

---

## Provider Catalog

| Provider ID | VFS root | Display name |
|---|---|---|
| `github` | `/github` | GitHub |
| `notion` | `/notion` | Notion |
| `linear` | `/linear` | Linear |
| `slack-sage` | `/slack` | Slack |
| `slack-my-senior-dev` | `/slack-msd` | Slack (MSD) |
| `slack-nightcto` | `/slack-nightcto` | Slack (NightCTO) |

The provider catalog is loaded from `GET /api/v1/integrations/catalog` and cached for 1 hour. Do not hardcode provider IDs in agent logic — use the catalog.

---

## Multi-Agent Conventions

When two agents share the same mount:

1. **Disjoint paths whenever possible.** Two agents writing to different files never conflict.
2. **`.relay/conflicts/` is a stop signal.** If you see a conflict on a path another agent owns, back off. Do not overwrite.
3. **Use agent-relay channels for coordination.** Post "done writing `<path>`" before another agent picks it up.
4. **No file locking.** The VFS does not support distributed locks. Coordinate by convention.

---

## Sync-Mirror Limitations (Tell Users When Relevant)

This is a synced mirror, not a POSIX kernel filesystem. State these limitations in your response when they are relevant:

- **Lag:** New remote files appear within 30 s (default) or sooner on websocket event. Not real-time.
- **File handle stability:** An editor holding a file open may see content change on disk during reconcile.
- **`mtime` is unreliable:** Reflects local write time, not remote event time.
- **Synthetic create events:** Downstream `inotify`/`fsevents` watchers see a create event on every reconcile for new content. Debounce ≥ 1 s if needed.

Mitigations are `.relay/state.json`, `relayfile status`, and `relayfile pull`.

---

## SDK Quick Reference (TypeScript)

```typescript
import { WorkspaceHandle } from "@relayfile/sdk";

// Connect to workspace (token auto-refreshes after 55 min)
const handle = await WorkspaceHandle.connect({ workspaceId: "ws_abc" });

// Read state
const state = await handle.readState();

// Enforce provider is ready
await handle.enforceProviderReady("notion", { timeoutMs: 60_000 });

// Write + wait
await handle.writeFile(
  "github/repos/acme/api/pulls/42/reviews/review.json",
  JSON.stringify({ body: "LGTM!", event: "APPROVE" })
);
await handle.waitForWriteback(
  "github/repos/acme/api/pulls/42/reviews/review.json",
  { timeoutMs: 30_000 }
);

// Refresh token manually (normally automatic)
await handle.refreshToken();
```

---

## Error Quick Reference

| Symptom | Cause | Action |
|---|---|---|
| File not under `<provider>/` | Provider not connected or still syncing | Check `state.json` status; run `relayfile integration connect <provider>` |
| Write → `.relay/conflicts/*.invalid.*` | Schema validation failed | Fix payload per `_PERMISSIONS.md`, retry |
| Write → `.relay/conflicts/*.local` | Concurrent edit conflict | Merge remote + local, re-save |
| Write → `.relay/dead-letter/` | Permanent API failure | Inspect JSON, fix root cause, `relayfile ops replay <id>` |
| `lagSeconds > 60` | Webhook degraded or network lag | Note staleness; `relayfile pull <path>` if fresh data required |
| `status == "error"` | Provider auth failure | `relayfile integration disconnect <p>` then reconnect |
| Path in `permissions-denied.log` | Read-only path | Use a writable path per `_PERMISSIONS.md` |
| Mount not found | Env or daemon not set up | `relayfile setup` or set `$RELAYFILE_LOCAL_DIR` |
