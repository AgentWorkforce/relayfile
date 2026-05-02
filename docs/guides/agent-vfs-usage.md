# Relayfile VFS — Agent Usage Guide

This guide is for agents (and the skill loaders that initialize them). It covers everything needed to discover, read, write, and safely writeback through the synced mirror.

**Context budget warning:** This guide is self-contained. Low-context agents should read it top-to-bottom without skipping sections. Every section is load-bearing.

---

## Mental Model

The Relayfile VFS is a **synced mirror** of provider APIs (GitHub, Notion, Linear, Slack) materialized as ordinary files on disk. An agent reads and writes those files normally. The sync daemon handles authentication, webhooks, and writeback to the provider.

Key facts:
- Files are ordinary local files. Use file tools, not provider APIs.
- The mirror lags the source of truth by up to 30 s (default poll interval).
- Writes are eventually propagated. A write is not committed to the provider until the sync daemon acknowledges it.
- `mtime` is local write time, not source-of-truth event time. Do not sort by `mtime`.
- The `.relay/` directory is reserved. Do not write to it.

---

## Step 1: Discover the Mount Path

Find the local mirror directory in this priority order:

### 1. Environment variable (always wins)

```bash
echo $RELAYFILE_LOCAL_DIR
```

If set, use that path. Done.

### 2. CLI status command

```bash
relayfile status --json | jq -r '.localDir'
```

For multi-workspace setups, the default workspace `localDir` is returned. To target a specific workspace:

```bash
relayfile status my-project --json | jq -r '.localDir'
```

### 3. Walk upward from CWD

Search parent directories for the first one containing `.relay/state.json`. That directory is the mount root.

### Failure case

If none of the above resolves, fail with a clear message and stop:

```
relayfile mount not found. Run 'relayfile setup' or set RELAYFILE_LOCAL_DIR.
```

Do not guess paths. Do not proceed without a confirmed mount root.

---

## Step 2: Check Sync State Before Acting

Before any non-trivial read or any write, read `.relay/state.json`:

```bash
cat <mount>/.relay/state.json
```

Example:

```json
{
  "workspaceId": "ws_abc",
  "mode": "poll",
  "lastReconcileAt": "2026-05-02T18:34:11Z",
  "lastEventAt":     "2026-05-02T18:34:08Z",
  "intervalMs": 30000,
  "providers": [
    { "provider": "notion", "status": "ready", "lagSeconds": 4,
      "deadLetteredOps": 0, "lastError": null }
  ],
  "pendingWriteback": 0,
  "pendingConflicts": 0,
  "deniedPaths": 0
}
```

**Rules:**
- If the provider you need has `status != "ready"`, wait or warn. Do not assume files are current.
- If `lagSeconds > 60`, treat data as potentially stale. Note it in your response.
- If `pendingConflicts > 0`, check `.relay/conflicts/` before writing to affected paths.
- Do not use `mtime` for ordering. Use `lastReconcileAt` and `lagSeconds`.

---

## Path Conventions

### Provider-backed files

```
<mount>/github/                                         # GitHub
<mount>/github/repos/<owner>/<repo>/pulls/<n>/
  metadata.json                                         # PR metadata (read-only)
  files.json                                            # changed files (read-only)
  reviews/<timestamp>.json                              # posted reviews (read-only)
  reviews/review.json                                   # write here to post a review

<mount>/notion/                                         # Notion pages
<mount>/notion/pages/<page-id>/content.md               # page body (check _PERMISSIONS.md)

<mount>/linear/                                         # Linear issues
<mount>/linear/issues/<issue-id>/metadata.json          # issue metadata
<mount>/linear/issues/<issue-id>/comments/new.json      # write to add a comment

<mount>/slack/                                          # Slack
<mount>/slack/channels/<channel-id>/messages/           # channel messages (read-only)
<mount>/slack/channels/<channel-id>/messages/new.json   # write to send a message
```

### Writeback rules per provider

Each provider publishes its own rules:

```
<mount>/<provider>/_PERMISSIONS.md
```

Always read `_PERMISSIONS.md` before writing to a provider subtree. It lists exactly which paths trigger writeback and the schema each writeback path expects. Writing to a read-only path will be denied by the Cloud and logged to `.relay/permissions-denied.log`.

