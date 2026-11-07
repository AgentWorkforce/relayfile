# RelayFile CLI — Design Doc

**Status:** Updated for v1 Cloud productization
**Date:** 2026-05-02 (original: 2026-03-24)

---

## Overview

The `relayfile` CLI is the primary interface for humans and CI systems to interact with RelayFile workspaces. It wraps the RelayFile HTTP API and the existing `relayfile-mount` sync engine into a single, ergonomic command-line tool.

### Design principles

- **Minimal flags, sensible defaults.** The happy path should require as few arguments as possible.
- **Canonical auth over local fallbacks.** The npm entrypoint uses a bundled, login-only slice of `@agent-relay/cloud` to establish or refresh the shared session before setup starts. That setup-auth path never invokes the `agent-relay` CLI. The legacy explicit `relayfile login` command may still delegate to an installed Agent Relay CLI; it is not part of the zero-install setup path. Explicit Relayfile tokens (`--token`, `RELAYFILE_TOKEN`, or self-hosted `relayfile login --api-key`) remain available for CI and self-hosted deployments.
- **Composable with pipes and scripts.** All commands emit structured JSON when `--json` is passed; human-readable tables otherwise.
- **No implicit destructive actions.** Deletes require confirmation unless `--yes` is passed.

---

## Authentication

### Supported methods (in priority order)

| Priority | Method | Use case |
|----------|--------|----------|
| 1 | `--token` flag | One-off override |
| 2 | `RELAYFILE_TOKEN` env var | CI/CD pipelines |
| 3 | `~/.agentworkforce/relay/cloud-auth.json` (or `CLOUD_API_*`) | Cloud-hosted interactive use |
| 4 | `~/.relayfile/credentials.json` | Self-hosted/API-key compatibility |

These stores may coexist. Hosted commands do not migrate, rewrite, or delete
the self-hosted file. Resolution is first-match-wins in the table above: an
explicit flag or environment token overrides the hosted session, a configured
hosted session overrides the self-hosted file, and the self-hosted file remains
the fallback when no hosted session exists. A configured but malformed hosted
session is an error rather than an implicit switch to another server.

### Auth flow: Cloud-hosted

```
npx relayfile@latest
```

1. The npm launcher calls `ensureCloudSession` from its bundled Agent Relay
   Cloud SDK slice. The SDK opens hosted login when needed, refreshes existing
   credentials, and writes the canonical shared session.
2. The quickstart creates a Cloud workspace named after the current directory,
   connects GitHub, and mounts it at `./relayfile-mount`. If another local
   project with the same directory name is already tracked, Relayfile adds a
   stable path suffix instead of reusing that project's workspace or mirror.
3. `relayfile setup` resolves the Cloud session without invoking
   `agent-relay`. Some other workspace-resolution paths still call
   `agent-relay workspace active --json` for the canonical
   `relayfileWorkspaceId`. Cloud session resolution itself uses this order:
   - A `CLOUD_API_ACCESS_TOKEN` in the environment wins, together with
     `CLOUD_API_URL`, `CLOUD_API_REFRESH_TOKEN`,
     `CLOUD_API_ACCESS_TOKEN_EXPIRES_AT` and
     `CLOUD_API_REFRESH_TOKEN_EXPIRES_AT` when supplied. The access token alone
     is a complete session: with no refresh token there is nothing to roll, so
     Relayfile uses it as-is and never attempts a refresh.
   - Otherwise the canonical credential file
     `~/.agentworkforce/relay/cloud-auth.json`, which must carry `apiUrl`,
     `accessToken`, `refreshToken` and an RFC3339 `accessTokenExpiresAt`.
     A file that is present but incomplete is an error, not a fall-through.
   - With neither, Relayfile reports that no session exists and names both
     recovery paths. It never invents a session.

   Refresh: an access token within five minutes of expiry — or a refresh token
   within 24 hours of its own expiry — is rolled through Cloud's
   `/api/v1/auth/token/refresh`. **A session that came from the environment is
   refreshed in memory only and never written to disk**, because it belongs to
   whoever exported it. **Only a file-sourced session is written back**, under
   the same `cloud-auth.json.lock` directory lock `agent-relay` uses, because
   Cloud rotates the refresh token on every refresh and a rotation kept private
   would invalidate the copy the CLI reads.
4. Relayfile runtime tokens are minted with Cloud `/join` and kept in memory; they are not persisted as a second cloud session.

