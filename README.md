# Relayfile

**The integration filesystem for agents.**

Mount Linear, Notion, GitHub, Slack, HubSpot, Salesforce, and the rest of your SaaS stack as a virtual filesystem. Every agent in your system reads them with `cat`, writes them by saving files, and coordinates through a real, ACL'd, real-time-synced filesystem. The mount can live anywhere — the SDK, CLI, and FUSE layer all let you choose where to expose it.

LLMs are far better at reading files than calling typed tools — the file system is the most-trained-on API in existence. Relayfile leans on that instead of fighting it.

```bash
$ ls mount/
github  linear  notion  slack

$ cat mount/linear/issues/AGE-12.json
{ "identifier": "AGE-12", "title": "Fix login bug", "state": "Todo", ... }

$ echo '{"description":"Updated by reviewer agent"}' \
    > mount/linear/issues/AGE-12.json   # PATCH back to Linear

$ grep -l '"state":"Todo"' mount/linear/issues/*.json
```

That's the entire interface. No new SDK to learn, no MCP schemas eating your context window — just the bash and file-IO an agent already knows.

## Why files

Three reasons:

1. **Context efficiency.** A typical MCP setup loads 100+ tool schemas into every agent session before any work happens. Files load nothing — context cost is what the agent actually opens.
2. **Completeness.** APIs return what their search ranks. `ls` returns what's there. For "what changed yesterday across these three integrations" type questions, exhaustive enumeration beats query-by-query retrieval.
3. **Coordination.** Multiple agents working through the same filesystem can see each other's writes immediately, scoped by ACL. Multi-agent collaboration becomes a property of the substrate, not something each app re-implements.

## What's in the box

