# Getting Started With RelayFile

RelayFile gives multiple machines and agents a shared workspace-backed filesystem. A common flow is:

1. Start a RelayFile server locally or point the CLI at a hosted deployment.
2. Log in and create a workspace.
3. Seed the workspace from an existing project.
4. Mount that workspace on one or more machines.
5. Watch edits propagate through the shared tree.

## Prerequisites

- For local development, install Go so you can run `go run ./cmd/relayfile`.
- For hosted usage, you only need the RelayFile CLI plus a server URL and token.
- If you want to mount the same workspace on multiple machines, each machine needs network access to the same RelayFile server.

## Start The Server Locally

Run the API server from the repository root:

```bash
go run ./cmd/relayfile
```

By default the service listens on `http://127.0.0.1:8080`.

If you want durable local state instead of in-memory state, use:

```bash
RELAYFILE_BACKEND_PROFILE=durable-local \
RELAYFILE_DATA_DIR=.data \
go run ./cmd/relayfile
```

If you are using a hosted deployment instead, skip this step and replace `http://127.0.0.1:8080` in the examples below with your hosted server URL.

## Log In And Create A Workspace

Log in once so the CLI can store the server URL and token:

```bash
relayfile login --server http://127.0.0.1:8080 --token dev-token
```

Create a workspace for the project you want to sync:

```bash
relayfile workspace create my-project
```

List available workspaces:

```bash
relayfile workspace list
```

If your environment already assigns you a workspace ID, you can use that directly instead of creating one.

## Seed Files From A Project

Upload an existing local directory into the workspace:

```bash
relayfile seed my-project ./src
```

Useful variants:

```bash
relayfile seed my-project .
relayfile seed my-project ./src --exclude node_modules --exclude .git
relayfile seed my-project ./src --dry-run
```

`seed` is the fastest way to populate a workspace before collaborators or agents mount it.

## Mount On Two Machines

After seeding, mount the same workspace on each machine that should participate.

Machine A:

```bash
relayfile mount my-project ./src
```

Machine B:

```bash
relayfile mount my-project ./src
```

Both mounts point at the same remote workspace. RelayFile continuously syncs changes between the local directory and the server-backed virtual tree.

If you want a one-shot sync instead of a long-running mount:

```bash
relayfile mount my-project ./src --once
```

## Watch Files Sync

With both mounts running:

1. Edit `./src/README.md` on Machine A.
2. Save the file.
3. Wait for the next sync cycle.
4. The updated file appears on Machine B.

With the default polling interval, changes usually arrive in about 1-2 seconds.

If you want to watch the workspace status while testing:

```bash
relayfile status my-project
```

For low-level API testing, you can also inspect the event feed directly:

```bash
curl -sS \
  -H "Authorization: Bearer dev-token" \
  "http://127.0.0.1:8080/v1/workspaces/my-project/fs/events?path=/&limit=20"
```

## Next Steps

- Collaboration patterns: `docs/guides/collaboration.md`
- Cloud workflow integration: `docs/guides/cloud-integration.md`
- REST contract details: `docs/api-reference.md`