### Reserved Relayfile metadata paths (do not write)

```
<mount>/.relay/state.json             # sync state (read-only for agents)
<mount>/.relay/conflicts/             # conflict staging area (read-only for agents)
<mount>/.relay/dead-letter/           # dead-lettered ops (read-only for agents)
<mount>/.relay/permissions-denied.log # denial log (read-only for agents)
<mount>/.relay/mount.pid              # daemon PID
<mount>/.relay/mount.log              # daemon log
```

**Do not write to any path under `.relay/`.** Do not delete or rename top-level provider directories (`github/`, `notion/`, etc.).

---

## Reading Files

Use standard file read operations. No special API needed.

```bash
cat <mount>/github/repos/acme/api/pulls/42/metadata.json
```

**When to prefer file reads over provider APIs:**
- Always. The VFS exists so agents never need provider credentials or API knowledge.
- Provider APIs require OAuth tokens the agent does not have.
- File reads are simpler, cacheable, and work offline from the last sync.

**Staleness check:** If freshness matters, verify `lagSeconds` in `state.json` for the relevant provider. If lag is acceptable, proceed. If not, run `relayfile pull <path>` to force a refresh before reading.

---

## Writing Files (Safe Edit Pattern)

Writes trigger writeback to the provider. Follow this exact sequence:

### 1. Read the current file

```bash
cat <mount>/github/repos/acme/api/pulls/42/reviews/review.json
```

Capture the current content. Note the path for later.

### 2. Check `_PERMISSIONS.md`

Verify the target path is a writeback path, not read-only. Check the required schema.

```bash
cat <mount>/github/_PERMISSIONS.md
```

### 3. Validate your payload against the schema

For JSON writeback paths, validate locally before saving. Example for a GitHub review:

```json
{
  "body": "Looks good to me.",
  "event": "APPROVE"
}
```

`event` must be one of `APPROVE`, `REQUEST_CHANGES`, `COMMENT`. Writing an invalid schema causes a schema validation error: the Cloud rejects the write, moves the file to `.relay/conflicts/<path>.invalid.<ts>`, and restores the previous version. Fix and retry from that conflict file.

### 4. Write atomically

```bash
# Write the file
cat > <mount>/github/repos/acme/api/pulls/42/reviews/review.json << 'EOF'
{"body": "Looks good to me.", "event": "APPROVE"}
EOF
```

The mount's file watcher picks up the change within 100 ms and queues the upload.

### 5. Wait for acknowledgement

Poll `.relay/state.json` until `pendingWriteback == 0` or the file appears in `.relay/conflicts/` or `.relay/dead-letter/`.

```bash
# Simple poll loop (30 s timeout)
timeout=30
elapsed=0
while [ $elapsed -lt $timeout ]; do
  pending=$(jq '.pendingWriteback' <mount>/.relay/state.json)
  conflicts=$(ls <mount>/.relay/conflicts/ 2>/dev/null | grep -c "review.json" || true)
  [ "$pending" -eq 0 ] && [ "$conflicts" -eq 0 ] && echo "ack" && break
  sleep 2
  elapsed=$((elapsed + 2))
done
```

The SDK provides `waitForWriteback(path, { timeoutMs })` for TypeScript callers.

### 6. Handle conflicts on retry

If a conflict appears:

1. Read both versions: the current path (remote) and `.relay/conflicts/<path>.<rev>.local` (your edit).
2. Merge the intent into the current file at the original path.
3. Save again (step 4).
4. Wait for ack again (step 5).
5. Do not delete the conflict file yourself — the system moves it to `resolved/` automatically after a successful write.

---

## After Writing: What Success Looks Like

A write is **not** confirmed until:
- `pendingWriteback` returns to 0, AND
- No new entry for the path appears under `.relay/conflicts/` or `.relay/dead-letter/`

A write that lands in `.relay/dead-letter/` has permanently failed. Inspect the dead-letter JSON:

```bash
cat <mount>/.relay/dead-letter/<opId>.json
```

Fields include: `path`, `code`, `message`, `attempts`, `replayUrl`. Fix the underlying issue (schema, permissions, provider auth) then replay:

```bash
relayfile ops replay <opId>
```

---

## Status Codes and Provider States

