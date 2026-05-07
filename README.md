# relayfile

Turn any API into a filesystem that AI agents can read and write.

Agents don't call APIs. They read and write files. Relayfile handles webhooks, auth, and writeback so agents never need to know about the services behind the files.

```bash
# Agent reads a GitHub PR
cat /relayfile/github/repos/acme/api/pulls/42/metadata.json

# Agent writes a review
echo '{"body": "LGTM!", "event": "APPROVE"}' \
  > /relayfile/github/repos/acme/api/pulls/42/reviews/review.json

# Done. The review is posted to GitHub. The agent didn't authenticate or call any API.
```

## How It Works

```
External Services          relayfile             Your Agents
─────────────────     ─────────────────     ─────────────────
                       ┌──────────────┐
  GitHub ──webhook──▶  │              │     cat /github/...
  Slack  ──webhook──▶  │  Virtual     │◀──  echo '...' > /slack/...
  Notion ──webhook──▶  │  Filesystem  │     ls /notion/...
  Linear ──webhook──▶  │              │
                       └──────┬───────┘
                              │
                        Adapters map paths
                        Providers handle auth
                        relayauth scopes access
```

1. **Webhooks arrive** from GitHub, Slack, Notion, etc.
2. [**Adapters**](https://github.com/AgentWorkforce/relayfile-adapters) normalize the payload and map it to a VFS path
3. [**Providers**](https://github.com/AgentWorkforce/relayfile-providers) handle OAuth tokens and API proxying
4. **Agents read and write files** — that's their entire integration
5. When an agent writes to a writeback path, the adapter posts the change back to the source API

## Quick Start (Docker)

```bash
cd docker && docker compose up --build
```

This starts relayfile on `:9090` and relayauth on `:9091`, seeds a `ws_demo` workspace with sample files, and prints a dev token. See [`docker/README.md`](docker/README.md) for details.

## Getting Started

**Relayfile Cloud** — everything managed. Run the CLI, sign in with the hosted browser flow, choose an integration, and Relayfile mirrors it as ordinary files on disk for your agent:

```bash
relayfile
```

This runs a guided wizard: Cloud login, workspace name, integration provider, local directory, and OAuth consent. Files appear under `./relayfile-mount/<provider>/` within 30 seconds of OAuth completing.

For non-interactive / CI setup:

```bash
relayfile setup \
  --cloud-token "$RELAYFILE_CLOUD_TOKEN" \
  --provider github \
  --workspace my-project \
  --local-dir ./relayfile-mount \
  --no-open \
  --once
```

> **Note:** The default attachment is a **synced mirror**, not a kernel FUSE mount. Files are ordinary files on disk; a daemon polls the Cloud every 30 s and handles writeback. FUSE is available via `--mode=fuse` but is not required. See [docs/guides/vfs-cloud-setup.md](docs/guides/vfs-cloud-setup.md) for the full human guide including limitations, conflict resolution, and troubleshooting.

After setup you can add more integrations without remounting:

```bash
relayfile integration connect notion
relayfile integration connect linear
relayfile integration list
```

Check live sync state at any time:

```bash
relayfile status
```

Run the sync loop in the background:

```bash
relayfile mount --background my-project ./relayfile-mount
relayfile stop my-project
```

**Self-hosted:**

```bash
# 1. Start the server
RELAYFILE_JWT_SECRET=my-secret go run ./cmd/relayfile

# 2. Generate a token
export RELAYFILE_AGENT_NAME=compose-agent
SIGNING_KEY=my-secret ./scripts/generate-dev-token.sh

# 3. Mount a workspace
relayfile mount my-workspace ./files --token $TOKEN
```

Development tokens should include `workspace_id`, `agent_name`, and `aud: ["relayfile"]`.

## Authentication & Permissions

Tokens are scoped JWTs issued by [relayauth](https://github.com/AgentWorkforce/relayauth). The VFS paths *are* the permission boundaries — you control exactly what each agent can see and do:

```bash
# Read-only access to specific Notion pages
RELAYAUTH_SCOPES_JSON='["relayfile:fs:read:/notion/pages/product-roadmap/*", "relayfile:fs:read:/notion/pages/eng-specs/*"]'

# Code review agent: read PRs, write only reviews
RELAYAUTH_SCOPES_JSON='["relayfile:fs:read:/github/repos/acme/api/pulls/*", "relayfile:fs:write:/github/repos/acme/api/pulls/*/reviews/*"]'

# Support agent: read + reply in Slack support channel only
RELAYAUTH_SCOPES_JSON='["relayfile:fs:read:/slack/channels/support/*", "relayfile:fs:write:/slack/channels/support/messages/*"]'

# Observer: see everything, change nothing
RELAYAUTH_SCOPES_JSON='["relayfile:fs:read:*"]'
```

Scope format: `plane:resource:action:path` — supports wildcards and path prefixes.

## Ecosystem

| Repo | What it does |
|------|-------------|
| **[relayfile](https://github.com/AgentWorkforce/relayfile)** | Go server + TypeScript/Python SDKs |
| **[relayauth](https://github.com/AgentWorkforce/relayauth)** | Token issuance + scoped permissions |
| **[relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters)** | GitHub, GitLab, Slack, Teams, Linear, Notion adapters |
| **[relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers)** | Nango, Composio, Pipedream, Clerk, Supabase, n8n providers |
| **[cloud](https://github.com/AgentWorkforce/cloud)** | Cloudflare Workers deployment (managed service) |

## SDK

```bash
npm install @relayfile/sdk    # TypeScript
pip install relayfile          # Python
```

```ts
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({ token: process.env.RELAYFILE_TOKEN! });

// Read
const file = await client.getFile("ws_123", "/github/repos/acme/api/pulls/42/metadata.json");

// Write (triggers writeback to GitHub automatically)
await client.putFile("ws_123", "/github/repos/acme/api/pulls/42/reviews/review.json", {
  content: JSON.stringify({ body: "LGTM!", event: "APPROVE" }),
});

// List
const tree = await client.listFiles("ws_123", "/github/repos/acme/api/pulls/");
```

## API

Filesystem:
- `GET /v1/workspaces/{id}/fs/tree` — list files
- `GET /v1/workspaces/{id}/fs/file` — read file
- `PUT /v1/workspaces/{id}/fs/file` — write file
- `DELETE /v1/workspaces/{id}/fs/file` — delete file
- `GET /v1/workspaces/{id}/fs/query` — structured metadata query

Webhooks & Writeback:
- `POST /v1/workspaces/{id}/webhooks/ingest` — receive webhooks
- `GET /v1/workspaces/{id}/writeback/pending` — pending writebacks
- `POST /v1/workspaces/{id}/writeback/{wbId}/ack` — acknowledge writeback

Operations:
- `GET /v1/workspaces/{id}/ops` — operation log
- `POST /v1/workspaces/{id}/ops/{opId}/replay` — replay failed operation

Full spec: [`openapi/relayfile-v1.openapi.yaml`](openapi/relayfile-v1.openapi.yaml)

## Self-Hosted

```bash
# In-memory (development)
go run ./cmd/relayfile

# Durable local (persisted to disk)
RELAYFILE_BACKEND_PROFILE=durable-local RELAYFILE_DATA_DIR=.data go run ./cmd/relayfile

# Production (Postgres)
RELAYFILE_BACKEND_PROFILE=production RELAYFILE_PRODUCTION_DSN=postgres://localhost/relayfile go run ./cmd/relayfile
```

Docker Compose:

```bash
cp compose.env.example .env
docker compose up --build -d
```

## Mount

Mount a workspace as a local directory:

```bash
# Synced mirror (default, recommended)
relayfile mount ws_123 ./files

# One-shot sync (CI/probe)
relayfile mount ws_123 ./files --once

# Background daemon
relayfile mount --background ws_123 ./files
relayfile stop ws_123

# FUSE mount (opt-in, requires FUSE support)
relayfile mount ws_123 ./files --mode=fuse

# Self-hosted: polling sync without Cloud
go run ./cmd/relayfile-mount --workspace ws_123 --local-dir ./files --interval 2s --token $TOKEN
```

The default mode is a **synced mirror** (`--mode=poll`). It is not a real-time POSIX filesystem. Known differences: file handles are not stable across syncs, `mtime` reflects local write time only, directory listings can lag by up to 30 s. See [docs/guides/vfs-cloud-setup.md#known-limitations](docs/guides/vfs-cloud-setup.md#known-limitations).

**For agents:** See [docs/guides/agent-vfs-usage.md](docs/guides/agent-vfs-usage.md) and the installable skill at [docs/skills/relayfile-workspace.md](docs/skills/relayfile-workspace.md).

## CLI Inspection

Inspect the remote VFS directly without mounting:

```bash
relayfile login --server https://api.relayfile.dev --token "$RELAYFILE_TOKEN"
relayfile workspace use ws_123
relayfile tree /github --depth 5
relayfile read /github/repos/acme/api/pulls/42/metadata.json
relayfile tree /github --depth 5 --json
relayfile read /external/blob.bin --output blob.bin
```

You can also set `RELAYFILE_WORKSPACE=ws_123` instead of storing a default.

Open the hosted observer for the active workspace:

```bash
relayfile observer
relayfile observer ws_123 --no-open
```

Inspect and recover dead-lettered writeback operations:

```bash
relayfile ops list
relayfile ops replay op_abc123
```

Check which paths are writable for a provider:

```bash
relayfile permissions
relayfile permissions github/repos/acme/api/pulls/
```

## Documentation

| Guide | Audience |
|-------|----------|
| [docs/guides/getting-started.md](docs/guides/getting-started.md) | First steps with self-hosted Relayfile |
| [docs/guides/vfs-cloud-setup.md](docs/guides/vfs-cloud-setup.md) | Human guide: Cloud setup, integrations, status, daemon, troubleshooting |
| [docs/guides/agent-vfs-usage.md](docs/guides/agent-vfs-usage.md) | Agent guide: mount discovery, reads, writes, conflicts, writeback |
| [docs/guides/cloud-integration.md](docs/guides/cloud-integration.md) | Cloud workflow model for orchestrators |
| [docs/guides/collaboration.md](docs/guides/collaboration.md) | Multi-human and human+agent collaboration patterns |
| [docs/skills/relayfile-workspace.md](docs/skills/relayfile-workspace.md) | Installable agent skill (copy to `.agents/skills/`) |
| [docs/agent-workspace-governance-and-jit-integrations.md](docs/agent-workspace-governance-and-jit-integrations.md) | Consolidated agent workspace governance and just-in-time integration contract |
| [docs/per-user-governance-contract.md](docs/per-user-governance-contract.md) | Cross-repo contract for per-user governance with relayauth |
| [docs/cli-design.md](docs/cli-design.md) | CLI command reference and design |
| [docs/productized-cloud-mount-contract.md](docs/productized-cloud-mount-contract.md) | v1 product contract (normative) |
| [docs/api-reference.md](docs/api-reference.md) | REST API reference |

## Changelogs

Each publishable package keeps its own `CHANGELOG.md`:

- [`relayfile`](packages/cli/CHANGELOG.md) — CLI
- [`@relayfile/core`](packages/core/CHANGELOG.md)
- [`@relayfile/sdk`](packages/sdk/typescript/CHANGELOG.md)
- [`@relayfile/local-mount`](packages/local-mount/CHANGELOG.md)
- [`@relayfile/file-observer`](packages/file-observer/CHANGELOG.md)

**Process** — landed in every PR, finalized at release:

1. PRs that touch a package add an entry under its `## [Unreleased]` section (Keep a Changelog format: `Added` / `Changed` / `Deprecated` / `Removed` / `Fixed` / `Security`). Include the PR number as a link reference at the bottom of the file.
2. At release, the `Publish Package` workflow runs [`scripts/finalize-changelogs.mjs`](scripts/finalize-changelogs.mjs), which renames `[Unreleased]` to `[x.y.z] - YYYY-MM-DD`, opens a fresh empty `[Unreleased]` above, and rewrites the compare-link references. Prereleases skip this step so their entries accumulate until the final release.
3. Packages without user-visible changes in a given release leave `[Unreleased]` as `_No unreleased changes._`. The finalizer rewrites the dated section's body to `_No user-visible changes in this release._` so the release heading still reads naturally.

## License

MIT
