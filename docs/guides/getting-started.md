# Getting Started

Relayfile normalizes SaaS webhooks — from Linear, Notion, GitHub, Slack, and others — into a consistent file tree, then delivers them to your agents. Agents react to events using `cat`, `grep`, and file-write operations rather than provider-specific SDKs or webhook parsers.

Relayfile runs as a background daemon (the integration runtime). The daemon is where OAuth stays warm, rate limits are absorbed, and webhook deliveries are deduplicated before your agents ever see them. You can run it locally or let [Agent Relay Cloud](#hosted-provider-files) run it for you.

This repo is the file server and mount layer. For the rest of the ecosystem, see [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters) (`../relayfile-adapters` locally) for path mapping, webhook normalization, and writeback behavior, and [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers) (`../relayfile-providers` locally) for provider auth, API proxying, subscriptions, and connection health.

## Hosted Provider Files (Least Friction)

If you want Notion, Slack, Linear, GitHub, or other provider-backed files without running any infrastructure, start here. Agent Relay Cloud runs the daemon, OAuth, sync workers, and writeback workers for you.

```bash
relayfile setup \
  --provider notion \
  --workspace my-agent \
  --local-dir ./relayfile-mount \
  --no-open
```

That command connects to `agentrelay.com`, completes provider auth, waits for sync, and mounts provider files locally. Your agent reads and reacts to the mounted files; the integration stack is hosted.

## Local OSS Quickstart

```bash
cd docker
docker compose up --build
```

This starts relayfile on `http://localhost:9090`, relayauth on `http://localhost:9091`, and creates a sample workspace named `ws_demo`.

```bash
TOKEN="$(docker compose logs seed | awk '/token/ {print $NF}' | tail -1)"

curl -H "Authorization: Bearer $TOKEN" \
  -H "X-Correlation-Id: quickstart-tree" \
  "http://localhost:9090/v1/workspaces/ws_demo/fs/tree?path=/"
```

Mount the workspace:

```bash
cd ..
RELAYFILE_TOKEN="$TOKEN" go run ./cmd/relayfile-mount \
  --base-url http://localhost:9090 \
  --workspace ws_demo \
  --local-dir ./relayfile-mount
```

Your agent can now read and write files under `./relayfile-mount`.

## Run The Server Directly

For local development without Docker, start the local token issuer:

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
go run ./cmd/relayfile-cli mount "$RELAYFILE_WORKSPACE" ./relayfile-mount
```

## Common Local Commands

```bash
# Read remote tree and files without mounting
go run ./cmd/relayfile-cli tree ws_demo /
go run ./cmd/relayfile-cli read ws_demo /docs/welcome.md

# One-shot sync for CI
go run ./cmd/relayfile-cli mount ws_demo ./relayfile-mount --once

# Export a workspace
go run ./cmd/relayfile-cli export ws_demo --format tar --output ws_demo.tar
```

## Fully Self-Hosted Provider Files

Relayfile alone is the VFS API. For provider-backed files end to end, self-host:

- relayfile
- relayauth or another JWT issuer compatible with relayfile scopes
- the [adapters](https://github.com/AgentWorkforce/relayfile-adapters) and [providers](https://github.com/AgentWorkforce/relayfile-providers) you need
- Nango, if you want OAuth-backed provider sync/writeback without using hosted Agent Relay

Keep third-party credentials in the provider layer. Relayfile should receive normalized files, webhooks, writeback operations, and `connectionId` metadata; it should not become the OAuth credential store.

## Next Steps

- [Cloud integration](cloud-integration.md)
- [Agent VFS usage](agent-vfs-usage.md)
- [API reference](../api-reference.md)
- [Environment variables](../environment-variables.md)
