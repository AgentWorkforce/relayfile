# Cloud Integration

Relayfile can run as local OSS infrastructure or as the file layer inside hosted Agent Relay. The agent experience is the same: read files, write files, let the integration layer handle provider APIs.

## Local OSS vs Hosted Agent Relay

| Task | Local OSS | Hosted Agent Relay |
|---|---|---|
| Start relayfile | `docker compose -f docker/docker-compose.yml up --build` | managed |
| Create workspace | seeded `ws_demo` or CLI/API | `relayfile setup --workspace ...` |
| Mount files locally | `relayfile mount ws_demo ./relayfile-mount` | `relayfile setup --local-dir ./relayfile-mount` |
| Seed arbitrary local files | `relayfile seed ws_demo ./dir` | `relayfile seed <workspace> ./dir` |
| Connect Notion/Slack/Linear/GitHub | self-host provider stack | hosted OAuth flow |
| OAuth provider | run Nango yourself | managed Nango |
| Agent instructions | normal filesystem tools | use the setup skill |

## Hosted Setup With The Skill

Use the `setting-up-relayfile` skill from [AgentWorkforce/skills#28](https://github.com/AgentWorkforce/skills/pull/28) when an agent needs provider-backed files from hosted `agentrelay.com`.

```bash
relayfile setup \
  --provider notion \
  --workspace my-agent \
  --local-dir ./relayfile-mount \
  --no-open
```

The skill covers the full hosted flow: cloud login, workspace creation, Nango OAuth, initial sync, local mount verification, writeback checks, and dead-letter recovery.

After setup, hand the agent the mount path:

```bash
export RELAYFILE_LOCAL_DIR="$PWD/relayfile-mount"
```

The agent should read and write under `$RELAYFILE_LOCAL_DIR/<provider>/...` instead of calling provider APIs directly.

## Fully Self-Hosted Cloud-Like Setup

To self-host the same end-to-end shape, run these pieces together:

- relayfile API
- relayauth or compatible scoped JWT issuer
- provider adapters for the systems you expose as files
- provider workers for sync and writeback
- Nango for OAuth-backed provider credentials and provider sync state

Relayfile should remain the VFS and operation log. Nango or an equivalent provider layer should own OAuth credentials.

## Existing Connection IDs

Relayfile stores provider identity as `connectionId` metadata on files, webhook envelopes, operations, and writeback work. That lets you bring existing provider connections when the provider layer can use the ID.

### Nango

For self-hosted deployments, use the same Nango project that already contains the connection. Configure the Nango provider with the right host, secret key, and `providerConfigKey`, then link your relayfile workspace to the existing Nango `connectionId` before sync.

The hosted Agent Relay CLI currently exposes a connect-session flow, not a public import command for arbitrary Nango connections. If a connection lives in a different Nango project, hosted Agent Relay cannot use it without a migration or a fresh hosted OAuth connection.

Operationally, a self-hosted cloud-compatible link needs:

```text
workspace_id         relayfile workspace id
provider             provider id, for example notion or github
connection_id        existing Nango connection id
provider_config_key  Nango provider config key
metadata_json        readiness/sync metadata, if your worker uses it
```

Then trigger the provider's initial sync so relayfile receives files.

### Composio

Use the existing Composio connected account ID as relayfile's `connectionId`. Configure the Composio provider with your API key, verify the account with `getConnectedAccount` or `healthCheck`, and have your adapter/proxy calls use that ID for read/writeback.

### Pipedream

Use the existing Pipedream account ID as relayfile's `connectionId`. Configure the Pipedream provider with your Connect project credentials. Proxy calls also need the external user id, supplied through `x-pd-external-user-id`, `external_user_id`, or a resolver callback.

## Operational Notes

- Use one workspace per isolated task or run.
- Reuse a workspace when humans and agents need a shared persistent project tree.
- Keep OAuth credentials out of relayfile. Store them in Nango, Composio, Pipedream, or your own provider layer.
- Use [API reference](../api-reference.md) endpoints for tree reads, file writes, webhook ingestion, writeback status, and operation replay.