Relayfile requires the `agent-relay` CLI binary on `PATH` to be version
`8.7.0` or newer for *workspace resolution*, because that is the first
published CLI version with the `workspace active --json` surface. Operators can
override the binary with `RELAYFILE_AGENT_RELAY_BIN`. Relayfile deliberately
does **not** read `AGENT_RELAY_BIN`: throughout Agent Relay that variable names
the *broker* binary (`agent-relay-broker`), which has no `cloud` or `workspace`
subcommand, and every relay-spawned agent exports it. Sandboxes and Daytona/base images
that run relayfile must install or update `agent-relay` before exercising the
Cloud-hosted path. The cloud/workforce sandbox image owner is responsible for
that runtime rollout; Relayfile enforces the version at startup but does not
update deployed images itself.

### Auth flow: API key / self-hosted

```
relayfile login --api-key --server https://api.relayfile.dev
```

1. Prompts the user for an API key (paste from dashboard or generate via API).
2. Validates the key by calling `GET /health` on the target server.
3. Writes `~/.relayfile/credentials.json`.

### Credential file: `~/.relayfile/credentials.json`

```json
{
  "server": "https://api.relayfile.dev",
  "token": "eyJ...",
  "refreshToken": null,
  "expiresAt": null
}
```

- `server` — base URL for all API calls. Default: `https://api.relayfile.dev`.
- `token` — Bearer JWT or API key.
- `refreshToken` / `expiresAt` — legacy fields. Cloud-hosted refresh uses the shared canonical Cloud session instead.
- File permissions: `0600` (user-only read/write).

---

## Commands

### `relayfile` / `relayfile setup`

Run the low-friction Cloud setup path for humans and agent-guided onboarding.

```
relayfile
relayfile setup [--provider github] [--workspace my-project] [--local-dir ./relayfile-mount]
```

With no arguments, `relayfile` supplies `github`, the current directory name,
and `./relayfile-mount` automatically. The flag defaults below describe the
explicit `relayfile setup` wizard.

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-api-url` | `https://agentrelay.com/cloud` | Relayfile Cloud API URL |
| `--cloud-token` | `RELAYFILE_CLOUD_TOKEN` | Cloud access token for headless setup; skips browser login |
| `--provider` | prompted, default `github` | Integration to connect; use `none` to skip |
| `--workspace` | prompted | Workspace name to create |
| `--local-dir` | prompted, default `./relayfile-mount` | Local VFS mount directory |
| `--no-open` | false | Print hosted login/connect URLs instead of opening a browser |
| `--skip-mount` | false | Complete setup without starting the foreground mount loop |
| `--once` | false | Run one mount sync cycle and exit |

**Behavior:**

1. The npm launcher asks the bundled Agent Relay Cloud SDK to establish or
   refresh the canonical shared session. The native runtime then reads that
   same store directly; the launcher does not copy file-backed credentials
   into `CLOUD_API_*` environment variables.
2. The native runtime reads the Cloud access token from
   `~/.agentworkforce/relay/cloud-auth.json` (or the `CLOUD_API_*`
   environment), refreshing it in place when it is inside its expiry window.
3. Create or reuse the named Cloud workspace directly; setup does not require
   an installed `agent-relay` binary or a preselected Agent Relay workspace.
4. Mint Relayfile runtime credentials without persisting them as a second login.
5. Request a hosted Nango connect session for the selected integration and wait until the Cloud status endpoint reports it ready.
6. Start the existing `relayfile mount` sync loop so the user and agent see ordinary files.

Re-running `relayfile setup` with the same workspace name reuses the locally
tracked workspace ID, refreshes the Cloud session if needed, mints a fresh
Relayfile JWT through the client-specific renewal route, preserves the existing
local mirror directory, and only
opens a new integration connect flow when the requested provider is not already
connected.

This path is intentionally a wrapper over the lower-level commands and Cloud APIs. Existing `login`, `workspace`, `mount`, `tree`, and `read` commands remain available for CI, self-hosted servers, and scripted workflows.

The bundled SDK surface is intentionally limited to Cloud session creation and
refresh. This keeps the published package small while retaining Agent Relay's
canonical browser/device flow, auth-file locking, and refresh behavior.

---

### `relayfile login`

Authenticate through the canonical relay session.

```
relayfile login [--no-open] [--provision-messaging-only]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--no-open` | `false` | Forwarded as `agent-relay cloud login --device` (prints a sign-in URL and code instead of opening a browser) |
| `--api-key` | `false` | Preserve the self-hosted/API-key credential path |
| `--server` | `https://api.relayfile.dev` | Server base URL for `--api-key` |
| `--provision-messaging-only` | `false` | When the active Agent Relay workspace exists only in Relaycast, create a separate Relayfile-backed workspace with a ` (Relayfile)` display-name suffix. |

**Behavior:**

1. Default path delegates to `agent-relay cloud login`.
2. Relayfile does not write `~/.relayfile/cloud-credentials.json`.
3. `--api-key` keeps the self-hosted compatibility path and writes `~/.relayfile/credentials.json` with `0600` permissions.

