# mount-bulk-sync migration boundary

Scope: migrate `relayfile-mount` (`internal/mountsync`) from per-file HTTP
`PUT /fs/file` writes to batched `POST /fs/bulk` writes, and fix the
first-write 412 `missing If-Match header` storm. Cross-ref: AgentWorkforce/cloud#342.

## 1. Files touched

| File | Create/Modify | Why |
|------|---------------|-----|
| `internal/mountsync/syncer.go` | modify | Add `WriteFilesBulk` method on `HTTPClient`; extend `RemoteClient` interface with it; introduce a batching accumulator that `pushLocal` populates and flushes at end of cycle; change `handleLocalWriteOrCreate` to drop the `If-Match` header on first-write (no tracked revision) path. |
| `internal/mountsync/syncer_test.go` | modify | Add tests for bulk batching, first-write no-`If-Match` behavior, mixed create/update batch, and partial-error handling. Extend the existing `fakeRemoteClient` (or add one) to capture `WriteFilesBulk` calls. |
| `internal/mountsync/types.go` *(or inline in `syncer.go` if no types file)* | modify | Add `BulkWriteFile` / `BulkWriteError` / `BulkWriteResponse` wire types mirroring `internal/relayfile/store.go:168-179` so `mountsync` does not import the server package. |
| `docs/mount-bulk-migration-boundary.md` | create | This boundary doc. |
| `docs/mount-bulk-migration-checklist.md` | *out of scope for this step* | Produced in the next phase. |

No `internal/httpapi/` or `internal/relayfile/` changes. The server-side
`handleBulkWrite` (`internal/httpapi/server.go:1556`) and
`relayfile.Store.BulkWrite` already accept the shape we need.

## 2. `HTTPClient.WriteFilesBulk` signature

Wire types (mirror of `internal/relayfile/store.go:168-179` plus the
server response at `internal/httpapi/server.go:1629-1634`):

```go
// BulkWriteFile matches relayfile.BulkWriteFile on the wire.
type BulkWriteFile struct {
    Path        string `json:"path"`
    ContentType string `json:"contentType"`
    Content     string `json:"content"`
    Encoding    string `json:"encoding,omitempty"`
}

// BulkWriteError matches relayfile.BulkWriteError on the wire.
type BulkWriteError struct {
    Path    string `json:"path"`
    Code    string `json:"code"`
    Message string `json:"message"`
}

// BulkWriteResponse matches the JSON body returned by
// POST /v1/workspaces/{id}/fs/bulk (status 202).
type BulkWriteResponse struct {
    Written       int              `json:"written"`
    ErrorCount    int              `json:"errorCount"`
    Errors        []BulkWriteError `json:"errors"`
    CorrelationID string           `json:"correlationId"`
}
```

Method signature:

```go
func (c *HTTPClient) WriteFilesBulk(
    ctx context.Context,
    workspaceID string,
    files []BulkWriteFile,
) (BulkWriteResponse, error)
```

Implementation notes:
- Route: `POST /v1/workspaces/{workspaceID}/fs/bulk` (confirmed at
  `internal/httpapi/server.go:161-163`). No `forkId` query param — mountsync
  does not use forks.
- Body: `{"files": [...]}`; `Content-Type: application/json`; reuse
  `c.doJSON` for retry/backoff parity with the existing single-file write.
- Empty `files` is a caller bug — return early with a typed sentinel; do
  not round-trip a guaranteed 400.
- Add the method to the `RemoteClient` interface at
  `internal/mountsync/syncer.go:96` so `fakeRemoteClient` in tests can
  implement it.

## 3. Syncer batching strategy

**Concurrency invariant (preserve as-is):** `pushLocal`
(`internal/mountsync/syncer.go:1341`) already serializes writes within one
sync cycle by iterating `localRemotePaths` in sorted order, one goroutine,
no parallelism. The batching layer lives **between** the per-file handler
and `HTTPClient`; it does not introduce concurrency.

