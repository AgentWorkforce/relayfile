# RelayFile Cloud Integration

RelayFile is the shared filesystem layer for Agent Relay style cloud workflows. It gives each workflow run a workspace-backed tree that humans, automations, and cloud agents can mount at the same time.

## How RelayFile Fits Into Cloud Workflows

A typical Agent Relay flow looks like this:

1. A workflow run starts in the cloud.
2. The orchestration layer creates or resolves a RelayFile workspace for that run.
3. Agents mount the workspace into their sandbox snapshot.
4. Humans can mount the same workspace locally.
5. Everyone reads and writes the same project tree through RelayFile.

This makes the filesystem a first-class workflow artifact instead of a side effect hidden inside one machine or one agent process.

## Automatic Workspace Creation Per Workflow Run

Cloud orchestrators can create a fresh workspace for every workflow run so that:

- each run is isolated from other runs
- files remain attached to the run that produced them
- replay and audit tooling can refer back to a stable workspace ID

The user-facing CLI model is:

```bash
relayfile workspace create workflow-2026-03-24-1234
relayfile seed workflow-2026-03-24-1234 ./bootstrap
```

In hosted automation, the same lifecycle is usually driven by the control plane rather than by a human at a terminal.

## relayfile-mount In Sandbox Snapshots

Inside a cloud sandbox, `relayfile-mount` acts as the local bridge between the sandbox filesystem and the RelayFile API.

Typical sandbox mount:

```bash
go run ./cmd/relayfile-mount \
  --base-url https://relayfile.agent-relay.com \
  --workspace ws_123 \
  --remote-path / \
  --local-dir /workspace \
  --token "$RELAYFILE_TOKEN"
```

The sandbox sees ordinary files under `/workspace`, but the source of truth is the RelayFile workspace. That means:

- sandbox snapshots can be short-lived without losing shared files
- agents can restart and reconnect to the same workspace
- multiple agents can work against the same directory tree without shipping tarballs around

## How Agents Share Files Via RelayFile

RelayFile turns file exchange into normal filesystem work:

- Agent A writes `/notes/plan.md`
- Agent B mounts the same workspace and sees `/notes/plan.md`
- A human reviews or edits the same file locally
- writeback, event feeds, and operation logs preserve the sync history

This is especially useful for:

- generated code or patch files
- logs and structured artifacts
- prompts, plans, and handoff notes
- exported bundles and review outputs

## Shared Human And Agent Workspaces

A practical pattern is:

1. Human mounts `project-x` locally.
2. Cloud agent mounts `project-x` inside its sandbox.
3. Human edits requirements or reviews generated code.
4. Agent writes implementation changes back into the same tree.
5. Both sides see updates after the next sync cycle.

That avoids manual upload, download, and copy-paste loops.

## Operational Notes

- Use a unique workspace per workflow run when isolation matters more than continuity.
- Reuse a long-lived workspace when a team wants a shared persistent project tree.
- Use the sync, ops, and admin endpoints from `docs/api-reference.md` to inspect ingestion health, dead letters, and replay status.
- For external providers, submit inbound events through `POST /v1/workspaces/{workspaceId}/webhooks/ingest` and consume outbound work through the writeback queue endpoints.