| Provider status | Meaning for agents |
|---|---|
| `pending` | Provider just connected; no files yet. Wait. |
| `cataloging` | Listing remote objects. Files will appear soon. Wait. |
| `syncing` | Files populating. Partial data. Read with caution. |
| `ready` | Fully synced. Safe to read and write. |
| `error` | Last sync attempt failed. Check `lastError`. Reads may be stale. |
| `degraded` | Sync lag > 10 min. Reads serve stale data. Note in response. |

When a provider is `cataloging` or `syncing`, do not delete local files under that subtree. The mount will not delete them either — it waits for `ready` before applying remote-delete semantics.

---

## Multi-Agent Coordination

When two agents share a mount:

- **Treat `.relay/conflicts/` as a coordination signal.** If you see a conflict on a path another agent owns, back off. Do not overwrite it.
- **Prefer disjoint paths.** Agents working on different files never conflict. Structure parallel work to use separate paths.
- **Use agent-relay channels for handoffs** when two agents need to coordinate on a shared file. One agent can post "done editing `github/.../review.json`" before the next one picks it up.
- **Do not use file locking.** The VFS does not support locks across processes. Coordinate through channels or by convention.

---

## Low-Context Agent Checklist

Paste this into a low-context agent's initial context if the full guide is too large:

```
RELAYFILE VFS QUICK REFERENCE

Mount discovery (in order):
  1. $RELAYFILE_LOCAL_DIR
  2. relayfile status --json | jq -r '.localDir'
  3. Walk upward from CWD for .relay/state.json

Before reading or writing:
  - Read .relay/state.json
  - provider.status must be "ready"
  - provider.lagSeconds should be < 60

Path rules:
  - Files are at <mount>/<provider>/...
  - Read _PERMISSIONS.md before writing to any path
  - Never write to .relay/
  - Never delete top-level provider dirs

Safe write sequence:
  1. Read current file
  2. Check _PERMISSIONS.md for schema
  3. Validate JSON payload locally
  4. Write file
  5. Poll state.json until pendingWriteback == 0
  6. If .relay/conflicts/ contains your path: merge and retry
  7. If .relay/dead-letter/ contains your path: inspect and replay

Do NOT:
  - Trust mtime for ordering (use lastReconcileAt)
  - Call provider APIs directly (use files)
  - Write to .relay/
  - Ignore lagSeconds > 60
```

---

## SDK Helpers (TypeScript)

The `@relayfile/sdk` `WorkspaceHandle` exposes these helpers:

```typescript
import { WorkspaceHandle } from "@relayfile/sdk";

const handle = await WorkspaceHandle.connect({ workspaceId: "ws_abc" });

// Read sync state
const state = await handle.readState();
// { lastReconcileAt, providers: [{ provider, status, lagSeconds }], ... }

// Wait for a provider to be ready
await handle.enforceProviderReady("notion", { timeoutMs: 60_000 });

// Write a file and wait for writeback ack
await handle.writeFile("github/repos/acme/api/pulls/42/reviews/review.json",
  JSON.stringify({ body: "LGTM!", event: "APPROVE" })
);
await handle.waitForWriteback("github/repos/acme/api/pulls/42/reviews/review.json",
  { timeoutMs: 30_000 }
);

// Refresh the workspace token (called automatically after 55 min)
await handle.refreshToken();
```

---

## Common Errors and Recovery

| Symptom | Likely cause | Fix |
|---|---|---|
| File missing from `<mount>/<provider>/` | Provider still syncing | Check `state.json` status; wait for `ready` |
| Write moves to `.relay/conflicts/*.invalid.*` | Schema validation failed | Read `_PERMISSIONS.md`, fix payload, retry |
| Write moves to `.relay/conflicts/*.local` | Concurrent edit conflict | Merge remote + local, re-save |
| Write moves to `.relay/dead-letter/` | Permanent API error | Inspect JSON, fix root cause, `relayfile ops replay <id>` |
| `lagSeconds > 60` | Webhook unhealthy or network lag | Note staleness in response; run `relayfile pull` if needed |
| `status == "error"` | Provider auth or API failure | Run `relayfile integration list` and `relayfile integration connect <provider>` |
| Permission denial in log | Path is read-only | Read `_PERMISSIONS.md` for writable paths |
