# relayfile

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
  "http://localhost:9090/v1/workspaces/ws_demo/fs/tree?path=/"

curl -H "Authorization: Bearer $TOKEN" \
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

```bash
RELAYFILE_BACKEND_PROFILE=durable-local \
RELAYFILE_DATA_DIR=.data \
RELAYFILE_JWT_SECRET=dev-secret \
go run ./cmd/relayfile
```

In another terminal:

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

## Hosted Agent Relay

The OSS repo is enough to run relayfile locally. If you want an agent to get provider-backed files from hosted `agentrelay.com` without self-hosting OAuth, use the `setting-up-relayfile` skill from [AgentWorkforce/skills#28](https://github.com/AgentWorkforce/skills/pull/28).

Local OSS:

```bash
docker compose -f docker/docker-compose.yml up --build
go run ./cmd/relayfile-cli seed ws_demo ./examples
go run ./cmd/relayfile-cli mount ws_demo ./relayfile-mount
```

Hosted cloud with the skill:

```bash
relayfile setup \
  --provider notion \
  --workspace my-agent \
  --local-dir ./relayfile-mount \
  --no-open
```

Both paths give the agent files. The difference is who runs the integration layer:

| Need | Use local OSS | Use hosted Agent Relay |
|---|---:|---:|
| Local VFS API | yes | yes, managed |
| Local directory mirror | yes | yes |
| Seed files from disk | yes | yes |
| OAuth for Notion, Slack, Linear, GitHub | self-host it | managed |
| Provider sync and writeback workers | self-host them | managed |
| Nango | self-host if you want end-to-end OAuth | managed |

For end-to-end self-hosting of provider-backed files, run relayfile, relayauth, the relevant adapters/providers, and Nango. Relayfile itself does not store third-party OAuth credentials.

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
- [API reference](docs/api-reference.md)
- [Docker quickstart](docker/README.md)
- [OpenAPI spec](openapi/relayfile-v1.openapi.yaml)
