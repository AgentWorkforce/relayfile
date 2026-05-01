# RelayFile CLI — Design Doc

**Status:** Draft
**Date:** 2026-03-24

---

## Overview

The `relayfile` CLI is the primary interface for humans and CI systems to interact with RelayFile workspaces. It wraps the RelayFile HTTP API and the existing `relayfile-mount` sync engine into a single, ergonomic command-line tool.

### Design principles

- **Minimal flags, sensible defaults.** The happy path should require as few arguments as possible.
- **Config file over flags over env vars.** Credentials and server URL are stored once via `relayfile login`; individual commands inherit them automatically. Environment variables (`RELAYFILE_TOKEN`, etc.) override config for CI/CD use.
- **Composable with pipes and scripts.** All commands emit structured JSON when `--json` is passed; human-readable tables otherwise.
- **No implicit destructive actions.** Deletes require confirmation unless `--yes` is passed.

---

## Authentication

### Supported methods (in priority order)

| Priority | Method | Use case |
|----------|--------|----------|
| 1 | `--token` flag | One-off override |
| 2 | `RELAYFILE_TOKEN` env var | CI/CD pipelines |
| 3 | `~/.relayfile/credentials.json` | Interactive use after `relayfile login` |

### Auth flow: API key (v1)

```
relayfile login --server https://api.relayfile.dev
```

1. Prompts the user for an API key (paste from dashboard or generate via API).
2. Validates the key by calling `GET /health` on the target server.
3. Writes `~/.relayfile/credentials.json`.

### Auth flow: OAuth via browser (future, v2)

Same as `gh auth login` — opens a browser, completes device-code flow, stores refresh token. Not in scope for v1; the credential file schema reserves fields for it.

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
- `refreshToken` / `expiresAt` — reserved for OAuth flow (v2). Null for API-key auth.
- File permissions: `0600` (user-only read/write).

---

## Commands

### `relayfile` / `relayfile setup`

Run the low-friction Cloud setup path for humans and agent-guided onboarding.

```
relayfile
relayfile setup [--provider github] [--workspace my-project] [--local-dir ./relayfile-mount]
```

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

1. Start a localhost callback and open the Cloud login URL at `/api/v1/cli/login`.
2. Store Cloud login tokens in `~/.relayfile/cloud-credentials.json`.
3. Create and join a Cloud workspace, then store the Relayfile VFS URL/token in `~/.relayfile/credentials.json`.
4. Request a hosted Nango connect session for the selected integration and wait until the Cloud status endpoint reports it ready.
5. Start the existing `relayfile mount` sync loop so the user and agent see ordinary files.

This path is intentionally a wrapper over the lower-level commands and Cloud APIs. Existing `login`, `workspace`, `mount`, `tree`, and `read` commands remain available for CI, self-hosted servers, and scripted workflows.

---

### `relayfile login`

Authenticate and store credentials.

```
relayfile login [--server URL]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `https://api.relayfile.dev` | Server base URL |

**Behavior:**

1. Prompt for API key (stdin, or `--token` flag for non-interactive).
2. Call `GET <server>/health` to validate connectivity.
3. Call a token-validated endpoint (e.g., `GET /v1/workspaces` with limit=1) to verify the token is accepted.
4. Write `~/.relayfile/credentials.json` with `0600` permissions.
5. Print confirmation: `Logged in to <server> as <agent-name>`.

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

1. `POST /v1/workspaces` with `{ "name": "<name>" }`.
2. Store the workspace ID mapping in `~/.relayfile/workspaces.json`.
3. Print the workspace ID.

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

1. `GET /v1/workspaces` (paginated).
2. Print table: `ID | Name | Files | Last Activity`.
3. With `--json`, emit the raw API response.

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
| `--interval` | `2s` | Polling interval between sync cycles |
| `--once` | `false` | Run a single sync cycle and exit |
| `--provider` | (none) | Filter events by provider name |
| `--no-websocket` | `false` | Disable WebSocket streaming, poll only |

**Behavior:**

1. Resolve workspace name to ID.
2. Create `local-dir` if it doesn't exist.
3. Load or initialize state file at `<local-dir>/.relayfile-mount-state.json`.
4. Start the existing `mountsync.Syncer` engine (reuses `internal/mountsync`).
5. Run in foreground. Print sync activity to stderr:
   ```
   [mount] syncing ws_abc123 -> ./my-project
   [mount] + /src/main.go (1.2 KB)
   [mount] ~ /README.md (updated)
   [mount] waiting for changes...
   ```
6. `Ctrl+C` (SIGINT/SIGTERM) triggers graceful shutdown.

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

Show workspace status and stats.

```
relayfile status [workspace]
```

| Argument | Required | Default | Description |
|----------|----------|---------|-------------|
| `workspace` | No | default workspace | Workspace name or ID |

**Behavior:**

1. Resolve workspace (uses default if omitted).
2. Fetch workspace metadata and tree stats.
3. Print:
   ```
   Workspace:  my-project (ws_abc123)
   Server:     https://api.relayfile.dev
   Files:      142
   Last event: 2026-03-24T11:42:00Z (3 minutes ago)
   Agents:     compose-agent, reviewer-bot
   ```

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
  credentials.json    # server URL + token (0600)
  workspaces.json     # workspace name -> ID mapping + default
```

---

## Token resolution

The CLI resolves tokens in this order, first match wins:

1. `--token` flag
2. `RELAYFILE_TOKEN` environment variable
3. `~/.relayfile/credentials.json` → `token` field

If no token is found, the CLI prints:

```
Error: not authenticated. Run 'relayfile login' or set RELAYFILE_TOKEN.
```

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
| 3 | Resource not found (workspace doesn't exist) |
| 130 | Interrupted (Ctrl+C) |

---

## Implementation plan

### Phase 1 — Core (ship first)

1. `relayfile login` — API key auth, credential storage
2. `relayfile mount` — wraps existing `mountsync` engine
3. `relayfile seed` — wraps bulk API
4. `relayfile export` — wraps export API
5. `relayfile status` — workspace info

### Phase 2 — Workspace management

6. `relayfile workspace create`
7. `relayfile workspace list`
8. `relayfile workspace delete`

### Phase 3 — Polish

9. OAuth browser flow
10. Auto-update / version check
11. Shell completions (bash, zsh, fish)
12. `relayfile doctor` — diagnose connectivity and auth issues

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