Messaging-only provisioning deduplicates against
`~/.relayfile/workspaces.json`, so the guarantee is local to the current
machine. Running the flag from a second machine without that catalog entry can
create another Relayfile-backed workspace with the same display name.

**Errors:**
- Server unreachable: `Error: cannot reach <server> — check the URL and your network.`
- Invalid token: `Error: server rejected the token (HTTP 401). Check your API key.`

---

### `relayfile workspace create`

Create a new workspace.

```
relayfile workspace create <name>
```

| Argument | Required | Description |
|----------|----------|-------------|
| `name` | Yes | Human-readable workspace name |

**Behavior:**

1. With the canonical hosted session, reuse a stored hosted binding only when
   `GET /api/v1/workspaces` confirms its exact Cloud workspace ID for the
   current session. Otherwise, `POST /api/v1/workspaces` with
   `{ "name": "<name>" }`.
2. Resolve the returned Cloud app workspace ID through
   `GET /api/v1/workspaces/{id}/resolve`. Relayfile data-plane and delegated
   credential routes use the returned `relayfileWorkspaceId`, never the Cloud
   app UUID.
3. Mint delegated Relayfile credentials for that resolved workspace and store
   the Cloud-ID ↔ Relayfile-ID mapping in `~/.relayfile/workspaces.json`.
4. With only `~/.relayfile/credentials.json`, preserve the existing
   self-hosted/API-key compatibility behavior and leave that file unchanged.
   The catalog is shared by both modes, so hosted setup never reuses a row by
   display name alone. A stored hosted binding is reusable only when the
   current Cloud session lists its exact Cloud workspace ID; otherwise the CLI
   creates a new hosted workspace and leaves the self-hosted or stale row
   untouched.

**`~/.relayfile/workspaces.json`:**

```json
{
  "workspaces": {
    "my-project": {
      "id": "ws_abc123",
      "server": "https://api.relayfile.dev",
      "createdAt": "2026-03-24T12:00:00Z"
    }
  },
  "default": "my-project"
}
```

The first workspace created becomes the default. Use `relayfile workspace use <name>` to switch.

---

### `relayfile workspace list`

List accessible workspaces.

```
relayfile workspace list [--json]
```

**Behavior:**

1. With the canonical hosted session, `GET /api/v1/workspaces` and print the
   reachable Cloud workspaces.
2. Otherwise use the explicit/environment/self-hosted token against the
   Relayfile admin listing, retaining the local catalog fallback.

---

### `relayfile logout`

Logout revokes the refresh token in the canonical hosted session before
removing `cloud-auth.json` and `cloud-identity.json`, then clears Relayfile's
local self-hosted and delegated credentials. If revocation cannot be verified,
the hosted credential is kept for a retry and the command returns an error; it
does not report that the user is already logged out. A session supplied through
`CLOUD_API_*` must be unset in the parent process and cannot be cleared by a
child CLI.

---

### `relayfile workspace delete`

Delete a workspace.

```
relayfile workspace delete <name> [--yes]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--yes` | `false` | Skip confirmation prompt |

**Behavior:**

1. Resolve `<name>` to a workspace ID via `~/.relayfile/workspaces.json` or the API.
2. Prompt: `Delete workspace "<name>" (ws_abc123)? This cannot be undone. [y/N]`
3. `DELETE /v1/workspaces/{workspaceId}`.
4. Remove entry from `~/.relayfile/workspaces.json`.

---

### `relayfile mount`

Mount a workspace to a local directory, syncing changes in real-time.

```
relayfile mount <workspace> [local-dir]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | Yes | — | Workspace name or ID |
| `local-dir` | No | `.` (cwd) | Local directory to sync into |

| Flag | Default | Description |
|------|---------|-------------|
| `--interval` | `30s` | Polling interval between sync cycles |
| `--once` | `false` | Run a single sync cycle and exit |
| `--mode` | `poll` | `poll` (synced mirror, recommended) or `fuse` (opt-in, POSIX) |
| `--background` | `false` | Detach from terminal; write PID to `.relay/mount.pid`, logs to `.relay/mount.log` |
| `--pid-file` | `.relay/mount.pid` | Override PID file path |
| `--no-websocket` | `false` | Disable WebSocket streaming, poll only |

**Behavior:**

1. Print on start: `Mirror started at <local-dir>. Sync interval 30s ±20%. Type 'relayfile status' for live state.`
2. Resolve workspace name to ID.
3. Create `local-dir` if it doesn't exist; create `.relay/` subdirectory.
4. Start `mountsync.Syncer` in foreground (default) or detached (`--background`).
5. Foreground mode logs to stderr; `Ctrl+C` flushes pending writes (≤5 s), persists `.relay/state.json`, and exits.
6. Background mode (`--background`): fork + setsid (macOS/Linux) or detached child (Windows); redirect stdio to `.relay/mount.log` (rotated at 10 MB × 3); write PID to `.relay/mount.pid`.
7. On `--mode=fuse` in an environment without FUSE: exit 50 with `fuse mode is not available in this build; rerun with --mode=poll`.

**Sync state:** After every cycle and on clean exit, the daemon writes `.relay/state.json` atomically. Token expiry triggers an automatic workspace rejoin without dropping the process. If no successful reconcile occurs for ≥10 minutes, logs `mount stalled: <reason>` but keeps running.

**Relationship to `relayfile-mount`:** This command replaces the standalone `relayfile-mount` binary for end users. The `cmd/relayfile-mount` binary remains available for backwards compatibility and for deployments that need a minimal single-purpose daemon.

---

### `relayfile tree`

List a remote workspace path without mounting.

```
relayfile tree <workspace> [path] [--depth n] [--json]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | Yes | — | Workspace ID |
| `path` | No | `/` | Remote VFS path to list |

