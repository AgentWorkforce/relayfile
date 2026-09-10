# Dead-letter push design (writeback failure → local mirror)

**Status:** draft for discussion
**Audience:** anyone implementing contract §8.4 push-side coverage
**Related:** `docs/productized-cloud-mount-contract.md` §8.4, relayfile#71

## Problem

Contract §8.4 requires the CLI to mirror each dead-lettered writeback op as a JSON record at `.relay/dead-letter/<opId>.json`:

```json
{
  "opId": "op_…",
  "path": "/github/.../review.json",
  "code": "validation_error",
  "message": "body must include event",
  "createdAt": "…",
  "lastAttemptedAt": "…",
  "attempts": 3,
  "replayUrl": "https://…/v1/workspaces/<id>/ops/op_…/replay"
}
```

Today the mirror is populated by **server-pull** (`relayfile ops list` calls `/v1/workspaces/{id}/ops?status=dead_lettered` and reconciles the local directory against the response). That works for the user-facing list/replay flow, but it does not cover the **push** half of the contract: the CLI is supposed to write the record at the moment the writeback fails non-retryably so an offline user can still see the failure without polling.

Push-side coverage has design questions that pull-side did not, and they should be settled before code lands.

## Open questions

### 1. Which failure codes are non-retryable?

Contract §8.4 says: *"a non-retryable error (4xx other than 409/429, or a 5xx after the configured retries)"*. Concrete classification:

| HTTP status | Treatment |
|---|---|
| 409 (revision conflict) | retryable; existing `materializeConflict` path |
| 429 (rate limited) | retryable; backoff |
| 4xx other than 409/429 | **dead-letter immediately** |
| 5xx | retryable up to N attempts; **dead-letter after N** |
| network errors | retryable; not dead-letter |

The retry counter / N is not currently tracked client-side per-op. Either (a) accept that "after N retries" is a server-side decision (server returns the op as dead-lettered, client mirrors via pull), or (b) introduce a `pendingWritebackAttempt` counter in `.relayfile-mount-state.json` per path. Recommend (a) — keeps the client thin.

### 2. Op id source

The bulk write response (`internal/relayfile/store.BulkWriteResult`, `BulkWriteError`) currently carries no `opId`. The single-write `WriteResult.OpID` does. Options:

- **(A) Extend bulk write result/error with `opId`.** Cleanest. Server-side change in `internal/httpapi/server.go` and `internal/relayfile/store.go`. Client deserializer extends `BulkWriteError`/`BulkWriteResult` accordingly. Push-side records use the canonical id; replay against cloud works without translation.
- **(B) Synthetic local id.** Client generates `local_<sha256(path+revision+content)>` and writes the record. Replay against cloud cannot work for these (cloud doesn't know the id). On the next sync cycle, the pull-side reconciliation overwrites the local file with the canonical record once the server assigns one.
- **(C) Defer push entirely.** Stay with pull-only. Acceptable if "offline users see failures immediately" is not a hard requirement.

Recommend (A). The server change is small (~1 field), and it lets the push and pull sides share the same id namespace forever.

### 3. Deduplication with the pull-side mirror

The same op can be written twice: once by push at failure time, once by the next pull-side reconcile. Options:

- **(A) Filename is the opId.** Both writers target `.relay/dead-letter/<opId>.json`. Pull-side overwrites; idempotent. **Recommended.**
- **(B) Push writes a `.pending` file; pull renames to canonical.** Adds complexity for no gain.

If we go with question 2 / option (B) (synthetic ids), the push-side filename is `local_<hash>.json` while the pull-side is `op_<id>.json` — they don't dedupe. Argument against (B).

### 4. State file accounting

`.relay/state.json` tracks `pendingWriteback`, `pendingConflicts`, `deniedPaths`. Should there also be a `deadLetteredOps`? The cloud `/sync` aggregate already reports it; the CLI's `runStatus` reads the cloud value. The local count would only matter offline. Lean toward **no** — single source of truth (cloud) is simpler.

## Proposed implementation order

Assuming (A) for op id source, (A) for dedup:

1. **Server-side**: extend `relayfile.BulkWriteError` (and `BulkWriteResult`) with `OpId string \`json:"opId,omitempty"\`` populated by `Store.BulkWrite` when the writeback queue assigns an id. Add a focused server test.
2. **Client deserializer**: extend `internal/mountsync/types.go`'s aliases / `BulkWriteResponse` to surface the new field. No behavior change yet.
3. **Push writer**: in `internal/mountsync/syncer.go`'s `handleWriteError`, after the existing branches for conflict / forbidden / schema-validation, add a `materializeDeadLetter(opId, path, code, message)` that writes `.relay/dead-letter/<opId>.json`. Only trigger for 4xx-not-409/429 codes.
4. **Pull reconcile**: existing `refreshDeadLetterMirror` in `cmd/relayfile-cli/main.go` already prunes ops the server no longer reports as dead-lettered. No change needed.
5. **Tests**: a `TestBulkWrite_DeadLetterFailureWritesLocalRecord` in `internal/mountsync/syncer_test.go` (mirrors the existing `TestBulkWrite_PerFileConflictCreatesArtifactAndRefreshesRemote` and `TestBulkWrite_SchemaValidationQuarantines*` shape).

Total: ~150 LOC across server + client + one test. Self-contained.

## What this design explicitly defers

- Retry counter on the client (question 1, option b).
- An "opaque pending" file pattern (question 3, option b).
- Local-only `deadLetteredOps` accounting in `state.json` (question 4).

If any of those become necessary later, they can layer in without re-doing the push primitive.
