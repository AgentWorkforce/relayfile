# Productized Cloud Mount — v1 Product Contract

**Status:** v1 contract (locked)
**Date:** 2026-05-02
**Owners:** Relayfile CLI, Relayfile Cloud, Relayfile SDK, Agent Skills
**Audience:** Implementers of `relayfile`, `@relayfile/sdk`, `agentrelay.com/cloud` routes, agent harnesses, and the `relayfile-workspace` skill.

This document is the v1 contract for the productized Cloud mount path —
the experience a user gets when they type `relayfile` (or run
`relayfile setup`) and end up with a working, multi-integration workspace
locally and inside their agent. It is normative: every requirement
marked **MUST** is shipped behavior; **SHOULD** is the recommended
default but may be configurable; **MAY** is optional.

The product premise is fixed:

> **The default local attachment is a synced mirror, not a kernel FUSE mount.**
> Agents see ordinary files on disk. The product is honest about sync
> state, conflicts, refresh, and writeback. FUSE remains optional and is
> hardened separately.

If the synced mirror cannot meet a guarantee that a real filesystem
would, the contract calls it out and specifies the user-visible
mitigation. We do not paper over the difference.

---

## 1. First-Run UX: `relayfile` and `relayfile setup`

### 1.1 Invocation forms

The CLI **MUST** support these equivalent first-run forms:

```
relayfile
relayfile setup
relayfile setup --provider github --workspace my-project --local-dir ./relayfile-mount
relayfile setup --cloud-token $RELAYFILE_CLOUD_TOKEN --provider github \
    --workspace ci-project --local-dir ./relayfile-mount --no-open --skip-mount
```

`relayfile` with no args **MUST** dispatch to `runSetup`. The flags
below are normative for `setup`; defaults are listed in parentheses.

| Flag                | Default                                | Purpose                                                           |
|---------------------|----------------------------------------|-------------------------------------------------------------------|
| `--cloud-api-url`   | `https://agentrelay.com/cloud`         | Cloud control plane URL                                           |
| `--cloud-token`     | `$RELAYFILE_CLOUD_TOKEN`               | Skip browser login when set (CI/headless)                         |
| `--workspace`       | prompted, default `relayfile-<ts>`     | Workspace display name                                            |
| `--provider`        | prompted, default `github`             | Integration provider; `none` skips                                 |
| `--local-dir`       | prompted, default `./relayfile-mount`  | Mirror directory                                                   |
| `--no-open`         | `false`                                | Print URLs instead of opening a browser                           |
| `--skip-mount`      | `false`                                | Finish setup without starting the mount loop                       |
| `--once`            | `false`                                | Run a single sync cycle and exit (CI/probe)                       |
| `--login-timeout`   | `5m`                                   | OAuth callback timeout                                             |
| `--connect-timeout` | `5m`                                   | Integration readiness timeout                                      |

### 1.2 Required ordered steps

`relayfile setup` **MUST** execute these steps in this order, and every
step **MUST** be idempotent on re-run:

1. **Print intent.** A single line stating: "Relayfile setup. This signs
   you in, connects an integration, and prepares a local VFS mount."
2. **Cloud login.**
   - If `--cloud-token`/`$RELAYFILE_CLOUD_TOKEN` is set, skip the browser
     and persist the supplied token.
   - Otherwise: bind a localhost callback (`http://127.0.0.1:<port>/callback`),
     open `${cloud-api-url}/api/v1/cli/login?redirect_uri=…&state=…`,
     verify state, and capture `access_token`, `refresh_token`,
     `access_token_expires_at`, `refresh_token_expires_at`, `api_url`.
   - Persist to `~/.relayfile/cloud-credentials.json` with `0600` perms
     and the `cloudCredentials` schema in §5.1.
3. **Workspace name.** Use `--workspace`, else prompt. Default suggestion:
   `relayfile-<UTC YYYYMMDD-HHMMSS>`.
4. **Provider choice.** Use `--provider`, else prompt with the catalog
   defined in §6.2. `none` or `skip` are accepted to defer integration.
5. **Local dir.** Use `--local-dir`, else prompt; default
   `./relayfile-mount`. The directory **MUST** be created with `0o755`
   and a `.relay/` subdir with `0o755`.
6. **Workspace create + join.** `POST /api/v1/workspaces`, then
   `POST /api/v1/workspaces/{id}/join` with
   `{agentName: "relayfile-cli", scopes: ["fs:read", "fs:write"]}`.
   Persist Relayfile VFS credentials to `~/.relayfile/credentials.json`
   (§5.1) and add the workspace to `~/.relayfile/workspaces.json`,
   marking it default.