| Flag | Default | Description |
|------|---------|-------------|
| `--depth` | `1` | Maximum tree depth |
| `--json` | `false` | Pretty-print the raw API response |

**Behavior:**

1. Calls `GET /v1/workspaces/{workspaceId}/fs/tree?path=<path>&depth=<depth>`.
2. Prints a compact human-readable tree by default.
3. With `--json`, prints the raw response formatted as JSON for scripts.

---

### `relayfile read`

Read one remote file without mounting.

```
relayfile read <workspace> <path> [--output file] [--json]
relayfile cat <workspace> <path>
```

| Argument | Required | Description |
|----------|----------|-------------|
| `workspace` | Yes | Workspace ID |
| `path` | Yes | Remote VFS file path |

| Flag | Default | Description |
|------|---------|-------------|
| `--output` | `-` | Output file path or stdout |
| `--json` | `false` | Pretty-print the raw API response |

**Behavior:**

1. Calls `GET /v1/workspaces/{workspaceId}/fs/file?path=<path>`.
2. Prints file content to stdout by default.
3. Decodes `encoding: "base64"` content before writing to stdout or `--output`.
4. With `--json`, prints the raw response formatted as JSON.

---

### `relayfile seed`

Bulk-upload a local directory into a workspace.

```
relayfile seed <workspace> [dir]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | Yes | — | Workspace name or ID |
| `dir` | No | `.` (cwd) | Local directory to upload |

| Flag | Default | Description |
|------|---------|-------------|
| `--exclude` | (none) | Glob patterns to exclude (repeatable) |
| `--dry-run` | `false` | List files that would be uploaded without uploading |
| `--batch-size` | `50` | Files per bulk API request |

**Behavior:**

1. Walk `dir`, respecting `.gitignore` and `--exclude` patterns.
2. For each batch of files, call `POST /v1/workspaces/{workspaceId}/fs/bulk`.
3. Print progress: `[seed] 142/350 files uploaded...`
4. On completion: `[seed] done — 350 files uploaded to ws_abc123`.

**Uses the Bulk Seed API** defined in `docs/bulk-export-design.md`.

---

### `relayfile export`

Download a workspace snapshot.

```
relayfile export <workspace> [--format tar|json|patch] [--output file]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `tar` | Export format |
| `--output` | stdout | Output file path (or `-` for stdout) |
| `--path` | `/` | Export only files under this path |

**Behavior:**

1. Resolve workspace.
2. `GET /v1/workspaces/{workspaceId}/fs/export?format=<format>&path=<path>`.
3. Stream response to `--output` or stdout.
4. Print to stderr: `[export] downloaded 42 files (1.3 MB) from ws_abc123`.

**Uses the Workspace Export API** defined in `docs/bulk-export-design.md`.

---

### `relayfile status`

Show workspace sync status including per-provider state.

