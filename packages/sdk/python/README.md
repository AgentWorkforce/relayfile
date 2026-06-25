# relayfile

Python SDK for the RelayFile virtual filesystem API.

## Install

```bash
pip install relayfile-sdk
```

The import namespace remains `relayfile`.

## Cloud Setup

The Python SDK exposes the portable Relayfile Cloud setup flow from the
TypeScript SDK. Interactive browser login and native mount launching remain
TypeScript/Node-only, but Python can create or join workspaces, connect or
adopt integrations, set provider metadata, and build peer-agent invites.

```python
from relayfile import RelayfileSetup

setup = RelayfileSetup(access_token="cld_access_token")
workspace = setup.create_workspace(name="docs-agent")

connect = workspace.connect_integration("github")
print(connect.connect_link)

workspace.wait_for_connection(
    "github",
    connection_id=connect.connection_id,
)

client = workspace.client()
tree = client.list_tree(workspace.workspace_id)
```

## Workspace Primitives (M1)

The SDK exposes the workspace primitive paths and data shapes used by digest,
layout, schema, and writeback tooling.

```python
from relayfile import (
    DIGEST_PATHS,
    RelayFileClient,
    is_digest_path,
    provider_layout_path,
    resource_schema_path,
)

client = RelayFileClient("https://api.relayfile.dev", token_provider)

# DIGEST_PATHS is the literal anchor-path taxonomy: the rolling daily
# files plus rolling and closing-window weekly files. Closed-window daily
# artifacts use the date-stamped form ``digests/YYYY-MM-DD.md``; filter
# events through ``is_digest_path`` to subscribe to the full taxonomy
# (anchor paths plus date-stamped form).
print(DIGEST_PATHS)
# (
#     "digests/yesterday.md",
#     "digests/today.md",
#     "digests/this-week.md",
#     "digests/last-week.md",
# )
assert is_digest_path("digests/today.md")
assert is_digest_path("digests/2026-05-12.md")
assert is_digest_path("digests/this-week.md")
assert is_digest_path("digests/last-week.md")

# Read canonical provider layout documentation.
layout = client.read_file(workspace_id, provider_layout_path("linear"))

# Read a writeback schema served beside a writeback resource.
schema = client.read_file(
    workspace_id,
    resource_schema_path("linear", "issues/AGE-16__abc/comments"),
)

# Writeback list mirrors `relayfile writeback list --state ...`. Rows come back
# as typed WritebackItem instances; dead-lettered rows carry an inline
# DeadLetterErrorPayload resolved from the .error.json sidecar.
#
# Naming note: the CLI flag uses `--state dead` (short form); the SDK and wire
# value is `"dead_lettered"` (the full WritebackState literal). The CLI
# translates `dead` -> `dead_lettered` before hitting the daemon.
pending = client.list_writebacks(workspace_id, state="pending")
# `list_pending_writebacks` is preserved as a thin alias for back-compat.

# Non-pending states (e.g. `"dead_lettered"`, `"succeeded"`, `"failed"`)
# currently raise NotImplementedError until the state-filtered endpoint
# (`GET /v1/workspaces/{ws}/writeback?state=...`) is added to the
# authoritative OpenAPI contract by the `update-relayfile-cli` slice
# (workspace-primitives work item 5). The typed return shape and the
# `DeadLetterErrorPayload` coercion are ready ahead of that landing.
```
