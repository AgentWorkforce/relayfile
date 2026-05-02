# Mount Safety/Status Slice Evidence

This slice makes the synced mirror self-describing without depending on FUSE:

- public local status lives at `<mount>/.relay/state.json`
- unresolved conflicts are materialized under `<mount>/.relay/conflicts/`
- resolved conflict artifacts move to `<mount>/.relay/conflicts/resolved/`
- denied writes remain visible in `<mount>/.relay/permissions-denied.log`

The machine-readable status file now exposes:

- `status`: `ready`, `stale`, `offline`, `conflict`, or `writeback-pending`
- `pendingWriteback`, `pendingConflicts`, and `deniedPaths`
- per-file status entries, including `encoding: "base64"` for binary mirrors
- `lastError` for rejected or offline writeback attempts

Safety behavior covered by tests:

- revision conflicts preserve the local edit as an artifact and refresh the remote copy in place
- binary files round-trip as raw local bytes with base64-on-the-wire writeback
- rejected large writes stay local and remain visible as pending writeback
- remote deletes remain per-file and settle to a clean local status