7. **Integration connect.** If a provider was chosen and not `none`:
   `POST /api/v1/workspaces/{id}/integrations/connect-session` with
   `{allowedIntegrations: [provider]}`. Print the `connectLink`, open it
   unless `--no-open`, and poll
   `GET /api/v1/workspaces/{id}/integrations/{provider}/status?connectionId=…`
   every 2 s until `ready=true` or `--connect-timeout` elapses.
8. **Initial sync gate.** Wait for the workspace's per-provider sync
   status to reach `ready` (§7) before exiting setup, unless
   `--skip-mount` is set. Show progress: `Syncing notion… 142/2,400 files
   (lag 12 s)`. The default deadline is `--connect-timeout`; on timeout
   the CLI **MUST** print: `Initial sync still running. Resume with:
   relayfile mount <id> <dir>` and exit non-zero.
9. **Mount.** Unless `--skip-mount`, fork the existing
   `relayfile mount <workspaceId> <localDir>` loop in the foreground.
   `--once` performs a single reconcile and exits.

### 1.3 Output contract

Each step **MUST** emit exactly one human-readable line on success. On
failure, the CLI **MUST** print `error: <reason>` to stderr and exit with
a non-zero code from this table:

| Code | Meaning                                              |
|-----:|------------------------------------------------------|
| 0    | Setup complete (mount running or skipped)            |
| 1    | Generic failure                                       |
| 10   | Cloud login failed (state mismatch / oauth error)     |
| 11   | Cloud login timed out                                 |
| 20   | Workspace create/join failed                          |
| 30   | Integration connect failed                            |
| 31   | Integration not ready before deadline                 |
| 40   | Initial sync not ready before deadline                |
| 50   | Mount initialization failed                           |

### 1.4 Idempotency

Re-running `relayfile setup` against the same `--workspace` name
**MUST**:

- Reuse the locally tracked workspace ID if present in
  `~/.relayfile/workspaces.json` rather than minting a duplicate.
- Refresh the Relayfile VFS token via Cloud `join` before mount.
- Skip integration connect if the integration status endpoint already
  returns `ready=true`.

---

## 2. Adding More Providers After First Setup

### 2.1 Multi-integration command

The CLI **MUST** expose:

```
relayfile integration connect <provider> [--workspace NAME] [--no-open] [--timeout 5m]
relayfile integration list      [--workspace NAME] [--json]
relayfile integration disconnect <provider> [--workspace NAME] [--yes]
```

Behavior **MUST** match `setup`'s integration step (§1.2.7) for
`connect`, including the readiness wait. `list` calls `GET
/api/v1/workspaces/{id}/integrations` and prints
`provider | status | lag | last_event_at`. `disconnect` calls
`DELETE /api/v1/workspaces/{id}/integrations/{provider}/status`.

Adding a provider **MUST NOT** require re-mounting. The running mount
loop **MUST** observe new files at `/<provider>/…` after the next
reconcile cycle (default 30 s) or the next websocket invalidation event,
whichever comes first. The CLI **MUST** print:

```
notion connected. Files will appear under <local-dir>/notion within ~30s.
```

### 2.2 Provider order and namespaces

The local mirror layout is fixed: each provider gets a top-level
directory rooted at the workspace mount, mirroring the Cloud VFS:

```
<local-dir>/
  github/
  notion/
  linear/
  slack-sage/
  .relay/
```

Adding a provider **MUST NOT** mutate other providers' subtrees.
Removing a provider **MUST** delete only that subtree from the local
mirror after a successful disconnect, and **MUST** leave a marker at
`.relay/disconnected/<provider>.json` recording when it was removed.

### 2.3 Permissions inheritance

When adding a provider, the workspace's existing
`permissions.readonly` and `permissions.ignored` lists **MUST** be
preserved. The connect call **MUST NOT** clear or rewrite them.

---

## 3. Synced-Mirror Limitations and User-Visible Mitigations

The default attachment is a poll-and-watch synced mirror. It is *not*
POSIX, *not* atomic with the source of truth, and *not* a kernel
filesystem. The contract makes the gap explicit and ships mitigations
the user can see.

### 3.1 Honest naming

- `relayfile mount` **MUST** print on start:
  `Mirror started at <local-dir>. Sync interval 30s ±20%. Type
  'relayfile status' for live state.`
- The default flag is `--mode=poll` and the help text **MUST** say
  "synced mirror (recommended)" rather than implying real-time POSIX
  semantics. `--mode=fuse` remains opt-in.

### 3.2 Sync state file

