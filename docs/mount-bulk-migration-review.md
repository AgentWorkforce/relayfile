# mount-bulk-sync migration — reviewer verdict

Reviewed against `docs/mount-bulk-migration-boundary.md` on 2026-04-24.
Diff was read fresh; any prior contents of this file have been
discarded.

## 1. Scope / files touched

`git diff --name-only` → 5 modified paths, plus untracked files:

| File | Status | In boundary §1? |
|------|--------|-----------------|
| `internal/mountsync/syncer.go` | modified | ✅ yes |
| `internal/mountsync/syncer_test.go` | modified | ✅ yes |
| `internal/mountsync/types.go` | new (untracked) | ✅ yes |
| `docs/mount-bulk-migration-boundary.md` | new (untracked) | ✅ yes (this is the doc) |
| `internal/mountsync/http_client_test.go` | modified | ⚠️ not listed, but tightly scoped |
| `.trajectories/…`, `package-lock.json`, `workflows/*.ts` | incidental | ignored (not code under review) |

`http_client_test.go` is not named in §1 but only adds two unit tests
for the new `HTTPClient.WriteFilesBulk` method
(`TestHTTPClientWriteFilesBulkUsesSinglePOSTWithAllFiles`,
`TestHTTPClientWriteFilesBulkRejectsEmptyBatch`). That is a direct test
of the deliverable in §2 and matches the pattern of neighbouring tests
in the same file. Not scope creep.

Minor: `types.go` imports `internal/relayfile` to type-alias
`BulkWriteFile` and `BulkWriteError`. The boundary said mountsync must
not import the *server* (`internal/httpapi`) package; it does not
forbid `internal/relayfile`, which is the wire-type source of truth.
Acceptable.

## 2. Concurrency safety

`pushLocal` (`internal/mountsync/syncer.go:1488`) serializes writes
exactly as before: single goroutine, sorted `localRemotePaths`, no
parallel fan-out. The new accumulator `pendingWrites` is a **local
slice** inside `pushLocal`, not a field on `Syncer`, so there is no
shared mutable state across goroutines and nothing new that the
existing `Syncer.mu` needs to guard. The cycle-level serialization
invariant from the boundary doc is preserved.

## 3. 412 first-write fix — no pre-write GET

`preparePendingBulkWrite` no longer constructs a `baseRevision := "0"`
sentinel and no longer calls `s.client.WriteFile` — it enqueues into
the bulk accumulator. The bulk endpoint does not consult `If-Match`,
so no `If-Match: "0"` ever hits the wire (gate 5 greps confirm zero
matches). No speculative GET is issued before writes. The post-flush
`ReadFile` in `reconcileBulkWrite` runs **after** a successful bulk
write, only when the response did not carry a revision — this matches
boundary strategy rule 4 (“one GET per write, sequential, inside the
cycle; we save N−1 PUTs and dodge 412s”). Bounded by the number of
files just written, not unbounded, and never precedes the write.

## 4. Per-file error handling

`flushPendingBulkWrites` (`syncer.go:~660`) walks both the success
paths and the `errors` slice and dispatches each entry independently:

- `forbidden` / `conflict` / per-file codes are translated via
  `bulkWriteErrorAsError` into the same `*HTTPError` / `*ConflictError`
  shapes the single-file path already produced, then handled by
  `handleWriteError` — which preserves the prior behaviour
  (`WriteDenied`/`DeniedHash` record for create-forbidden, conflict
  reconciliation with a `ReadFile`).
- An unrecognized code captured into `firstErr` **does not abort the
  batch**: the loop keeps reconciling the remaining entries and only
  returns `firstErr` at the end.

`TestBulkWrite_PartialErrors` and
`TestBulkWrite_PerFileErrorMarksDirtyForRetry` exercise this and pass.
Criterion satisfied.

## 5. Post-flush revision read-back

`reconcileBulkWrite` (`syncer.go:~720`) updates
`s.state.Files[path].Revision` from, in order:

1. `response.revisionsByPath()[path]` — revisions surfaced by the
   server in the bulk response (`Results[].Revision` or
   `Files[].Revision`).
2. If the server omitted a revision, a post-write `s.client.ReadFile`
   on that path.

It **never** reuses the pre-write `tracked.Revision` as the stored
revision, which is exactly what the boundary mandated. Confirmed by
reading the function end-to-end: the `trackedFile` literal written to
`s.state.Files` uses the local `revision` variable (populated from the
response or the read-back), `pendingWrite.snapshot.Hash` for the
content hash, and `Dirty: false`.

Today the real server does not return per-file revisions
(`internal/httpapi/server.go:1607-1612` — only
`{written, errorCount, errors, correlationId}`), so production will
exercise the `ReadFile` branch for every successful write. That is
the expected path per strategy rule 4. The `Results`/`Files`
revision-harvest branch is dead on the current server but
future-proof, and `TestBulkWrite_ReconcileUsesResponseRevision` pins
it down.

Nit (non-blocking): the contentType fallback in `reconcileBulkWrite`

```go
contentType := strings.TrimSpace(pendingWrite.snapshot.ContentType)
if contentType == "" {
    contentType = pendingWrite.snapshot.ContentType
}
```

is a no-op — the fallback reassigns the same value that just trimmed
to empty. Harmless, but cosmetic dead code.

## 6. Build / vet / fmt / test gates

All acceptance gates pass locally:

- `go build ./internal/mountsync/...` — clean.
- `go vet ./internal/mountsync/...` — clean.
- `gofmt -l internal/mountsync/` — clean.
- `go test ./internal/mountsync/... -count=1` — `ok 2.280s`.
- `go test ./internal/mountsync/ -run 'TestBulkWrite_|TestBulkSeedThenSync' -count=1 -v`
  — all 10 `TestBulkWrite_*` + `TestBulkSeedThenSync` pass, including
  `_SingleCallForNFiles`, `_FirstWriteNoIfMatch`, `_ChunkAtThreshold`,
  `_PartialErrors`, `_DeletesStayPerFile`.
- `go test ./internal/httpapi/... ./internal/relayfile/...` — clean
  (regression gate).
- `grep 's.client.WriteFile(' internal/mountsync/syncer.go` → **zero
  matches** inside the push loop (gate 3).
- `grep 'If-Match.*0'` inside `internal/mountsync/` → zero matches
  (gate 5).

## Summary

The implementation matches the boundary doc on every criterion this
review had to verify: scoped file list, preserved cycle-level
serialization, no `If-Match: "0"` on the wire, no pre-write GET,
per-file error surfacing without batch abortion, and post-flush
revision refresh sourced from the response (with a bounded
`ReadFile` fallback).

REVIEW_APPROVED
