# File by Agent Relay: integration filesystem for agents

Turn files into the integration surface for agents.

Relayfile exposes a workspace as a simple virtual file tree. Agents read with `cat`, write by saving files, and relayfile records the operations, permissions, events, and writeback queue behind that tree.

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

Use the `setting-up-relayfile` skill from [AgentWorkforce/skills#28](https://github.com/AgentWorkforce/skills/pull/28) when an agent should set up hosted files:

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
