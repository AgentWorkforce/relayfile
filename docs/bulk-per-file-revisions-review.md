# Bulk Per-File Revisions — Review Verdict

Reviewed against `docs/bulk-per-file-revisions-boundary.md` at branch
`fix/bulk-write-per-file-revisions`.

## 1. Scope / files modified

Boundary-listed files touched (all present):

- `internal/httpapi/server.go` ✅
- `internal/httpapi/server_test.go` ✅
- `internal/relayfile/store.go` ✅ — `BulkWriteResult` added here (boundary
  allowed "wherever `BulkWriteFile` / `BulkWriteError` live")
- `internal/mountsync/types.go` ✅ — alias to `relayfile.BulkWriteResult`,
  dropped the compatibility `Files []RemoteFile` branch as the boundary
  allowed
- `internal/mountsync/syncer.go` ✅
- `internal/mountsync/syncer_test.go` ✅

Additional files touched (minor scope creep, non-blocking):

- `internal/relayfile/store_test.go` — expanded to cover the new
  `results` return; reasonable companion to the store change even though the
  boundary did not enumerate it.
- `internal/relayfile/adapters_test.go` — cosmetic `gofmt` alignment only
  (no functional change). Outside boundary scope but harmless.
- `package-lock.json` — unrelated npm lockfile churn; not required by this
  feature.
- `.trajectories/index.json`, `.trajectories/active/…`, `.trajectories/completed/…`,
  `workflows/05[9]-*.ts`, `workflows/061-bulk-write-per-file-revisions.ts` —
  trajectory / workflow bookkeeping, not production code.

Nothing functionally drifts outside the feature surface.

## 2. Backward compatibility

Wire response is purely additive. `handleBulkWrite` still emits
`written`, `errorCount`, `errors`, `correlationId` with unchanged names,
types, and semantics. `results` is a new sibling key that older clients
using struct decoding (or `map[string]any`) will simply drop. Confirmed by
re-reading `internal/httpapi/server.go:1597-1615` — only additive changes.

Mount side decodes `results` with `omitempty` on the response struct and
falls back to the existing `ReadFile` path when the map is empty or the
revision is blank (`internal/mountsync/syncer.go:684` retains the `ReadFile`
call when `result.Revision` is missing). Rollout remains safe in either
order.

## 3. Signature change coverage

`Store.BulkWrite` and `Store.BulkWriteFork` now return
`(int, []BulkWriteResult, []BulkWriteError)`. Every call site is updated:

- `internal/httpapi/server.go:1603` (fork) and `:1605` (main).
- `internal/httpapi/server_test.go:904, :945, :983` (export tests use `_`).
- `internal/relayfile/store_test.go:444, :493, :547, :560, :589, :630`.

Grep for `\.BulkWrite(Fork)?\(` shows no stale 2-return call sites.

## 4. Mount `reconcileBulkWrite`

`flushPendingBulkWrites` builds `resultsByPath` via the new
`resultsByPath()` helper in `internal/mountsync/types.go:23-36`, which
normalizes paths and trims whitespace. When an entry exists, `ContentType`
is copied into the pending snapshot before the reconcile call, and the
revision is passed as the third argument to `reconcileBulkWrite`. An empty
revision still triggers the defensive `ReadFile` fallback inside
`reconcileBulkWrite` (unchanged), preserving the rolling-deploy safety net
the boundary called out.

## 5. Test coverage

- **Success path carries `results`**: `TestBulkWriteEndpoint`
  (`internal/httpapi/server_test.go:823+`) asserts `Results` has exactly
  one entry with matching `Path`, non-empty `Revision`, and the expected
  `ContentType`.
- **Error path omits failed entry from `results`**: same test submits a
  second file with `encoding: utf16` that fails validation; it asserts the
  failed path is in `Errors` (code `invalid_encoding`) and verifies no
  entry in `Results` shares that path.
- **Invariants**: `len(results) == written` and `len(errors) == errorCount`
  are both asserted.
- **Mount skips `ReadFile` when revision is present**:
  `TestBulkMigrationReducesHTTPCalls`
  (`internal/mountsync/syncer_test.go:660+`) now records *every* request on
  `/fs/file` and asserts `len(fsFileRequests) == 0` after the bulk sync.
  The old `fsFilePostCount` POST-only counter and its apologetic comment
  block are removed, as the boundary required.
- **Store-level results shape** is exercised by the new
  `assertBulkWriteResults` helper (`internal/relayfile/store_test.go:203`)
  and wired into every `BulkWrite` test case in that file.

## 6. E2E tightening

`TestBulkMigrationReducesHTTPCalls` no longer inspects only POSTs — it
asserts zero total requests on `/fs/file`. The POST-filter inside the mock
handler was removed; any method on `/fs/file` now fails the test. This is
exactly the tightening the boundary mandated (section 6, "Mount").

## 7. Build / format / vet

- `gofmt -l internal/{httpapi,mountsync,relayfile}` → clean.
- `go build ./...` → clean.
- `go test ./internal/httpapi/... ./internal/mountsync/... ./internal/relayfile/... -count=1 -timeout 180s`
  → all three packages `ok`.
- `go vet ./...` → one failure in `internal/mountfuse/fuse_test.go:76`
  (`fakeRemoteClient` missing `WriteFilesBulk`). Verified pre-existing by
  running `go vet` on the stashed pre-change tree; it fails identically.
  `internal/mountfuse` is the FUSE mount path the boundary explicitly
  excludes from scope (section 7, "FUSE mount path … unchanged"), so this
  is out of scope for this review. Recommend tracking it as a separate
  cleanup issue.

## 8. Nit (non-blocking)

When `BulkWrite` returns on an early input-validation error
(`workspaceID` blank), `results` is returned as `nil`, and the server
response therefore marshals `"results": null` rather than `[]`. The
boundary doc does not specify empty-array vs. null semantics, and current
clients tolerate either, so this is not blocking — but emitting
`[]BulkWriteResult{}` in the handler when `results == nil` would make the
wire shape more predictable for future JS/SDK consumers.

## Verdict

All seven checklist items pass. The new wire field is additive, signatures
are updated consistently, the mount prefers response-supplied revisions
with a preserved `ReadFile` fallback, tests cover both the success and
error halves of the response, and the E2E proof is tightened to assert
zero `/fs/file` requests of any method. Pre-existing `go vet` failure in
`internal/mountfuse` is unrelated to this branch.

REVIEW_APPROVED
