# Relayfile Cloud VFS — Human Setup Guide

This guide covers everything a human operator needs: first-time setup, connecting additional integrations, checking sync status, stopping and restarting the daemon, and diagnosing problems.

## What You Are Getting

When you run `relayfile setup` (or just `relayfile`), you get a **synced mirror** — not a kernel filesystem. Ordinary files appear under a local directory of your choice. A background loop keeps them in sync with the cloud and with provider APIs (GitHub, Notion, Linear, Slack). You can read and write those files with any tool that knows how to use files.

The mirror has real limitations. Read [Known Limitations](#known-limitations) before relying on it for time-sensitive or high-volume writes.

---

## First-Time Setup

### Interactive (recommended for humans)

```bash
relayfile
```

This runs the full setup wizard:

1. Opens a browser to sign in to Relayfile Cloud.
2. Prompts for a workspace name (default: `relayfile-<timestamp>`).
3. Prompts for an integration provider (GitHub, Notion, Linear, Slack, or none).
4. Prompts for a local directory (default: `./relayfile-mount`).
5. Opens the integration OAuth consent page.
6. Waits for the initial sync to complete, showing live progress.
7. Starts the sync loop in the foreground.

Press `Ctrl+C` at any time to stop. The local files remain; re-run `relayfile` or `relayfile mount <workspace> <dir>` to resume.

### Non-interactive / CI

```bash
relayfile setup \
  --cloud-token "$RELAYFILE_CLOUD_TOKEN" \
  --provider github \
  --workspace my-project \
  --local-dir ./relayfile-mount \
  --no-open \
  --once
```

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-token` | `$RELAYFILE_CLOUD_TOKEN` | Skip browser login |
| `--provider` | prompted | `github`, `notion`, `linear`, `slack`, or `none` |
| `--workspace` | prompted | Workspace display name |
| `--local-dir` | `./relayfile-mount` | Local mirror directory |
| `--no-open` | false | Print URLs instead of opening a browser |
| `--skip-mount` | false | Finish setup without starting the sync loop |
| `--once` | false | Run one sync cycle then exit (CI/probe use) |
| `--cloud-api-url` | `https://agentrelay.com/cloud` | Cloud control-plane URL |
| `--login-timeout` | `5m` | OAuth callback wait |
| `--connect-timeout` | `5m` | Integration readiness wait |

### Re-running setup (idempotent)

You can safely re-run `relayfile setup` with the same `--workspace` name. It reuses the existing workspace ID, refreshes the access token, and skips the integration connect step if the provider is already connected.

### Exit codes

| Code | Meaning |
|-----:|---------|
| 0 | Setup complete |
| 10 | Cloud login failed (state mismatch, OAuth error) |
| 11 | Cloud login timed out |
| 20 | Workspace create/join failed |
| 30 | Integration connect failed |
| 31 | Integration not ready before deadline |
| 40 | Initial sync not ready before deadline |
| 50 | Mount initialization failed |

Exit 40 is not fatal to the workspace — sync continues in the background. Re-attach with:

```bash
relayfile mount <workspace-id> ./relayfile-mount
```

---

## Adding More Integrations

After the first setup you can connect additional providers without remounting.

```bash
# Connect Notion
relayfile integration connect notion

# Connect a specific Slack variant
relayfile integration connect slack

# Connect without opening a browser (print the URL instead)
relayfile integration connect linear --no-open

# Specify a non-default workspace
relayfile integration connect github --workspace my-other-project
```

New provider files appear under `<local-dir>/<provider>/` within ~30 seconds of the OAuth flow completing (sooner if a websocket invalidation fires).

### List connected integrations

```bash
relayfile integration list
relayfile integration list --json
```

Output:

```
provider     status    lag     last event
github       ready     2s      just now
notion       syncing   18s     5s ago
linear       error     —       401 invalid_grant (3 min ago)
```

### Disconnect an integration

```bash
relayfile integration disconnect notion
relayfile integration disconnect notion --yes   # skip confirmation
```

Disconnecting removes `<local-dir>/notion/` from the mirror and leaves a marker at `<local-dir>/.relay/disconnected/notion.json`. The integration can be reconnected at any time.

---

## Checking Status

```bash
relayfile status
relayfile status my-project
relayfile status --json          # machine-readable
```

Example output:

```
workspace ws_abc (my-project)   mode: poll   lag: 4s
  github       ready    2,400 files    last event 2s ago
  notion       ready      214 files    last event 4s ago
  linear       error    last error: 401 invalid_grant (3 min ago)

pending writebacks: 0    conflicts: 0    denied: 12
  see .relay/permissions-denied.log for denied paths
```

Status reads from both the Cloud `/sync` endpoint and the local `.relay/state.json` file so it works even when offline (showing the last known state).

### Webhook health warning

If a provider's webhook stops delivering events, status shows:

```
notion webhook unhealthy — falling back to periodic sync (lag 78s)
```

This is not an error. Poll sync continues; the provider will recover its webhook independently.

---

## Starting and Stopping the Sync Loop

### Foreground (default)

```bash
relayfile mount my-project ./relayfile-mount
```

Ctrl+C flushes pending writes (≤5 s), persists `.relay/state.json`, and exits cleanly.

### Background daemon

```bash
relayfile mount --background my-project ./relayfile-mount
```

- Detaches from the terminal.
- Writes PID to `./relayfile-mount/.relay/mount.pid`.
- Rotates logs at `./relayfile-mount/.relay/mount.log` (10 MB × 3 files).

### Stopping a background daemon

```bash
relayfile stop my-project
```

Or signal directly:

```bash
kill $(cat ./relayfile-mount/.relay/mount.pid)
```

### Restarting

```bash
relayfile stop my-project
relayfile mount --background my-project ./relayfile-mount
```

### Single sync cycle (CI/probe)

```bash
relayfile mount --once my-project ./relayfile-mount
```

Reconciles once and exits. Useful in pipelines that need a fresh snapshot before a job runs.

---

## Force-Refreshing Files

Force a full reconcile of a subtree, discarding stale local copies:

```bash
relayfile pull                          # entire workspace
relayfile pull notion/Docs/Project.md  # single file
relayfile pull github/repos/           # subtree
```

Files you have locally modified (dirty) are never discarded by `pull`. If a remote change conflicts with your local edit, `pull` moves your edit to `.relay/conflicts/` and refreshes the file from the remote.

---

## Checking Permissions

See which paths are writable for each provider:

```bash
relayfile permissions
relayfile permissions github/repos/acme/api/pulls/
```

Each provider also publishes its own rules at:

```
<local-dir>/<provider>/_PERMISSIONS.md
```

Read that file to understand exactly what paths the agent (and you) can write to.

---

## Dead-Letter Operations

When a writeback fails permanently (schema violation, authorization error, repeated 5xx), it lands in the dead-letter queue.

```bash
# List failed operations
relayfile ops list

# Replay one after fixing the underlying issue
relayfile ops replay op_abc123

# Replay all
relayfile ops list --json | jq -r '.[].opId' | xargs -I{} relayfile ops replay {}
```

Dead-lettered ops are also visible as JSON files at:

```
<local-dir>/.relay/dead-letter/<opId>.json
```

---

## Conflict Resolution

When two writers edit the same file before a sync cycle completes, the system detects a conflict:

1. Your local edit is saved verbatim to `.relay/conflicts/<path>.<rev>.local`.
2. The remote version replaces the original path.
3. The CLI prints: `conflict at notion/Docs/A.md — your edit saved at .relay/conflicts/…`

To resolve:

1. Read both versions (the original path and `.relay/conflicts/…`).
2. Edit the original path to reflect the merged intent.
3. Save. The next sync cycle uploads your merge against the new revision.
4. Once acknowledged, the conflict file moves to `.relay/conflicts/resolved/` automatically.

---

## Known Limitations

The sync loop is a **synced mirror**, not a POSIX kernel filesystem. Differences to keep in mind:

- **File handles are not stable.** If your editor holds a file open, the contents may change on disk during the next reconcile. Re-open the file to see updates.
- **`mtime` is local write time, not source-of-truth time.** Use `.relay/state.json → lastReconcileAt` and `lagSeconds` for ordering decisions.
- **Directory listings can be briefly stale.** A new file created remotely may not appear locally for up to 30 s (or until a websocket event fires).
- **`inotify`/`fsevents` watchers will see synthetic create events** on every reconcile for new content. Downstream tools that require single-shot events should debounce ≥1 s.
- **FUSE is opt-in.** The default mode is `--mode=poll` (synced mirror). FUSE is available with `--mode=fuse` but is not available in Docker containers without `--privileged`.

These are deliberate design choices, not bugs. The mitigations are the `.relay/state.json` status file, `relayfile status`, and `relayfile pull`.

---

## Troubleshooting

### Mount appears stalled

```bash
relayfile status           # check lag and provider errors
cat .relay/state.json      # machine-readable sync state
cat .relay/mount.log       # daemon log (background mode only)
```

If the mount has not reconciled for ≥10 minutes it logs `mount stalled: <reason>` but keeps running. Common causes: network interruption, expired token, provider API outage.

### Token expired

```
error: cloud session expired. Run 'relayfile login' to sign in again.
```

Run `relayfile login`. The running mount continues serving local reads from disk until the process is restarted.

If the refresh token itself expires (default 7 days), the mount enters degraded mode: local reads work, local writes are refused and logged to `.relay/permissions-denied.log` with reason `cloud_session_expired`. Fix:

```bash
relayfile login
relayfile stop my-project
relayfile mount --background my-project ./relayfile-mount
```

### Integration shows `error` status

```bash
relayfile integration list                     # see error message
relayfile integration disconnect linear        # remove
relayfile integration connect linear           # reconnect + re-OAuth
```

### Files missing after connect

Providers start in `cataloging` then `syncing` state. Files appear incrementally — large workspaces (thousands of files) can take several minutes. Watch progress:

```bash
watch -n 5 relayfile status
```

### Permission denials

```bash
cat .relay/permissions-denied.log
relayfile permissions github/repos/acme/api/
```

Agents may only write to paths declared writable in `_PERMISSIONS.md`. Read-only paths return 403 from the Cloud; the denial is logged but does not stop the mount.

### FUSE not available

```
fuse mode is not available in this build; rerun with --mode=poll
```

Docker containers without `--privileged` cannot use FUSE. Use the default poll mode:

```bash
relayfile mount --mode=poll my-project ./relayfile-mount
```

---

## File Layout Reference

```
~/.relayfile/
  cloud-credentials.json    # Cloud login tokens (0600)
  credentials.json          # Relayfile VFS workspace token (0600)
  workspaces.json           # workspace name → ID mapping

<local-dir>/
  github/                   # GitHub files
  notion/                   # Notion files
  linear/                   # Linear files
  slack/                    # Slack files
  .relay/
    state.json              # live sync state (machine-readable)
    mount.pid               # PID of background daemon
    mount.log               # daemon log (rotated)
    conflicts/              # unresolved conflicts (human-fixable)
      resolved/             # cleared conflicts (audit trail)
    dead-letter/            # permanently failed writebacks
    disconnected/           # markers for removed integrations
    permissions-denied.log  # append-only denial log
    integrations/
      github.json           # Nango connection ID per provider
      notion.json
```
