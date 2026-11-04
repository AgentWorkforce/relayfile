# Cloud Integration

Hosted Agent Relay is the managed cloud path for provider-backed files. Agent Relay Cloud runs the workspace, relayfile API, scoped auth, Nango OAuth, provider sync workers, and writeback workers. The user's machine only needs a local mount when an agent or tool wants ordinary filesystem access.

## Hosted Agent Relay

From the project where the agent will work, run the clean-machine quickstart:

```bash
npx relayfile@latest
```

Relayfile owns the full hosted flow: Google sign-in, automatic account and workspace creation, GitHub OAuth, initial sync, and the local mount. No invite code, separate Agent Relay CLI, copied token, relayfile server, relayauth service, Nango instance, adapter, or worker has to run on the user's machine.

Use the explicit form for a different provider or workspace:

```bash
npx relayfile@latest setup --provider notion --workspace my-agent --local-dir ./relayfile-mount
```

After setup, hand the agent the mount path:

```bash
export RELAYFILE_LOCAL_DIR="$PWD/relayfile-mount"
```

The agent should read and write under `$RELAYFILE_LOCAL_DIR/<provider>/...` instead of calling provider APIs directly.

### GitLab project and group tokens

To create a workspace with a project token:

```bash
relayfile setup --provider gitlab-pat --backend nango --workspace my-agent --local-dir ./relayfile-mount
```

Alternatively, connect a group token to an existing workspace that has no
GitLab connection:

```bash
relayfile integration connect gitlab-group-token --backend nango --workspace existing-workspace --wait-sync
```

Enter the token in the hosted Nango Connect page. Both variants use the logical
provider `gitlab` and the same `/gitlab` filesystem tree as OAuth. The CLI keeps
the selected auth config in saved connection metadata, polls GitLab status,
and waits for GitLab sync without creating a separate token-provider directory.
To change an existing OAuth connection to a token, disconnect it first; the
CLI does not treat an OAuth connection as satisfying an explicit token request.

Developer-scoped project tokens need a Maintainer to configure project hooks.
Follow Cloud's [GitLab token setup guide](https://github.com/AgentWorkforce/cloud/blob/trunk/docs/flows/gitlab-token-connections.md)
to select projects and configure externally managed hooks before validating a
hosted listener. A successful connection alone does not verify webhook delivery
or a listener run.

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
- [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters) (`../relayfile-adapters` locally) for the systems you expose as files
- [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers) (`../relayfile-providers` locally) for auth, API proxying, webhook subscriptions, connection health, sync, and writeback
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
- When receiving outbound workspace events, follow the [webhook delivery verification and retry guide](webhook-delivery.md).
- Use [API reference](../api-reference.md) endpoints for tree reads, file writes, webhook ingestion, writeback status, and operation replay.