**Batch, don't stream.** Replace the per-file `s.client.WriteFile(...)`
call inside `pushSingleFile` (`internal/mountsync/syncer.go:599`) with an
enqueue into an in-cycle accumulator owned by the `Syncer`:

```go
type pendingBulkWrite struct {
    remotePath  string
    localPath   string
    snapshot    localSnapshot
    tracked     trackedFile
    exists      bool
    contentType string
    content     string
}
```

Rules:

1. **Full-cycle bulk** — the primary path. `pushLocal` accumulates every
   write that would have gone through `pushSingleFile` into a slice and
   flushes it with a single `WriteFilesBulk` call after the create/update
   loop completes but before the deletion loop (line 1420+). This is the
   common case (initial seed / mass local-edit storm) and collapses N HTTP
   requests into one.
2. **Chunk cap** — if the accumulator reaches `bulkFlushThreshold` (start
   with 256 files) during the loop, flush eagerly and reset. Prevents
   unbounded request bodies on large `scanLocalFiles` results. Constant,
   not configurable in this migration.
3. **Per-file stays** for the fsnotify fast path
   (`handleLocalWriteOrCreate`, `internal/mountsync/syncer.go:497,524`).
   Single-event writes should still batch-of-one through `WriteFilesBulk`
   (same endpoint, same code path — cheap) rather than re-enter the old
   `WriteFile` path. This keeps the `If-Match` header off the single-file
   PUT entirely and gives us one write code path to reason about.
4. **Conflict / read-after-write reconciliation** stays per-file.
   `WriteFilesBulk` does not return a new revision (the server response
   has no per-file revision; see `handleBulkWrite` at
   `internal/httpapi/server.go:1629-1634`). After a successful bulk flush,
   we must `ReadFile` each written path to refresh `tracked.Revision` and
   `tracked.Hash` so subsequent single-file operations (delete, conflict
   recovery) still have the optimistic-concurrency token they need. This
   read-back happens sequentially inside the cycle; we pay one GET per
   write, but we save N−1 PUTs and dodge 412s.
5. **Deletes are NOT batched** in this migration. `DeleteFile`
   (`internal/mountsync/syncer.go:191,734,1432`) still requires
   `If-Match`, and the server has no bulk-delete endpoint. Keep the
   existing per-file delete loop untouched.

## 4. First-write 412 fix

**Production symptom** (from the collect-context log):
`http 412 precondition_failed: missing If-Match header` firing on first
write of a new file, then `context deadline exceeded` cascading as
retries back up.

**Root cause.** `pushSingleFile` at `internal/mountsync/syncer.go:592-599`
sets `baseRevision := "0"` as a "create-if-absent" sentinel and passes it
into `HTTPClient.WriteFile` which unconditionally sets
`If-Match: "0"` at line 184. The server's single-file write endpoint
rejects non-empty, non-matching `If-Match` values on a path that does not
yet exist, surfacing as 412 `missing If-Match header`.

**Chosen fix: route all writes through `WriteFilesBulk` (Option A).**

The bulk endpoint (`handleBulkWrite`,
`internal/httpapi/server.go:1556`) does **not** consult `If-Match` at all
— it treats every entry as an upsert. Migrating all writes to bulk
*structurally removes* the 412 storm: there is no `If-Match` header left
on the write path for the server to reject.

The alternative (Option B: fetch current revision when `baseRevision`
is empty/`"0"`) was rejected because:
- it adds a speculative GET in front of every first write, doubling
  latency on the hot path;
- it races with concurrent remote creates (the GET returns 404, we write
  with `If-Match: "0"`, and we are right back to the same 412);
- it leaves two write code paths (bulk for batches, per-file for
  singletons) — more code, more drift risk.

Concretely, `pushSingleFile` no longer calls `s.client.WriteFile` for the
create/update case. It enqueues into the bulk accumulator; the accumulator
flushes via `WriteFilesBulk`; the read-back loop (strategy rule 4)
refreshes revisions. `HTTPClient.WriteFile` stays on the struct for now
(used by `revertReadonlyFile` and conflict recovery paths that already
have a known revision), but its use shrinks to paths that always have a
non-sentinel revision — so `If-Match: "0"` never hits the wire again.