```
relayfile status [workspace] [--json]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | No | default workspace | Workspace name or ID |

**Behavior:**

1. Resolve workspace (uses default if omitted).
2. Read Cloud `/sync` endpoint and local `.relay/state.json`.
3. Print one-screen summary:
   ```
   workspace ws_abc (my-project)   mode: poll   lag: 4s
     github       ready    2,400 files   last event 2s ago
     notion       ready      214 files   last event 4s ago
     linear       error    last error: 401 invalid_grant (3 min ago)

   pending writebacks: 0    conflicts: 0    denied: 12
     see .relay/permissions-denied.log for denied paths
   ```
4. If `webhookHealthy=false` and `lagSeconds > 60` for any provider, add a warning row.

`--json` emits the same shape machine-readably.

---

### `relayfile integration`

Manage provider integrations after initial setup.

```
relayfile integration connect <provider> [--workspace NAME] [--no-open] [--timeout 5m]
relayfile integration list      [--workspace NAME] [--json]
relayfile integration disconnect <provider> [--workspace NAME] [--name SLOT] [--yes]
relayfile integration set-metadata <provider> (KEY=VALUE... | --metadata-file FILE) [--workspace NAME] [--yes]
```

**Event discovery** (`events`, `describe`):

```
relayfile integration events   <provider> [--subscribable] [--json] [--refresh] [--cloud-api-url URL]
relayfile integration describe <provider> [--event EVENT] [--subscribable] [--json] [--refresh] [--cloud-api-url URL]
```

Both read the provider's `events` list from the cloud integration catalog
(`GET /api/v1/integrations/catalog?dynamic=true`, cached like
`integration available`). The CLI ships no per-provider event list: when the
catalog entry has no `events`, both commands fail with
`event catalog not published by <cloud-api-url> for <provider>; ...` and exit
code 3. `integration available --search` also matches event names, so
`relayfile integration available --search comment.created` finds `jira`.

`events` prints one row per event, sorted by name (values below are
placeholders, not the published Jira event set):

```
event            native           object   action   support       mode                   backend  paths
comment.created  comment_created  comment  created  subscribable  oauth-dynamic-webhook  nango    /jira/issues/*/comments/*.json
project.created  project_created  project  created  known-only    -                      nango    /jira/projects/*.json
source: <eventsSource> version: <eventsVersion> backend: <eventsBackend>
```

(The real output is tab-separated; `support` is `unknown` when the catalog does
not say.) `support` rates one thing: whether the backend can register an
**exact provider-event** (`event_types`) subscription for that event.
`support: known-only` means the adapter recognizes the event but no backend
path registers it as an exact typed subscription; `reason` says why. It does
not mean the provider's webhooks or path-glob delivery of the listed paths are
unavailable: path-glob subscriptions to those paths (existing runtime bridges
that subscribe by path) are a separate mechanism that this status neither
assesses nor disables, and the human output says so in a trailing `note:`
line. That mechanism is receiver-version dependent, and the note says so too:
catalog paths with an embedded wildcard (for example
`/jira/issues/*/comments/*.json`) only match on receivers that carry the
segment matcher, which shipped with the `?capabilities=eventTypes` probe
(relayfile-cloud #414); an older receiver matches exact paths and trailing
`*`/`**` only, so such a path silently never delivers there. The CLI does not
probe the receiver, so it never represents a catalog path glob as live on a
given receiver. Durable resource subscriptions are different again: their `resourceRef`
must be one exact canonical by-id leaf plus `eventTypes`, so a catalog event
path glob or a candidate handoff `resource` can never be used directly as a
durable `resourceRef`. Webhook subscriptions take `pathGlobs` selectors; the
provider-event runtime handoff reports `available: false` because its readiness
is unconfirmed for the session, not because the runtime is unsupported (see
`describe` below). Each event's
`schema` is the catalog's `{status, reason}` object (`status: unknown` means no
typed payload capture exists, and the CLI never presents its example as the
schema). `--subscribable` keeps only events with `support: subscribable`.

Coverage is whatever the cloud catalog publishes: today that is the providers
with connection metadata (a subset of the adapters in relayfile-adapters, whose
own trigger catalog is far larger). A provider absent from the cloud catalog
is not discoverable here, and the CLI carries no hardcoded fallback list of
providers or events.

`describe` adds what a subscriber needs: `--event` accepts the normalized name
or any native name (`--event comment_created` resolves to `comment.created`).
Its sections are the provider entry, the events, subscription options (the
durable-subscription request fields from
`openapi/relayfile-v1.openapi.yaml#CreateOrRenewDurableResourceSubscriptionRequest`
and the relayfile-cloud webhook-subscription fields, including the
`?capabilities=eventTypes` probe), handoff values (one
`{provider, resource, event_types}` entry per delivered path glob, built only
from events the catalog marks `subscribable`; every other selected event is
listed under `handoffExcluded` with its support level and reason so a
known-only event stays describable but never becomes a subscribe request the
backend cannot register; this is the exact body of the runtime
`POST /integrations/subscribe` request, which the CLI does not send), the
runtime readiness contract, the delivered
`FilesystemEvent` envelope, and an illustrative example event (labelled as
such; not a captured payload or schema).

`runtime.subscribe.available` is always `false` and its `reason` says why:
the CLI has no runtime client, so exact provider-event readiness is
*unconfirmed for this session*, never "the runtime does not support it". The
`contract` block documents how a subscribing agent establishes readiness.
There is no version to read: adding an optional parameter changes neither the
relay `bridge_version` nor `hello.methods`, so the only positive signal is the
echo. `contract.readiness` spells it out: send `event_types` on the
current-session socket `POST /integrations/subscribe` (preferred, because it
rejects unknown request keys, so a runtime without the feature fails the
request rather than silently dropping the filter) and require the subscribe
response (and later setup status) to echo the same canonical set. The runtime
normalizes the list before echoing it (Jira aliases such as `comment_created`
map to `comment.created`; entries are sorted and deduplicated; at most 32), so
the comparison is set equality after that normalization, not request order.
Unconfirmed means `event_types` is absent, the canonical set differs, or the
request is rejected: the runtime did not honour the filter. An older bridge
path may have created an *unfiltered* subscription and omitted the echo, so the
agent fails closed: unsubscribe that resource immediately, never leave a broad
subscription active, and never fall back to an unfiltered subscription. That
check is the subscribing agent's, run per session at subscribe time; the CLI
performs no probe, never sends `event_types`, and keeps no second catalog.
`contract.source` names the relay runtime change that defines the request and
echo (relay PR 2087). `--json`
emits the same sections as keys
(`provider`, `events`, `subscription`, `handoff`, `handoffExcluded`, `runtime`,
`envelope`, `example`). `describe --subscribable` on a provider whose published
events are all known-only returns empty `events`/`handoff` and omits `example`,
matching `events --subscribable`; exit code 3 is reserved for a provider whose
catalog publishes no event list. Catalog loading for `events` and `describe` is
strict: a transport failure, a non-2xx status (including 401/403/503) or a
malformed body is a general error (exit 1) with no discovery output, and the
local metadata-only catalog that `integration available` falls back to is never
substituted, so a failed request cannot masquerade as an unknown provider or as
missing event data. A dynamic catalog cache written by an older CLI (before
events were decoded) is refetched instead of being trusted for the rest of its
TTL.

`set-metadata` updates the existing connection through Cloud; it does not
reconnect the provider. `KEY=VALUE` values remain strings. Use
`--metadata-file` for a JSON object containing arrays, booleans, or nested
objects, such as GitLab project selection. The preview masks secret/token
fields, including nested fields. Exactly one input form is required. JSON files
must be regular files with no group or other permissions (`chmod 600`). The
file form currently requires Unix; Windows is refused until private ACLs can
be validated.

For a GitLab bot with Developer access, select its project and use an
externally managed hook. Generate the matching secret locally:

```bash
node --input-type=module <<'NODE'
import { randomBytes } from 'node:crypto';
import { writeFileSync } from 'node:fs';
writeFileSync('gitlab-settings.json', JSON.stringify({
  projectIds: ['12345'], // Replace with the intended GitLab project ID.
  manageWebhooks: false,
  webhookSecret: randomBytes(32).toString('hex'),
}, null, 2), { mode: 0o600, flag: 'wx' });
NODE
relayfile integration set-metadata gitlab --workspace demo --metadata-file gitlab-settings.json
```

The same command is exposed as
`agent-relay file integration set-metadata gitlab --workspace demo --metadata-file gitlab-settings.json`.
Both use the normal Cloud login and workspace authorization. In the current
Cloud GitLab route, saving selection requests the selected-project syncs. A
project Maintainer must still install the webhook using the same secret and
the Cloud webhook URL; manual mode does not create a GitLab hook. Keep the
settings file private. Review existing metadata before changing a configured
connection: replacement and merge behavior is owned by the provider's Cloud
route, and an unsuccessful save may have partial effects.

**`connect` behavior:**

1. Call `POST /api/v1/workspaces/{id}/integrations/connect-session` with the provider.
2. Print the `connectLink`; open browser unless `--no-open`.
3. Poll `GET /api/v1/workspaces/{id}/integrations/{provider}/status` every 2 s.
   Before step 1 the CLI records a baseline from the same status endpoint so a
   ready row that already existed (for example a stale earlier connection) is
   not mistaken for this attempt. A connected status is accepted only when
   Cloud binds it to the minted `connectionId` (`connectionMatched: true` with
   a matching `currentConnectionId`), or when it evidences a connection newer
   than the baseline (a different `currentConnectionId`, or an
   `oauth.lastAuthAt` / `oauth.connectedAt` strictly later than the baseline's
   own parseable value for that field; a timestamp the baseline lacked is not
   comparable and proves nothing). When the baseline cannot be read,
   only a Cloud-bound status is accepted. The CLI never silently falls back to
   a pre-existing connection id. The mint response carries no attempt id, so
   this is post-baseline evidence, not attempt correlation.
4. On ready: `<provider> connected. Files will appear under <local-dir>/<provider> within ~30s.`
5. On timeout (default 5 m): print progress hint and exit 0 (sync continues in background).
6. Persist Nango connection ID to `.relay/integrations/<provider>.json`.
7. Does **not** require remounting. The running mount loop picks up new files on the next cycle.

**`list` output:**

```
provider     status    lag     last event
github       ready     2s      just now
notion       syncing   18s     5s ago
linear       error     —       401 invalid_grant (3 min ago)
```

**`disconnect` behavior:**

1. Prompt for confirmation unless `--yes`.
2. `DELETE /api/v1/workspaces/{id}/integrations/{provider}/status`. With
   `--name SLOT` the request carries
   `?scopeKind=workspace_service_account&scopeName=SLOT` and removes only
   that named service-account row; without it only the default (unnamed)
   connection is removed. Cloud never deletes named rows implicitly.
3. Verify the disconnect before touching local state. A 2xx alone is not
   proof: Cloud answers success for an already-absent row, and a
   `localDeleted: false` body means the row the caller meant survived. The
   CLI fails with `local mirror left untouched` when Cloud reports
   `localDeleted: false`. Otherwise it re-reads `GET .../status` for the same
   scope and proceeds only on an affirmative absent answer: a scoped 404, or a
   status object with `ready: false`, no `currentConnectionId` (or legacy
   `connectionId`), `oauth.connected: false` and state `pending` (the Cloud
   shape for a missing row) or `disconnected`. An empty body, `null`, `{}`,
   unrelated JSON, a missing `ready`/`state`, an unrecognized state, another
   provider, or any surviving connection signal is an error, not a success;
   `connectionMatched: false` never hides those signals. Against a Cloud that
   predates the scoped route, this check reports the surviving named row but
   cannot undo an unscoped deletion that already happened.
4. Default connection only: remove `<local-dir>/<provider>/` from the mirror
   and write the `.relay/disconnected/<provider>.json` marker. A `--name`
   disconnect leaves the mirror unchanged. Only an omitted `--name` selects
   the default connection; an explicitly empty value (`--name ""`, `--name=`)
   is a usage error raised before any credential lookup, Cloud request or
   local change, even with `--yes`.
5. Default connection only: list the provider's named rows that remain and
   print them with the `--name` command needed for each. Named rows are not
   removed by an unscoped disconnect.

---

### `relayfile pull`

Force a reconcile of a path or the entire workspace, discarding stale remote-deleted files.

```
relayfile pull [PATH]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `PATH` | No | workspace root | Subtree or file to refresh |

**Behavior:**

1. Re-fetch the given subtree from the Cloud VFS.
2. Discard local copies of files no longer present remotely (unless locally dirty).
3. Locally modified (dirty) files are **not** discarded; if a remote change would overwrite them, they land in `.relay/conflicts/`.

---

### `relayfile permissions`

Show writeback rules for a path.

```
relayfile permissions [PATH]
```

Reads `/<provider>/_PERMISSIONS.md` and the catalog for the given path (or workspace root). Prints which paths are writable and the expected schema.

---

### `relayfile ops`

Inspect and replay dead-lettered writeback operations.

```
relayfile ops list   [--workspace NAME] [--json]
relayfile ops replay <opId>
```

**`list` output:**

```
opId        path                                 code               attempts   created
op_abc123   github/.../review.json               validation_error   3          2026-05-02T18:00Z
```

**`replay` behavior:**

1. `POST /v1/workspaces/{id}/ops/{opId}/replay`.
2. On success: delete `.relay/dead-letter/<opId>.json` and print `op_abc123 replayed successfully`.
3. On failure: print the error and leave the dead-letter file in place.

---

### `relayfile stop`

Signal a background mount daemon to shut down.

```
relayfile stop [workspace]
```

Sends SIGTERM to the PID in `.relay/mount.pid`. The daemon flushes pending writes (≤5 s) and exits cleanly.

### `relayfile logs`

Print the detached mount log for a workspace.

```
relayfile logs [workspace] [--lines 40]
```

Reads `<local-dir>/.relay/mount.log` and prints the requested tail.

---

## Global flags

These flags apply to all commands:

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--server` | `RELAYFILE_SERVER` | from credentials | Server base URL |
| `--token` | `RELAYFILE_TOKEN` | from credentials | Bearer token |
| `--workspace` | `RELAYFILE_WORKSPACE` | from workspaces.json default | Workspace name or ID |
| `--json` | — | `false` | Output JSON instead of human-readable text |
| `--verbose` | — | `false` | Enable debug logging to stderr |

---

## Config directory structure

```
~/.relayfile/
  credentials.json          # self-hosted/API-key credentials only (0600)
  workspaces.json           # local workspace metadata: names, ids, localDir, history

<local-dir>/
  <provider>/               # one top-level dir per connected provider
  .relay/
    state.json              # sync state (updated atomically after each cycle)
    mount.pid               # PID of background daemon
    mount.log               # daemon log (rotated at 10 MB × 3)
    conflicts/              # one file per unresolved conflict
      resolved/             # cleared conflicts (audit trail)
    dead-letter/            # one JSON per permanently failed writeback op
    disconnected/           # markers for removed integrations
    permissions-denied.log  # append-only denial log
    integrations/
      <provider>.json       # Nango connectionId per provider
```

---

## Token resolution

The CLI resolves tokens in this order, first match wins:

1. `--token` flag
2. `RELAYFILE_TOKEN` environment variable
3. self-hosted only: `~/.relayfile/credentials.json` → `token` field

If no token is found, the CLI prints:

```
Error: not authenticated. Run 'npx relayfile@latest' for Cloud or set RELAYFILE_TOKEN.
```

An existing Agent Relay installation may use `agent-relay cloud login` as a
legacy alternative.

---

## Server URL resolution

1. `--server` flag
2. `RELAYFILE_SERVER` environment variable
3. `~/.relayfile/credentials.json` → `server` field
4. Default: `https://api.relayfile.dev`

---

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error (network, server error, invalid input) |
| 2 | Authentication failure (no token, token rejected) |
| 3 | Resource not found (workspace doesn't exist; `integration events`/`describe` provider publishes no event catalog) |
| 10 | Cloud login failed (OAuth state mismatch or error) |
| 11 | Cloud login timed out |
| 20 | Workspace create/join failed |
| 30 | Integration connect failed |
| 31 | Integration not ready before deadline |
| 40 | Initial sync not ready before deadline |
| 50 | Mount initialization failed (includes FUSE unavailable) |
| 130 | Interrupted (Ctrl+C) |

---

## Implementation plan

### Phase 1 — Core (shipped)

1. `relayfile login` — API key auth, credential storage
2. `relayfile mount` — wraps existing `mountsync` engine
3. `relayfile seed` — wraps bulk API
4. `relayfile export` — wraps export API
5. `relayfile status` — workspace info

### Phase 2 — Workspace management (shipped)

6. `relayfile workspace create`
7. `relayfile workspace list`
8. `relayfile workspace delete`

### Phase 3 — Cloud productization (v1, current)

9. `relayfile setup` / `relayfile` — guided Cloud setup wizard
10. `relayfile integration connect/list/disconnect` — multi-provider management
11. `relayfile status` extended — per-provider sync state, webhook health, conflict counts
12. `relayfile mount --background` + `relayfile stop` — daemon mode
13. `relayfile pull` — force reconcile
14. `relayfile permissions` — writable path inspection
15. `relayfile ops list/replay` — dead-letter visibility and recovery
16. Cloud token refresh + VFS token rejoin (automatic, inside mount daemon)
17. `.relay/state.json`, `.relay/conflicts/`, `.relay/dead-letter/` directories

### Phase 4 — Polish (planned)

18. `relayfile doctor` — diagnose connectivity and auth issues
19. Auto-update / version check
20. Shell completions (bash, zsh, fish)
21. `relayfile install-service` — launchd/systemd/Windows service (out of scope for v1)

### Build target

The CLI compiles to a single static binary via `cmd/relayfile-cli/main.go` using Go's `cobra` or `flag` subcommand pattern. Distribution via:

- `go install github.com/agentworkforce/relayfile/cmd/relayfile-cli@latest`
- GitHub Releases (goreleaser)
- Homebrew tap (future)

---

## Relationship to existing binaries

| Binary | Purpose | Status |
|--------|---------|--------|
| `cmd/relayfile/main.go` | HTTP API server | Unchanged |
| `cmd/relayfile-mount/main.go` | Standalone mount daemon | Kept for backwards compat |
| `cmd/relayfile-cli/main.go` | **New** — user-facing CLI | This design |

The CLI imports `internal/mountsync` directly for the `mount` subcommand rather than shelling out to `relayfile-mount`.

---

## Examples

```bash
# First-time setup
relayfile
# Opens Relayfile Cloud login
# Opens the selected integration connect flow
# Starts syncing files into ./relayfile-mount

# Token-first self-hosted setup
relayfile login --server https://api.relayfile.dev
# API key: ********
# Stored credentials for https://api.relayfile.dev

# Seed a project
relayfile seed my-workspace ./src
# [seed] 350 files uploaded to ws_abc123

# Mount and watch
relayfile mount my-workspace ./local-mirror
# [mount] syncing ws_abc123 -> ./local-mirror
# [mount] + /src/main.go (1.2 KB)
# [mount] waiting for changes...
# ^C
# [mount] stopped

# Export a snapshot
relayfile export my-workspace --format tar --output backup.tar
# [export] downloaded 350 files (4.2 MB) from ws_abc123

# Check status
relayfile status my-workspace
# Workspace:  my-workspace (ws_abc123)
# Files:      350
# Last event: 2 minutes ago

# CI/CD usage (no login needed)
RELAYFILE_TOKEN=eyJ... relayfile seed my-workspace ./dist
```