Every mirror directory **MUST** contain a machine-readable
`.relay/state.json` updated atomically after each cycle:

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

Plus:

- `.relay/conflicts/` — one file per unresolved conflict (§8.3).
- `.relay/dead-letter/` — one file per dead-lettered writeback op (§8.4).
- `.relay/permissions-denied.log` — append-only log of denied reads/writes.

These directories **MUST NOT** be mirrored back to the Cloud and
**MUST** be excluded by the file watcher (already enforced in
`internal/mountsync/file_watcher.go`).

### 3.3 `relayfile status`

`relayfile status [WORKSPACE]` **MUST** print a one-screen summary built
from the Cloud `/sync` endpoint and `.relay/state.json`:

```
workspace ws_abc (my-project)   mode: poll   lag: 4s
  notion       ready    214 files   last event 4s ago
  github       syncing  2,318/2,400 last event just now (lag 18s)
  linear       error    last error: 401 invalid_grant (3 min ago)

pending writebacks: 0    conflicts: 0    denied: 12 (see .relay/permissions-denied.log)
```

`--json` **MUST** emit the same shape for scripts.

### 3.4 Writeback semantics from the mirror

Local edits are **eventually** propagated. The contract is:

1. The watcher debounces 100 ms and queues a write.
2. The syncer batches writes; the bulk endpoint
   (`POST /v1/workspaces/{id}/fs/bulk`) is used when ≥ `bulkFlushThreshold`
   files are pending or the cycle ends.
3. Each file write carries the `If-Match: <baseRevision>` header. A 409
   produces a `ConflictError` and the file is moved to
   `.relay/conflicts/<path>.<rev>.local` while the remote copy is
   re-fetched into the original path. The CLI **MUST** print:
   `conflict at notion/Docs/A.md — your edit saved at .relay/conflicts/…`.
4. A 403/permission denial flips `WriteDenied=true` with a `DeniedHash`
   so retries only fire after the user changes the file. The denial is
   appended to `.relay/permissions-denied.log`.
5. On success the syncer rewrites the tracked revision.

### 3.5 Refresh and force-pull

- `relayfile pull [PATH]` **MUST** force a reconcile of the given subtree
  (default: workspace root), discarding local non-dirty files that no
  longer exist remotely and re-fetching the rest.
- Files marked dirty (locally modified, not yet acknowledged) **MUST NOT**
  be discarded by `pull`; they end up in `.relay/conflicts/` only when a
  remote write would otherwise overwrite them.

### 3.6 What the mirror does not provide

The CLI's `relayfile mount --help` and the agent skill's loaded
documentation **MUST** state plainly:

- File handles are not stable across syncs; an editor that holds a file
  open during a remote update will see content change underneath it on
  the next reconcile.
- `mtime` reflects the local write time, not the source-of-truth event
  time. Use `.relay/state.json` for ordering.
- Directory listings can briefly omit a newly created remote file until
  the next reconcile (default 30 s) or a websocket event invalidates the
  cache.
- `inotify`/`fsevents` watchers downstream of the mirror will see
  synthetic create events on every reconcile of new content; debounce
  ≥ 1 s if a downstream tool requires single-shot events.

These limitations are accepted; mitigations are §3.2–§3.5.

---

## 4. Daemon and Background Behavior

### 4.1 Foreground first

`relayfile setup` and `relayfile mount` **MUST** run in the foreground
by default — exactly as a developer expects from a CLI. Ctrl-C
**MUST** flush pending writes for ≤ 5 s, persist `.relay/state.json`,
and exit cleanly. There is no implicit daemon in v1.

### 4.2 Background command

The CLI **MUST** expose:

```
relayfile mount --background [WORKSPACE] [LOCAL_DIR]   # detach from terminal
relayfile mount --pid-file PATH                          # write pid for supervision
relayfile stop  [WORKSPACE]                              # signal the running mount
```

`--background` **MUST**:

- On macOS/Linux: `fork()` + `setsid()`, redirect stdio to
  `${localDir}/.relay/mount.log` (rotated at 10 MB × 3), and write the
  pid to `${localDir}/.relay/mount.pid`.
- On Windows: spawn a detached child process; same pid/log layout.

The CLI **MUST NOT** install system services (launchd plist, systemd
unit, Windows service) implicitly. A `relayfile install-service` command
is out of scope for v1 and **MUST** be tracked separately if requested.

### 4.3 Platform assumptions

