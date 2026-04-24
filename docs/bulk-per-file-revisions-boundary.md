# Bulk Per-File Revisions — Boundary Spec

This document scopes the server-side change to `/fs/bulk` so the response
includes per-file revisions, and the matching simplification in the mount
reconciler so it no longer needs a follow-up `ReadFile` to discover revisions.

## 1. Files touched

| File | Create/Modify | Reason |
| ---- | ------------- | ------ |
| `internal/httpapi/server.go` | Modify | `handleBulkWrite` must build and return a `results` array with `{path, revision, contentType}` for every successfully written file. |
| `internal/httpapi/server_test.go` | Modify | Add a test asserting the new `results` field is populated for successful writes and that failed entries are absent from `results` (they continue to appear in `errors`). |
| `internal/relayfile/store.go` | Modify | `BulkWrite` and `BulkWriteFork` already compute per-file revisions internally; change their signatures to surface those revisions (and content types) so `handleBulkWrite` can forward them. |
| `internal/relayfile/types.go` (or wherever `BulkWriteFile` / `BulkWriteError` live) | Modify | Add a shared `BulkWriteResult` struct (`Path`, `Revision`, `ContentType`) so server and mount wire types stay aligned. |
| `internal/mountsync/types.go` | Modify | `BulkWriteResponse.Results` already exists; verify field names and JSON tags match the server shape exactly (`results`, `path`, `revision`, `contentType`). Remove the `Files []RemoteFile` branch if it is only there as a compatibility shim — keep it only if it is still populated by another code path. |
| `internal/mountsync/syncer.go` | Modify | `reconcileBulkWrite`: when the bulk response already provides a revision for the path, use it directly and skip the `ReadFile` round-trip. Retain the `ReadFile` fallback only for the defensive empty-revision case. |
| `internal/mountsync/syncer_test.go` | Modify | Tighten `TestBulkMigrationReducesHTTPCalls` to assert zero total requests on `/fs/file` (not just zero `POST`s). Remove the now-redundant `fsFilePostCount` counter. |

## 2. New wire shape for `/fs/bulk` response

```json
{
  "written": 3,
  "errorCount": 1,
  "errors": [
    {
      "path": "locked.txt",
      "code": "permission_denied",
      "message": "agent lacks write permission"
    }
  ],
  "results": [
    {
      "path": "docs/a.md",
      "revision": "rev_01HW...",
      "contentType": "text/markdown"
    },
    {
      "path": "docs/b.md",
      "revision": "rev_01HW...",
      "contentType": "text/markdown"
    },
    {
      "path": "src/x.go",
      "revision": "rev_01HW...",
      "contentType": "text/x-go"
    }
  ],
  "correlationId": "corr_..."
}
```

Notes:

- `results` contains one entry per successfully written file, in the same
  order `BulkWrite` processed them. Entries in `errors` are **not** present in
  `results` (and vice versa).
- `contentType` is emitted with `omitempty`; absent when the store did not
  resolve one.
- `written` continues to equal `len(results)`.

## 3. Backward compatibility

- **Old SDK / client reads new response:** Extra top-level `results` field is
  ignored by standard JSON decoders (all existing SDKs use struct decoding with
  unknown-field tolerance or `map[string]any`). No existing field changes
  meaning, so old clients keep working.
- **New mount reads old server response:** `BulkWriteResponse.Results` is
  `omitempty` on the wire and decodes to `nil` / empty. `revisionsByPath()`
  returns an empty map, and `reconcileBulkWrite` falls back to the existing
  `ReadFile` code path to recover the revision. This makes the rollout safe in
  either order (server-first or client-first).
- No version gate, feature flag, or capability negotiation is required.

## 4. `Store.BulkWrite` signature change

Current:

```go
func (s *Store) BulkWrite(workspaceID string, files []BulkWriteFile) (int, []BulkWriteError)
func (s *Store) BulkWriteFork(workspaceID, forkID string, files []BulkWriteFile) (int, []BulkWriteError)
```

Proposed:

```go
func (s *Store) BulkWrite(workspaceID string, files []BulkWriteFile) (written int, results []BulkWriteResult, errors []BulkWriteError)
func (s *Store) BulkWriteFork(workspaceID, forkID string, files []BulkWriteFile) (written int, results []BulkWriteResult, errors []BulkWriteError)
```

Where `BulkWriteResult` is:

```go
type BulkWriteResult struct {
    Path        string `json:"path"`
    Revision    string `json:"revision"`
    ContentType string `json:"contentType,omitempty"`
}
```

- `written == len(results)` is an invariant maintained by both methods.
- `BulkWriteFork` receives the identical treatment — same struct, same
  ordering guarantee, same invariant. This keeps fork and main-branch writes
  symmetric for the mount code that consumes them.
- The new `BulkWriteResult` type lives next to `BulkWriteFile` /
  `BulkWriteError` in the `relayfile` package and is re-aliased from
  `internal/mountsync/types.go` the same way those two are today.

## 5. Mount-side change

In `reconcileBulkWrite`:

- Before calling `ReadFile`, consult the `revisionsByPath()` map built from
  `BulkWriteResponse.Results`. If the map contains a non-empty revision for
  `pendingWrite.remotePath`, use it directly and skip the `ReadFile` call.
- Preserve the `ReadFile` fallback for the defensive case where the server
  returned no `results` entry or an empty revision (covers rolling deploys and
  future server-side bugs).
- `contentType` resolution follows the same pattern: prefer the value from
  `BulkWriteResponse.Results`, fall back to `ReadFile` only if the bulk
  response did not supply one *and* the local snapshot lacks one.
- The function's signature and the caller in the syncer loop do not change;
  only the internal branching changes.

Net effect: the happy path goes from `POST /fs/bulk` + N × `GET /fs/file` down
to a single `POST /fs/bulk`.

## 6. Test updates

### Server (`internal/httpapi/server_test.go`)

Add a test (or extend an existing one in `TestBulkWriteEndpoint`) that:

- Submits a bulk write with at least one writable file and one file that will
  fail (e.g. permission denied or invalid path).
- Asserts `results` contains exactly the successful paths, each with a
  non-empty `revision` and the expected `contentType`.
- Asserts the failed file is present in `errors` and **absent** from
  `results`.
- Asserts `len(results) == written` and `len(errors) == errorCount`.

### Mount (`internal/mountsync/syncer_test.go`)

Tighten `TestBulkMigrationReducesHTTPCalls`:

- Replace the `fsFilePostCount atomic.Int32` POST-only counter with a single
  counter (or path slice) that records **every** request whose path matches
  `/fs/file` regardless of method.
- Assert that counter is `0` after the bulk sync completes.
- Remove the now-dead `fsFilePostCount` declaration, the POST filter inside
  the handler, and the post-check log line that reports `GET /fs/file`
  read-backs (there should be none).
- Keep the assertion that exactly one `POST /fs/bulk` was made.

## 7. Deliberately out of scope

- FUSE mount path (`cmd/relayfile-fuse`, `internal/fuse/...`) — unchanged.
- SDK / TypeScript bindings — no wire-type regeneration in this change; JS
  consumers continue to ignore the extra field until a separate follow-up.
- Observer / dashboard surfaces — they consume events, not the bulk response;
  no change.
- Any change to `errors` shape, `correlationId` semantics, or the `forkID`
  branching logic.

## 8. Acceptance gates

All three must pass, exactly as written:

```bash
go test ./internal/httpapi/... -count=1 -timeout 120s
go test ./internal/mountsync/... -count=1 -timeout 120s
go build ./...
```

BOUNDARY_READY
