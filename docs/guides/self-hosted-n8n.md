# Self-Hosted Stack: n8n + Relayfile

Run your entire agent integration stack on your own infrastructure. n8n handles webhook ingestion and normalization; relayfile handles the agent data layer. No data leaves your environment.

## Architecture

```
Provider webhook
  → n8n (self-hosted) receives and normalizes
  → Relayfile Write node pushes event to workspace
  → relayfile (self-hosted) materializes file
  → relayfile listen fires
  → agent reacts with full context
```

Agent write-back flows in reverse:

```
Agent writes file
  → relayfile event fires
  → Relayfile Trigger node
  → n8n continues downstream automation
```

## Prerequisites

- n8n self-hosted ([n8n installation docs](https://docs.n8n.io/hosting/))
- Docker or a Go environment for relayfile
- A JWT issuer for relayfile auth (relayauth ships in the Docker stack)

## Start relayfile

```bash
git clone https://github.com/AgentWorkforce/relayfile
cd relayfile/docker
docker compose up --build
```

This starts relayfile on `http://localhost:9090` and relayauth on `http://localhost:9091`. A sample workspace `ws_demo` is created automatically.

Get a token:

```bash
TOKEN="$(docker compose logs seed | awk '/token/ {print $NF}' | tail -1)"
```

For production, set `RELAYFILE_DATA_DIR` to a persistent volume and configure your own JWT issuer via `RELAYAUTH_JWKS_URL`.

## Install the relayfile n8n node

In your n8n instance, install the community node:

```bash
# In your n8n data directory
npm install @agentworkforce/n8n-nodes-relayfile
```

Or via the n8n UI: **Settings → Community Nodes → Install** → search `@agentworkforce/n8n-nodes-relayfile`.

Restart n8n after installation.

## Configure the relayfile credential in n8n

1. In n8n, go to **Credentials → New**
2. Search for **Relayfile**
3. Set:
   - **Base URL:** `http://localhost:9090` (or your relayfile host)
   - **Token:** your workspace token
   - **Workspace ID:** `ws_demo` (or your workspace name)

## Add a Relayfile Write node to a workflow

Any n8n workflow can write events into relayfile. Add the **Relayfile Write** node as the final step:

```
[Trigger: Linear webhook]
  → [normalize / filter nodes]
  → [Relayfile Write]
      Provider: linear
      Path: /linear/issues/{{$json.identifier}}.md
      Content: {{$json}}
```

When the workflow runs, the file materializes in your relayfile workspace and a file event fires.

## React with relayfile listen

On any machine with access to your relayfile instance:

```bash
RELAYFILE_TOKEN="$TOKEN" relayfile listen \
  --path "/linear/issues/by-state/triage/**" \
  --event file.created \
  --run "claude --print 'New triage issue at {{path}}. Assign it.'"
```

To survive reboots:

```bash
relayfile supervisor install \
  --path "/linear/issues/by-state/triage/**" \
  --event file.created \
  --run "claude --print 'New triage issue at {{path}}. Assign it.'"
```

## Use the Relayfile Trigger node (reverse direction)

To kick off an n8n workflow when an agent writes a file back:

1. Add a **Relayfile Trigger** node as the workflow start
2. Set the path glob to watch: `/linear/issues/**`
3. Connect downstream nodes (send a Slack message, update a sheet, etc.)

The trigger fires whenever relayfile emits a file event matching the glob — including agent write-backs.

## Data residency

In this configuration, no provider data transits any external service. The flow is:

```
Provider → your n8n → your relayfile → your agent
```

All state is stored in `RELAYFILE_DATA_DIR` on your own infrastructure. Token storage and OAuth management are handled by your own relayauth instance or a compatible JWT issuer.

## Next steps

- [Environment variables](../environment-variables.md)
- [Getting started](getting-started.md)
- [API reference](../api-reference.md)