| Platform           | Supported in v1 |
|--------------------|-----------------|
| macOS 12+ (arm64/x64) | yes          |
| Linux (glibc, kernel 5.4+) | yes     |
| Windows 10/11 (x64)        | yes     |
| WSL2                       | yes     |
| Alpine / musl              | best-effort, untested |
| Docker without `--privileged` | yes (poll only — FUSE refused with a clear error) |

The CLI **MUST** detect when `RELAYFILE_MOUNT_FUSE=1` is set on a
platform without FUSE and exit 50 with: `fuse mode is not available in
this build; rerun with --mode=poll`.

### 4.4 Supervision contract

A running mount process **MUST**:

- Re-establish the websocket on disconnect with exponential backoff
  capped at 30 s.
- Continue polling at the configured interval even when the websocket
  is down (degrading gracefully to poll-only).
- Persist `.relay/state.json` after every cycle and on graceful exit.
- Detect token expiry and trigger §5 rejoin without dropping the
  process.

If the mount cannot make progress for ≥ 10 minutes (no successful
reconcile), it **MUST** log
`mount stalled: <reason>` to stderr/log, but **MUST NOT** exit unless
the user sends SIGTERM. Stall reasons populate `.relay/state.json`.

---

## 5. Token Refresh and Workspace Rejoin Semantics

Long-lived mounts span hours to weeks. Tokens do not. The contract has
two layers: Cloud login tokens (user identity) and Relayfile VFS tokens
(workspace access).

### 5.1 Credential files

- `~/.relayfile/cloud-credentials.json` (`0600`):
  ```json
  {
    "apiUrl": "https://agentrelay.com/cloud",
    "accessToken": "...",
    "refreshToken": "...",
    "accessTokenExpiresAt":  "2026-05-03T18:00:00Z",
    "refreshTokenExpiresAt": "2026-05-09T18:00:00Z",
    "updatedAt":             "2026-05-02T18:00:00Z"
  }
  ```
- `~/.relayfile/credentials.json` (`0600`):
  ```json
  {
    "server": "https://api.relayfile.dev",
    "token":  "<JWT for workspace>",
    "updatedAt": "2026-05-02T18:00:00Z"
  }
  ```
- `~/.relayfile/workspaces.json`: catalog with `default` and per-name
  `id`, `createdAt`, `lastUsedAt`.

### 5.2 Cloud token refresh

The CLI **MUST** treat a Cloud access token as expired when
`now ≥ accessTokenExpiresAt - 60s`. Refresh **MUST** call
`POST ${apiUrl}/api/v1/auth/token/refresh` with the refresh token and
**MUST**:

- Persist the new token set atomically (write temp file, rename).
- Coalesce concurrent refresh attempts inside a single CLI process.
- Surface a `403 invalid_grant` as `error: cloud session expired. Run
  'relayfile login' to sign in again.` and exit 10. The mount process
  **MUST** keep running on the existing VFS token until that token also
  expires.

### 5.3 Relayfile VFS token rejoin

The Relayfile VFS token used by the mount **MUST** be refreshed by
re-issuing the workspace `join` call:

```
POST ${cloud-api-url}/api/v1/workspaces/{id}/join
  { "agentName": "relayfile-cli", "scopes": ["fs:read","fs:write"] }
```

Triggers:

- The mount **MUST** preemptively rejoin when the JWT's `exp` is within
  10 % of its lifetime, with a floor of 5 minutes.
- Any 401/403 from a Relayfile API call **MUST** trigger a single
  rejoin attempt before the call is retried. A second 401 fails the
  cycle and is logged.

The rejoin **MUST**:

- Use the Cloud access token (refreshing it first if needed per §5.2).
- Replace the on-disk `credentials.json` atomically.
- Update the in-memory token of the running syncer/HTTP client without
  killing the process or losing the websocket.

