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

**[Relayfile Cloud](https://relayfile.dev/pricing)** — everything managed. Sign up, get a token, connect your services from the dashboard.

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
# FUSE mount (real-time)
relayfile mount ws_123 ./files --token $TOKEN

# Polling sync (no FUSE required)
go run ./cmd/relayfile-mount --workspace ws_123 --local-dir ./files --interval 2s --token $TOKEN
```

## CLI Inspection

Inspect the remote VFS directly without mounting:

```bash
relayfile login --server https://api.relayfile.dev --token "$RELAYFILE_TOKEN"
relayfile tree ws_123 /github --depth 5
relayfile read ws_123 /github/repos/acme/api/pulls/42/metadata.json
relayfile tree ws_123 /github --depth 5 --json
relayfile read ws_123 /external/blob.bin --output blob.bin
```

## License

MIT