- **File-native reads.** `ls`, `cat`, `grep`, `find` — the agent's native vocabulary. No tool schemas in context.
- **File-native writes.** PATCH a record by writing to its canonical path. CREATE by saving a draft filename. DELETE by removing the file. Per-resource schemas are discoverable in-tree (`<resource>/.schema.json`). See [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters).
- **Per-agent ACLs.** Scope each agent's read/write surface via `.relayfile.acl`. Agents see only the paths they should — readonly on the rest of the tree.
- **Real-time multi-agent sync.** Writes from one agent are visible to others on the next read. No commit/push/pull cycle, no merge.
- **Real OS mount.** Native bash, native `find`/`grep`/`jq`/`rg`, no emulation gaps. Any process — agents, scripts, IDEs — can read or write the mount.
- **Pluggable architecture.** A core file server plus [adapters](https://github.com/AgentWorkforce/relayfile-adapters) (per-integration logic) and [providers](https://github.com/AgentWorkforce/relayfile-providers) (auth/proxy via Nango, Composio, Pipedream). One provider integration unlocks tens of apps.

## How relayfile compares

The "give agents a filesystem" idea is a healthy direction — relayfile isn't the only project pointing at it.

**vs. MCP servers (Linear, Notion, Slack, GitHub, …).** MCP gives the agent a typed tool surface per integration. Strong for single, well-defined writes (`linear.create_issue(title, priority, …)` enforces the shape at call time). The cost is that each connected server loads tool schemas into the context window, and an LLM is more reliable reading a directory than juggling N typed APIs. Relayfile exposes the same integrations as paths you can `Read` / `Bash` / `Glob` — no schema overhead — with exhaustive enumeration where MCP returns API search results. The two compose well: relayfile for reads and synthesis, MCP for typed writes that need server-side validation.

**vs. [Mirage](https://github.com/strukto-ai/mirage) and other virtual-filesystem-for-agents projects.** Mirage is doing thoughtful work in this space and it's worth a look. Their focus is **infrastructure and storage primitives** — S3, Postgres, Redis, GDrive, GCS, Mongo, SSH — mounted side-by-side as one tree. Relayfile's focus is **integrations** — the SaaS APIs where day-to-day agent work lives — with file-native writeback (PATCH / CREATE / DELETE through file ops), per-agent ACLs, real-time multi-agent sync, and a real OS mount you can put wherever fits your stack. Different scopes, both useful; pick the one your work lives in (or run both).

**vs. rolling your own.** A single agent against a single backend can do fine with FUSE, Mountpoint, or direct SDK calls. Relayfile becomes worth it when you need multi-agent coordination, scoped capabilities, and a consistent read/write contract across many SaaS surfaces.

## Run Locally

Fastest path:

```bash
cd docker
docker compose up --build
```

This starts:

| Service | URL | Purpose |
|---|---|---|
| relayfile | `http://localhost:9090` | VFS API |
| relayauth | `http://localhost:9091` | local dev token issuer |
| seed | exits after setup | creates `ws_demo` sample files |

Try the local API:

```bash
TOKEN="$(docker compose logs seed | awk '/token/ {print $NF}' | tail -1)"

curl -H "Authorization: Bearer $TOKEN" \
  -H "X-Correlation-Id: quickstart-tree" \
  "http://localhost:9090/v1/workspaces/ws_demo/fs/tree?path=/"

curl -H "Authorization: Bearer $TOKEN" \
  -H "X-Correlation-Id: quickstart-read" \
  "http://localhost:9090/v1/workspaces/ws_demo/fs/file?path=/docs/welcome.md"
```

Mount the workspace as ordinary local files:

```bash
cd ..
RELAYFILE_TOKEN="$TOKEN" go run ./cmd/relayfile-mount \
  --base-url http://localhost:9090 \
  --workspace ws_demo \
  --local-dir ./relayfile-mount
```

Now any local tool or agent can use `./relayfile-mount` like a normal directory.

## Local Development Without Docker

Start the local token issuer:

```bash
node docker/relayauth/server.js
```

In another terminal, start relayfile:

```bash
RELAYFILE_BACKEND_PROFILE=durable-local \
RELAYFILE_DATA_DIR=.data \
RELAYAUTH_JWKS_URL=http://127.0.0.1:9091/.well-known/jwks.json \
go run ./cmd/relayfile
```

In a third terminal:

```bash
export RELAYFILE_WORKSPACE=ws_demo
export RELAYFILE_TOKEN="$(./scripts/generate-dev-token.sh "$RELAYFILE_WORKSPACE")"

go run ./cmd/relayfile-cli login \
  --server http://127.0.0.1:8080 \
  --token "$RELAYFILE_TOKEN"

go run ./cmd/relayfile-cli seed "$RELAYFILE_WORKSPACE" ./examples
go run ./cmd/relayfile-cli tree "$RELAYFILE_WORKSPACE" /
go run ./cmd/relayfile-cli mount "$RELAYFILE_WORKSPACE" ./relayfile-mount --once
```

## Ecosystem

This repo is the file server and local mount layer. The wider Agent Relay file ecosystem also includes:

- [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters) (`../relayfile-adapters` in the local workspace): maps provider webhooks and objects into relayfile paths, normalizes events, and defines writeback behavior.
- [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers) (`../relayfile-providers` in the local workspace): handles provider auth, token lookup, API proxying, webhook subscriptions, and connection health.

Hosted Agent Relay runs these pieces for you. Fully self-hosted provider-backed files need relayfile plus the adapter and provider repos for the systems you expose.

## Hosted Agent Relay

If you want Notion, Slack, Linear, GitHub, or other provider-backed files without running any infrastructure, use hosted Agent Relay. Agent Relay Cloud runs the workspace, relayfile API, scoped auth, Nango OAuth, provider sync workers, and writeback workers for you.

Use the [`setting-up-relayfile` skill](https://github.com/AgentWorkforce/skills/blob/main/skills/setting-up-relayfile/SKILL.md) when an agent should set up hosted files:

```bash
relayfile setup \
  --provider notion \
  --workspace my-agent \
  --local-dir ./relayfile-mount \
  --no-open
```

That command connects to `agentrelay.com`, creates or joins a cloud workspace, completes provider auth, waits for sync, and mounts the resulting files for the agent. The local directory is just the agent's file interface; the integration stack is hosted.

Use the OSS repo when you want to run the file server yourself. Use hosted Agent Relay when you want the whole integration path managed:

| Need | Local OSS | Hosted Agent Relay |
|---|---:|---:|
| Run relayfile locally | yes | no |
| Mount files for an agent | yes | yes |
| Provider OAuth | self-host provider auth | managed |
| Provider sync/writeback | self-host workers | managed |
| Nango | self-host for end-to-end OAuth | managed |

For end-to-end self-hosting of provider-backed files, run relayfile, relayauth, the relevant [adapters](https://github.com/AgentWorkforce/relayfile-adapters), [providers](https://github.com/AgentWorkforce/relayfile-providers), and Nango. Relayfile itself does not store third-party OAuth credentials.

## Bring Existing Connections

Relayfile tracks provider identity as `connectionId` metadata. The core OSS server can ingest events and queue writebacks with a `connectionId`, but the OAuth provider must be able to use that ID.

Nango:

- In your own self-hosted stack, point the Nango provider at your Nango host/secret and link the workspace to the existing `connectionId` and `providerConfigKey` before triggering sync.
- Hosted `agentrelay.com` currently exposes the connect-session flow through the CLI, not a public import command for arbitrary existing Nango connections. If the connection is not in Agent Relay's Nango project, re-connect through the hosted flow or run your own Nango-backed stack.

Composio:

- Use your Composio API key and the existing connected account ID as the Relayfile `connectionId`.
- The Composio provider can list/get connected accounts and proxy/write through that account.

Pipedream:

- Use your Pipedream Connect project credentials and the existing account ID as the Relayfile `connectionId`.
- For proxy calls, also provide the external user ID through headers, query, or a resolver callback.

## Docs

- [Getting started](docs/guides/getting-started.md)
- [Cloud integration](docs/guides/cloud-integration.md)
- [Adapters](https://github.com/AgentWorkforce/relayfile-adapters)
- [Providers](https://github.com/AgentWorkforce/relayfile-providers)
- [API reference](docs/api-reference.md)
- [Docker quickstart](docker/README.md)
- [OpenAPI spec](openapi/relayfile-v1.openapi.yaml)
