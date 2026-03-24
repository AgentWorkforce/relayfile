# Collaboration With RelayFile

RelayFile is built for shared editing across machines, services, and agent sandboxes. The core model is simple: everyone mounts the same workspace, and RelayFile keeps the local directories converged.

## Two Humans Collaborating

Mount the same workspace on both machines.

Machine A:

```bash
relayfile mount project-x ./src
```

Machine B:

```bash
relayfile mount project-x ./src
```

Typical experience:

1. Person A edits a file on Machine A.
2. RelayFile uploads the change to the shared workspace.
3. Machine B pulls the update on the next sync cycle.
4. The file appears on Machine B in about 1-2 seconds with the default settings.

This works well for:

- pair programming across two laptops
- keeping a staging machine aligned with a development machine
- reviewing generated artifacts without sending archives around

## Human And Agent Collaboration

RelayFile also supports shared work between a local human environment and a cloud agent sandbox.

Example setup:

- Human mounts the workspace locally: `relayfile mount project-x ./src`
- Agent mounts the same workspace in the cloud sandbox
- Both sides read and write the same files

Common pattern:

1. Human seeds a project and starts a mount locally.
2. Agent mounts the same workspace remotely.
3. Agent writes code, notes, or generated artifacts.
4. Human reviews or edits those files locally.
5. The updated files flow back to the agent on the next sync cycle.

This removes the usual upload-download loop between human terminals and remote agent runs.

## Conflict Resolution

Conflicts happen when two writers edit the same file before the system can reconcile the changes.

RelayFile is designed around conflict-safe writeback and optimistic concurrency:

- each write is associated with file revision state
- concurrent changes are detected instead of silently overwritten
- sync and operation feeds show what happened
- retry and dead-letter handling keep failures observable instead of hidden

What to expect in practice:

1. Machine A and Machine B both edit `src/main.go`.
2. One change lands first.
3. The later write is compared against the current revision.
4. If the write is stale, RelayFile treats it as a conflict rather than blindly replacing the newer version.
5. Clients or operators can inspect the resulting operation and replay state.

Best practices:

- Prefer separate files for parallel work when possible.
- Seed once, then keep mounts running so each collaborator is working from a current tree.
- Use the status, event, and ops APIs when you need to diagnose a sync race.

## Related Guides

- Setup and first sync: `docs/guides/getting-started.md`
- Cloud workflow model: `docs/guides/cloud-integration.md`
- Full HTTP contract: `docs/api-reference.md`