The SDK (`@relayfile/sdk`'s `RelayfileSetup` / `WorkspaceHandle`) **MUST**
expose this refresh as `await handle.refreshToken()` and **MUST** call
it automatically when the token age exceeds 55 minutes.

### 5.4 What the user can lose

If the refresh token also expires (default 7 days), the mount **MUST**:

- Continue serving local reads from disk (already-synced state).
- Refuse local writes for affected paths and place them in
  `.relay/permissions-denied.log` with reason `cloud_session_expired`.
- Print one stderr line per minute (capped) directing the user to run
  `relayfile login`. This is the only condition under which v1 mounts
  enter a degraded read-only state.

---

## 6. Cloud Provider Catalog and Nango Config-Key Source of Truth

### 6.1 Single source of truth

The list of supported providers and their Nango `providerConfigKey`
mapping **MUST** live in **one** place: the Cloud package
`packages/web/lib/integrations/providers.ts`, exporting
`WORKSPACE_INTEGRATION_PROVIDERS` and a pure function
`getProviderConfigKey(provider)` (already used in
`app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts`).

The Relayfile CLI and the SDK **MUST NOT** hardcode their own provider
list. Both **MUST** load the catalog at runtime from
`GET ${cloud-api-url}/api/v1/integrations/catalog` (added in v1) which
returns:

```json
{
  "providers": [
    { "id": "github",            "displayName": "GitHub",   "configKey": "github-sage",            "vfsRoot": "/github" },
    { "id": "notion",            "displayName": "Notion",   "configKey": "notion-sage",            "vfsRoot": "/notion" },
    { "id": "linear",            "displayName": "Linear",   "configKey": "linear-sage",            "vfsRoot": "/linear" },
    { "id": "slack-sage",        "displayName": "Slack",    "configKey": "slack-sage",             "vfsRoot": "/slack" },
    { "id": "slack-my-senior-dev","displayName":"Slack (MSD)","configKey":"slack-my-senior-dev",   "vfsRoot": "/slack-msd" },
    { "id": "slack-nightcto",    "displayName":"Slack (NightCTO)","configKey":"slack-nightcto",    "vfsRoot": "/slack-nightcto" }
  ],
  "version": "<sha-of-providers.ts>"
}
```

The CLI and SDK **MUST** cache the catalog for 1 hour and **MUST**
revalidate when an integration `connect-session` returns
`409 unknown_provider`.

### 6.2 v1 provider list

For v1 the catalog **MUST** include exactly the providers in
`WORKSPACE_INTEGRATION_PROVIDERS`:
`github`, `notion`, `linear`, `slack-sage`, `slack-my-senior-dev`,
`slack-nightcto`. `slack` (the bare alias) **MUST** be accepted as
input by the CLI prompt but normalized to `slack-sage` before any API
call.

### 6.3 Nango key constraints

- The Cloud route **MUST** be the only caller of Nango's secret key.
  CLI/SDK **MUST NEVER** receive a Nango secret directly.
- `connect-session` responses **MUST** always include a `connectionId`
  string (v1 already enforces this). The CLI **MUST** persist it under
  `.relay/integrations/<provider>.json` so future readiness checks use
  the connection id, not the workspace id, when available.
- Adding a new provider in v1.x **MUST** require a single PR to
  `providers.ts`; downstream catalog consumers pick it up
  automatically on the next refresh.

### 6.4 Provider deprecation

Removing or renaming a provider **MUST**:

- Bump the catalog `version`.
- Mark the old id `deprecated: true` for at least 30 days before
  removal.
- Cause `relayfile integration list` to render a warning row when
  any deprecated provider is connected.

---

## 7. Initial Sync and Readiness Semantics After OAuth

### 7.1 Readiness states

For each connected provider the Cloud `/sync` endpoint
(`GET /api/v1/workspaces/{id}/sync`) returns one of:

| Status      | Meaning                                                                |
|-------------|-------------------------------------------------------------------------|
| `pending`   | Nango connection accepted; initial cataloging not yet started           |
| `cataloging`| Listing remote objects                                                  |
| `syncing`   | Materializing files into the VFS                                        |
| `ready`     | Initial sync complete; subsequent updates are incremental               |
| `error`     | Last attempt failed; `lastError` is populated                           |
| `degraded`  | Watermark older than 10 min and increasing; still serving stale data    |

Each provider entry **MUST** include `lagSeconds`, `failureCodes`,
`deadLetteredEnvelopes`, `deadLetteredOps`, and a stable monotonic
`watermarkTs`.

### 7.2 CLI behavior after connect

After `connect-session` returns `ready=true`:

1. The CLI **MUST** poll `/sync` every 2 s.
2. The CLI **MUST** consider the integration ready when **all** of:
   - Provider `status == "ready"`, **or**
   - `status == "syncing"` with `lagSeconds < 30` and at least one file
     materialized under `/<provider>/`.
3. The default deadline is `--connect-timeout` (5 m). On timeout the
   CLI **MUST** print: `notion still syncing in the background. Files
   will continue to populate. See 'relayfile status'.` and exit 0 (the
   workspace and mount are still usable; sync will catch up).
4. While polling, the CLI **MUST** print a one-line progress update at
   most every 5 s.

### 7.3 Mount behavior on first connect

The mount loop **MUST**:

- Treat a provider in `cataloging`/`syncing` as authoritative — it does
  not delete local files for a provider whose status is not `ready` even
  if a remote tree page comes back empty. This prevents the mirror from
  flapping while the source-of-truth is still being indexed.
- Resume normal reconcile semantics once the provider transitions to
  `ready`.

### 7.4 Webhook readiness

OAuth completion does not guarantee that the provider's webhook is
delivering events. The Cloud **MUST** include in the `/sync` response a
boolean `webhookHealthy` per provider. If `false` and `lagSeconds >
60`, the CLI **MUST** display a warning: `notion webhook unhealthy —
falling back to periodic sync (lag 78 s)`.

---

## 8. Writeback Safety, Schema Validation, Conflicts, Dead-Letter

### 8.1 Writeback path conventions

Each adapter declares two kinds of paths:

- **Read-only paths.** Materialized from upstream. Local edits are
  refused at the Cloud and recorded as denials.
- **Writeback paths.** A write here triggers the adapter's
  `writeback` handler, which calls the upstream API.

Write-allowed paths **MUST** be advertised in
`/<provider>/_PERMISSIONS.md` (already a convention) and in the catalog
response (§6.1) under each provider's `vfsRoot`. The CLI surfaces them
via `relayfile permissions [PATH]`.

### 8.2 Schema validation

For writeback paths whose adapter declares a schema (e.g. JSON files
under `/github/repos/*/pulls/*/reviews/*.json`), the Cloud **MUST**
validate the body before accepting the write. A schema violation
returns `400 schema_validation_failed` with a structured `errors` array.

The CLI **MUST**:

- Surface the violation immediately on the offending file:
  `error at github/repos/.../review.json: body.event must be one of
  APPROVE,REQUEST_CHANGES,COMMENT (line 3)`.
- Move the offending file to
  `.relay/conflicts/<path>.invalid.<ts>` so the user can edit it
  without breaking the next sync cycle.
- Re-fetch the previous remote version into the original path.

### 8.3 Conflict semantics

A conflict is a 409 returned by `PUT /fs/file` or by an entry in
`POST /fs/bulk`'s `errors[]` with `code: "conflict"`. The CLI **MUST**:

1. Save the local body verbatim to
   `.relay/conflicts/<path>.<localRev>.local`.
2. Re-fetch the remote into `<path>` and update the tracked revision.
3. Increment `pendingConflicts` in `.relay/state.json`.
4. Print: `conflict at <path>. Yours saved at .relay/conflicts/<…>;
   remote refreshed in place. Resolve by editing <path> and saving.`.

A conflict is cleared when the user's next save succeeds against the
new revision; the `.relay/conflicts/<…>.local` file is moved to
`.relay/conflicts/resolved/` for audit.

### 8.4 Dead-letter visibility

If the Cloud returns a non-retryable error (4xx other than 409/429,
or a 5xx after the configured retries), the writeback **MUST** be
dead-lettered. The Cloud `/sync` response surfaces
`deadLetteredOps` per provider. The CLI **MUST**:

- Mirror each dead-lettered op as a JSON record in
  `.relay/dead-letter/<id>.json`:
  ```json
  { "opId": "op_…", "path": "/github/.../review.json",
    "code": "validation_error", "message": "body must include event",
    "createdAt": "…", "lastAttemptedAt": "…", "attempts": 3,
    "replayUrl": "https://…/v1/workspaces/<id>/ops/op_…/replay" }
  ```
- Provide `relayfile ops list` and `relayfile ops replay <opId>` that
  call `POST /v1/workspaces/{id}/ops/{opId}/replay`. On success the
  local `.relay/dead-letter/<id>.json` is deleted.

### 8.5 Idempotency keys

The CLI **MUST** include `X-Correlation-Id` on every API call (already
implemented in `mountsync.HTTPClient.doJSON`). For writeback, the
adapter contract requires an idempotency key derived from
`(workspaceId, path, baseRevision, sha256(content))`; the Cloud rejects
duplicates with `200 already_applied`. The CLI **MUST** treat that as
success.

---

## 9. Agent Skill Contract

This section defines what the `relayfile-workspace` skill — and any
agent harness using the SDK — **MUST** rely on. It is the
agent-visible portion of the contract.

### 9.1 Mount discovery

An agent **MUST** discover its workspace mount in this priority order:

1. `$RELAYFILE_LOCAL_DIR` — always wins when set.
2. The directory printed by `relayfile status --json` as
   `.providers[*].localDir` (single workspace) or, for the default
   workspace, `.localDir`.
3. The first directory upward from CWD containing a `.relay/state.json`.

The skill **MUST** fail loud if none of the above resolves:
`relayfile mount not found. Run 'relayfile setup' or set
RELAYFILE_LOCAL_DIR.`.

### 9.2 Path conventions

The agent **MUST** treat the following paths as authoritative:

```
<mount>/<provider>/...           # source-of-truth materialized files
<mount>/<provider>/_PERMISSIONS.md  # writeback rules for this provider
<mount>/.relay/state.json         # sync state
<mount>/.relay/conflicts/         # unresolved conflicts (human-fixable)
<mount>/.relay/dead-letter/       # failed writebacks
<mount>/.relay/permissions-denied.log
```

The agent **MUST NOT**:

- Write into `<mount>/.relay/`.
- Delete or rename top-level provider directories.
- Trust `mtime` — use `state.json.lastReconcileAt` and provider
  `lagSeconds` for ordering decisions.

### 9.3 Status awareness

Before performing any non-trivial read or any write, an agent **SHOULD**:

1. Read `.relay/state.json`.
2. Skip or warn when the relevant provider's `status != "ready"` or
   `lagSeconds > 60`.
3. After writing a file, poll `state.json` until either
   `pendingWriteback == 0` or the file appears in
   `.relay/conflicts/`/`.relay/dead-letter/`.

The skill loader **MUST** provide a helper
`waitForWriteback(path, { timeoutMs })` that implements this poll.

### 9.4 Safe edits

Writes **MUST** follow this shape:

1. `read` the current file (capture revision via `state.json` or
   `getFile` SDK call).
2. Mutate locally.
3. Save. The mirror's watcher uploads with `If-Match` automatically.
4. Wait for ack per §9.3.
5. On conflict, re-read the remote, re-apply the diff, save again. Do
   **not** delete the local conflict file unless the agent explicitly
   chose to drop its edit.

Writes that produce JSON **MUST** be validated against the adapter
schema printed in `_PERMISSIONS.md` (or fetched from the catalog) before
saving, to surface schema errors locally rather than via a dead-letter
round-trip.

### 9.5 Multi-agent coordination

When two agents share a mount (the relayfile-workspace skill scenario),
they **MUST** treat `.relay/conflicts/` as a coordination signal —
seeing a conflict on a path another agent owns is the trigger to back
off, not to overwrite. The contract explicitly permits using
`agent-relay` channels for additional coordination but does not require
it.

---

## 10. End-to-End Acceptance Tests

The acceptance tests **MUST** live alongside existing CLI tests and run
against realistic mocks of Cloud, Nango, and Relayfile services.

### 10.1 Mock services

- **Mock Cloud** — `httptest` server in Go (CLI tests) and a
  `node:http` server in TS (SDK tests) implementing `/api/v1/cli/login`,
  `/api/v1/auth/token/refresh`, `/api/v1/workspaces`,
  `/api/v1/workspaces/{id}/join`,
  `/api/v1/workspaces/{id}/integrations/connect-session`,
  `/api/v1/workspaces/{id}/integrations/{provider}/status`,
  `/api/v1/workspaces/{id}/sync`, `/api/v1/integrations/catalog`.
- **Mock Nango** — already exists at
  `tests/helpers/mock-nango-sdk-server.ts`. v1 reuses it and adds a
  webhook simulator path.
- **Mock Relayfile** — `httptest` server delivering `/v1/.../fs/tree`,
  `/fs/file`, `/fs/bulk`, `/fs/events`, `/fs/export`, `/sync`, `/ops`.
  This expands the existing fixtures in `cmd/relayfile/*_test.go` and
  `internal/mountsync/*_test.go`.

### 10.2 Required acceptance scenarios

Each scenario **MUST** be a single test function that fails the build
if the contract is violated. Listed by area with the file each test
lives in.

| # | Scenario                                                                | File                                              |
|---|--------------------------------------------------------------------------|---------------------------------------------------|
| A1 | `relayfile setup` happy path with `--cloud-token` (no browser): creates workspace, connects github, waits for sync ready, mount runs one cycle, exits 0 with `--once`. | `cmd/relayfile/setup_e2e_test.go`                |
| A2 | `relayfile setup` with browser flow, state mismatch returns exit 10.    | `cmd/relayfile/setup_e2e_test.go`                |
| A3 | `relayfile setup` re-run with same `--workspace` reuses local id, refreshes token, skips connect when integration status is already ready. | `cmd/relayfile/setup_e2e_test.go`                |
| A4 | `relayfile integration connect notion` after setup: workspace gains `/notion` subtree without restarting mount. | `cmd/relayfile/integration_e2e_test.go`          |
| A5 | Mirror conflict: local edit + remote edit with new revision yields `.relay/conflicts/<path>.<rev>.local` and a refreshed local file. | `internal/mountsync/syncer_conflict_test.go`     |
| A6 | Schema validation failure on writeback: file lands in `.relay/conflicts/<path>.invalid.<ts>`, original restored, exit code 0 in CLI status. | `internal/mountsync/syncer_writeback_test.go`    |
| A7 | Dead-letter surfacing: a 422 from Cloud during writeback creates `.relay/dead-letter/<opId>.json`; `relayfile ops replay` clears it on success. | `cmd/relayfile/ops_e2e_test.go`                  |
| A8 | Token refresh: VFS token expires mid-mount; mount calls `/join` with the cloud access token, replaces credentials, continues without dropping websocket. | `internal/mountsync/syncer_refresh_test.go`      |
| A9 | Cloud refresh token expired: mount enters read-only degraded state, prints recovery instruction once per minute, no exit. | `internal/mountsync/syncer_refresh_test.go`      |
| A10 | Initial sync gate: Cloud reports `cataloging`; CLI waits, polls `/sync`, exits 0 once `ready`. With a forced timeout it exits 0 with the resume hint. | `cmd/relayfile/setup_e2e_test.go`                |
| A11 | Webhook unhealthy: Cloud returns `webhookHealthy=false, lagSeconds=80`; `relayfile status` includes the warning row. | `cmd/relayfile/status_e2e_test.go`               |
| A12 | Catalog refresh: a new provider appears in the mocked `/integrations/catalog`; CLI accepts it within 1 h or after a 409 forces revalidation. | `cmd/relayfile/catalog_test.go`                  |
| A13 | Synced-mirror honesty: `relayfile mount` start banner mentions `synced mirror` and the 30 s interval; `--help` shows the limitations from §3.6. | `cmd/relayfile/mount_help_test.go`               |
| A14 | Background mode: `relayfile mount --background` writes `mount.pid` and `mount.log`; `relayfile stop` signals it cleanly within 5 s. | `cmd/relayfile/background_test.go`               |
| A15 | SDK parity: `RelayfileSetup.login + createWorkspace + connectNotion + waitForNotion + client()` against the same mock cloud yields a usable client. | `tests/sdk/setup-e2e.test.ts`                    |
| A16 | Multi-agent coordination: two `WorkspaceHandle` instances writing to disjoint paths via the bulk endpoint never see false conflicts; writing to the same path produces a deterministic `.relay/conflicts/` entry on one side. | `tests/sdk/multi-agent.test.ts`                  |

### 10.3 Test data realism rules

Tests **MUST**:

- Use real provider ids from §6.2 — no fake placeholders.
- Use real Nango envelope shapes from
  `tests/nango-webhook-router-fanout.test.ts` and
  `tests/relayfile-provider-writeback.e2e.test.ts`.
- Exercise the bulk write path (`/fs/bulk`) for any test that writes
  more than one file, matching the CLI's actual batching behavior.
- Run with `RELAYFILE_LOG=debug` and assert structured log lines for
  the steps above. No test relies on stderr text alone.

### 10.4 CI gate

The `relayfile` repo's CI pipeline **MUST** run all A1–A16 scenarios on
every PR that touches `cmd/relayfile/**`, `internal/mountsync/**`,
`packages/sdk/src/setup*`, or `packages/sdk/src/cloud-login.ts`. A
failure of any acceptance test blocks merge.

---

## Summary of Producible Artifacts (v1)

The contract is implementable by shipping:

1. CLI: `setup`, `integration {connect,list,disconnect}`, `pull`,
   `status (extended)`, `permissions`, `ops {list,replay}`, `mount
   --background`, `stop`. Behavior changes in `mountsync` for the
   `.relay/state.json` writer, conflicts/dead-letter directories, and
   token rejoin.
2. SDK: `RelayfileSetup` already covers most of §5; adds
   `WorkspaceHandle.refreshToken` automatic invocation,
   `agentInvite()` carrying `vfsRoot` from the catalog, and a
   `waitForWriteback(path)` helper.
3. Cloud: `GET /api/v1/integrations/catalog`, extended `/sync` response
   (`webhookHealthy`, `deadLetteredOps`), `POST /ops/{id}/replay`,
   and unchanged `/connect-session` / `/join` semantics.
4. Skill: `relayfile-workspace` skill loads §9 verbatim and provides
   `waitForWriteback`, `readState`, and `enforceProviderReady` helpers.
5. Tests: A1–A16 plus the existing test suite.

PRODUCTIZED_CLOUD_MOUNT_CONTRACT_READY
