# Cloud Integration

Hosted Agent Relay is the managed cloud path for provider-backed files. Agent Relay Cloud runs the workspace, relayfile API, scoped auth, Nango OAuth, provider sync workers, and writeback workers. The user's machine only needs a local mount when an agent or tool wants ordinary filesystem access.

## Hosted Agent Relay

Use the `setting-up-relayfile` skill from [AgentWorkforce/skills#28](https://github.com/AgentWorkforce/skills/pull/28) when an agent needs provider-backed files from `agentrelay.com`.

```bash
relayfile setup \
  --provider notion \
  --workspace my-agent \
  --local-dir ./relayfile-mount \
  --no-open
```

The skill covers the full hosted flow: cloud login, workspace creation, provider OAuth, initial sync, local mount verification, writeback checks, and recovery guidance. No relayfile server, relayauth service, Nango instance, adapter, or worker has to run on the user's machine.

After setup, hand the agent the mount path:

```bash
export RELAYFILE_LOCAL_DIR="$PWD/relayfile-mount"
```

The agent should read and write under `$RELAYFILE_LOCAL_DIR/<provider>/...` instead of calling provider APIs directly.

## Local OSS vs Hosted Cloud

Use local OSS for development, tests, or fully self-hosted deployments. Use hosted Agent Relay when the user wants provider-backed files to work without operating the stack.

| Task | Local OSS | Hosted Agent Relay |
|---|---|---|
| Run relayfile API | user runs it | managed by Agent Relay Cloud |
| Issue scoped relayfile tokens | user runs relayauth or compatible issuer | managed |
| Mount files locally | `relayfile mount ws_demo ./relayfile-mount` | `relayfile setup --local-dir ./relayfile-mount` |
| Connect Notion/Slack/Linear/GitHub | user runs provider stack | hosted OAuth flow |
| Provider sync/writeback | user runs workers | managed |
| Nango | user self-hosts/configures it | managed |

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
