# Relayfile Fork Architecture

The `main` branch does not currently contain a Cloudflare Worker/Durable Object server. The routed workspace API lives in `internal/httpapi` and persists workspace state through `internal/relayfile.Store`, so the fork primitive is implemented there using the same pointer-plus-overlay model.

## State

Each fork stores only metadata and an overlay, not a snapshot:

```text
parent workspace
  revision: rev_42
  files: /a.md /b.md
        ^
        | parentRevision
        |
fork fork_123
  workspaceId: ws_1
  proposalId: prop_1
  parentRevision: rev_42
  expiresAt: 2026-04-28T...
  overlay:
    /a.md -> write revision fork:fork_123:1
    /b.md -> delete tombstone
```

Unmodified fork reads and listings use the current parent workspace view because this server does not have point-in-time file reads. Commit still compares the stored `parentRevision` with the current parent workspace revision and returns `parent_moved` if the parent changed.

## Routing

Fork lifecycle endpoints are mounted under `/v1/workspaces/:workspaceId/forks`. Existing file-system endpoints accept `forkId` as a query parameter and branch to the overlay view when present.

## Expiry

The Go store schedules an in-memory timer for each fork expiry. The persisted fork state includes `expiresAt`; on reload, timers are recreated for unexpired forks and expired forks are discarded.
