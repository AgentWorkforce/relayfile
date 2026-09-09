# Empty GitHub source mounts

Investigated 2026-09-09 after a source mount returned no files while logging
`traversal_complete=true traversal_failed=false` and exiting successfully.
The affected upstream repository, `AgentWorkforce/relayflows`, has 286 blobs
at main commit `512ab6846b40c600c712952c62ce0c9b4092810c`, confirmed with the
GitHub recursive tree API (`truncated=false`).

## Materialization finding

The reported `meta.json missing headSha` is the last failure from
`readGithubCloneManifest` in `internal/mountsync/syncer.go`. The mount first
tries `.relayfile/clone.json`, then the legacy `meta.json`. Consequently this
message establishes that neither candidate supplied a usable head SHA; it
does not establish that the legacy metadata writer should have supplied one,
or distinguish a missing clone marker from a malformed one.

The source import and repository-record sync have separate readiness evidence.
`internal/httpapi/github_tarball.go` imports files into `/contents` and writes
`.relayfile/clone.json` with `headSha`. Its import handlers require a head SHA.
Repository metadata, issues, and PRs being available does not prove that this
source import completed. Hosted provider backfill is outside this repository;
see `docs/integrations/SELF-HOST.md` for the distinction between provider
connection and materialized data.

The mount's tar seed is an optimization over already-materialized data:
`ExportGithubWorkingTreeTar` calls RelayFile's `/fs/export`, not GitHub, and
`githubWorkingTreeSnapshot` verifies its files against RelayFile's tree.
The fallback can recover from a missing manifest **if source files are already
listed**. It cannot manufacture files when both the seed and the hosted
`/contents` listing are unavailable. A populated fallback regression confirms
that missing metadata alone does not prevent mounting source files.

This identifies the incident as missing or unavailable source materialization
at the mount boundary, plus a general mount-side success-reporting defect.
The manifest/tar prerequisite is GitHub-specific; nothing in that code is
specific to `relayflows`. The exact hosted cause (import never scheduled,
failed import, missing marker, routing, or listing visibility) cannot be
determined from the supplied log and this checkout. No live workspace/provider
configuration was changed or inspected. Follow-up should inspect the hosted
clone import job and the clone marker and contents under the same mount scope.

## Mount behavior

- A completed zero-file GitHub working-tree traversal now returns
  `EmptyRemoteTreeError`, with public `lastError.code=empty_remote_tree` and
  `lastError.kind=source_unavailable`. Failed seed diagnostics are retained.
- A generic traversal advertising a positive `totalFiles` but returning no
  files also fails. Pruned runtime totals and deliberately lazy mounts are
  excluded; an empty final page after a populated resumed prefix is valid.
- The failure summary reports `traversal_complete=false traversal_failed=true`.
  Initial bootstrap remains incomplete, no successful reconcile timestamp or
  events fast path is committed, and `--once` returns an error. A daemon can
  retry when materialization becomes available.
- Empty source exports recheck through the tree path and cannot authorize
  deletion of tracked files. Ordinary empty non-source mounts remain valid.

There is currently no authoritative upstream-empty signal in the tree contract:
zero `totalFiles` also means omitted/unknown. Therefore even a genuinely empty
GitHub repository returns the distinguishable unverified state. Accepting it
as successfully empty requires explicit upstream-empty evidence in a future
provider contract; a missing manifest or empty RelayFile listing is insufficient.

## Verification

`internal/mountsync/empty_tree_test.go` reproduces a readable non-empty remote
whose listing hides every file, tests advertised totals, preserved seed errors,
status and summary fields, recovery on a populated listing, ordinary empty
mounts, empty export safety, and resumed empty tails. Both silent-success
cases failed against the original code before the fix.

`TestInitialSyncOnceRejectsEmptyUnmaterializedSource` exercises the real HTTP
client and polling runner: an empty source listing plus legacy metadata without
`headSha` fails `--once` and the sandbox readiness guard.