## 5. New test cases

Added to `internal/mountsync/syncer_test.go`. Each test uses a fake
`RemoteClient` that records calls; none hit the real httpapi server
except where noted.

1. **`TestBulkWrite_SingleCallForNFiles`** — local dir has 5 new files;
   after `SyncOnce`, the fake client records exactly one `WriteFilesBulk`
   call containing all 5 paths and zero `WriteFile` calls.
2. **`TestBulkWrite_FirstWriteNoIfMatch`** — against the real
   `newMountsyncAPIHandler` (same pattern as `TestBulkSeedThenSync`,
   `internal/mountsync/syncer_test.go:441`), seed nothing, write one new
   file locally, `SyncOnce`, assert the file lands on the server with
   HTTP 202 and no 412 is observed in the response log.
3. **`TestBulkWrite_ChunkAtThreshold`** — local dir has 300 files with
   `bulkFlushThreshold` forced to 256 via a test hook; assert exactly two
   `WriteFilesBulk` calls with sizes 256 and 44.
4. **`TestBulkWrite_PartialErrors`** — fake server returns a
   `BulkWriteResponse` with `errorCount=1` and one `forbidden` entry;
   assert the successful paths update `state.Files[remotePath].Revision`
   (via the read-back) while the failed path retains its prior tracked
   state and logs a denial, and the next `SyncOnce` does **not** re-push
   the denied file (matches the existing `WriteDenied`/`DeniedHash` skip
   at `internal/mountsync/syncer.go:1406`).
5. **`TestBulkWrite_DeletesStayPerFile`** — local dir drops a previously
   tracked file; assert `WriteFilesBulk` is NOT called for the delete and
   one `DeleteFile` call is issued with the tracked `If-Match` revision.

## 6. Deliberately out of scope

- Any change under `internal/httpapi/` or `internal/relayfile/` — the
  server-side `POST /fs/bulk` and `Store.BulkWrite` are already correct
  for our needs.
- Bulk delete. No server endpoint; per-file `DeleteFile` stays.
- FUSE / kernel mount layer. This migration is HTTP-client-only.
- Fork-aware bulk writes (`BulkWriteFork`). Mountsync does not target
  forks.
- Websocket event path (`websocketURL`, `syncer.go:1706`). Untouched.
- Retry/backoff tuning in `HTTPClient.doJSON`. Reuse as-is.
- Content encoding / binary handling beyond what
  `BulkWriteFile.Encoding` already carries.

## 7. Acceptance gates

Before commit, all of the following must pass locally from the repo root.

```bash
# 1. Package compiles and all mountsync tests pass, including new ones.
go test ./internal/mountsync/... -count=1

# 2. Targeted run of the bulk-migration tests (must all pass, no skips).
go test ./internal/mountsync/ -run 'TestBulkWrite_|TestBulkSeedThenSync' -count=1 -v

# 3. No residual per-file write call inside the push loop.
#    Expect: zero matches.
grep -n 's.client.WriteFile(' internal/mountsync/syncer.go | grep -v revertReadonlyFile | grep -v conflict

# 4. WriteFilesBulk is wired into the interface and the HTTPClient.
#    Expect: at least one match in each of these files.
grep -n 'WriteFilesBulk' internal/mountsync/syncer.go
grep -n 'WriteFilesBulk' internal/mountsync/syncer_test.go

# 5. No If-Match: "0" sentinel left on the wire.
#    Expect: zero matches.
grep -nE '"If-Match":\s*"0"' internal/mountsync/

# 6. Wider suite still green (regression check on httpapi + relayfile).
go test ./internal/httpapi/... ./internal/relayfile/... -count=1
```

Gate 3's filter is tolerant of the two legitimate remaining callers
(`revertReadonlyFile`, conflict-recovery read-after-write); if either is
renamed, update the grep alongside the rename in the same commit.

BOUNDARY_READY
