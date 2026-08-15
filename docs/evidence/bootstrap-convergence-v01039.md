# Mount bootstrap convergence against v0.10.39

Issue: `AgentWorkforce/relayfile#424`

Captured: 2026-08-15

Baseline: tag `v0.10.39`, commit `ea67a73`, CLI version `0.10.39`

Candidate implementation: commit `7bed3208aa7d`, CLI version `issue-424-7bed3208aa7d`

## Root cause

The blocked operation is the `ReadFile` batch after a successful `ListTree` of
`/neon/advisors/by-project`. v0.10.39 starts every file read for a server page
and waits for the entire worker pool before applying and persisting the page.
When the bootstrap context expires, completed reads at the front of the page do
not advance the durable page offset or `BootstrapFilesSynced`. The next process
therefore lists the same directory and reads the same page from offset zero.

An empty page cursor is not the defect. It is the valid cursor value at this
directory boundary; the complete checkpoint is directory frontier, cursor, and
page offset. The missing convergence boundary was a durable offset inside a
slow page.

The candidate processes at most 32 file entries at a time, applies that prefix,
and persists its page offset before reading the next prefix. It retains the
fetched page in memory while continuing in the same process, so the checkpoint
does not add repeated `ListTree` traffic and is not a new workload limit. The
existing 2,000-file cycle budget, timeout behavior, traversal guard, and stall
terminalization remain the outer safety controls.

## Investigation matrix

| Candidate cause | Evidence | Verdict |
| --- | --- | --- |
| Expanding/cyclic or duplicate Neon alias namespace | A mode-0600 copy of the live private state had 14,822 distinct queued directories, zero duplicates, one queued path under `/neon/advisors/by-project`, and 140 indexed descendants of that path at a fixed relative depth. No live state or mirror was modified. | Not the wedge represented by this checkpoint. The existing distinct-directory traversal guard remains the safety net. |
| `ListTree` or `ReadFile` ignores cancellation | The fixture records exactly one successful `ListTree` before entering `ReadFile`. On deadline and SIGINT, blocked HTTP handlers observe request-context cancellation; after exit, active request count is zero and the child is no longer alive. | `ReadFile` is where the cycle is consumed, but HTTP cancellation works. The defect is the all-or-nothing client batch around it. |
| Page cursor/offset persistence | Both v0.10.39 cycles stay at `("/neon/advisors/by-project", "", 0)`. The candidate durably reaches offset 32 and increases files synced from 27,392 to 27,424 before interruption. | Root cause. |
| Queue de-duplication or processed-directory re-enqueue | The copied frontier contains zero duplicates. The fixture returns a stable page and never emits directory entries, yet v0.10.39 still repeats offset zero. | Ruled out for the reproduced wedge. |
| Workspace overload/backoff consumes the cycle | The deterministic server emits no `429`, `5xx`, retry delay, or expanding tree. A slow/cancelable suffix alone reproduces the v0.10.39 failure. | Overload can amplify latency, but no limit/backoff increase is needed for convergence. |

The live state copy and its file index were kept only in a `mktemp -d` scratch
directory and were not committed. The checked-in fixture contains generated
paths and content only; it preserves the observed frontier/progress counts and
the 140-file Neon-shaped page.

## Built-CLI reproduction

Run:

```sh
scripts/test-bootstrap-convergence-v01039.sh
```

The script exports tag `v0.10.39` into a temporary directory without switching
or resetting the working tree, builds its real CLI and mount helper, builds the
candidate CLI and mount helper, and invokes the built CLI processes against a
local HTTP fixture and scratch state/mirror. The process bound is 150 seconds;
the measured candidate run completed in 12.6 seconds after interruption and
restart.

Redacted transcript from the run above:

```text
binaries: baseline="0.10.39" candidate="issue-424-7bed3208aa7d"

BEFORE cycle=1 elapsed=405ms path=/neon/advisors/by-project cursor=""
  page_offset=0 directories_pending=14822 files_synced=27392
  blocked_operation=ReadFile terminal="context deadline exceeded"
BEFORE cycle=2 elapsed=380ms path=/neon/advisors/by-project cursor=""
  page_offset=0 directories_pending=14822 files_synced=27392
  blocked_operation=ReadFile terminal="context deadline exceeded"

STATUS incomplete
  workspace ws_bootstrap_convergence   mode: poll   lag: 0s
  mount: bootstrapping
  local mirror: [scratch mirror]
  bootstrapping: 27424 files synced (authoritative total unavailable)
    directories pending: 14822
    current path: /neon/advisors/by-project (page offset 32)

AFTER interrupted elapsed=307ms path=/neon/advisors/by-project cursor=""
  page_offset=32 directories_pending=14822 files_synced=27424
  active_requests=0

STATUS complete
  workspace ws_bootstrap_convergence   mode: poll   lag: 0s
  mount: healthy
  local mirror: [scratch mirror]
  daemon: not running

AFTER complete restart_elapsed=12.296s total_elapsed=12.603s
  bootstrap_complete=true directories_pending=0 mirrored_files=140
  active_requests=0 child_alive=false
PASS
```

The test also seeds one future-due pending outbox record. It verifies the record
still exists after the forced shutdown, then makes it due for the restart and
requires exactly one successful bulk dispatch plus an `acked/` receipt before
accepting final healthy status.

## Verification

The following gates passed on the candidate checkout:

```text
go build ./...                                                   PASS
go vet ./...                                                     PASS
go test ./...                                                    PASS
go test -race ./internal/mountsync \
  -run 'TestTreeBootstrapPersistsWithinPageBeforeDeadlineAndResumes|TestInitialTreeBootstrapYieldsAtFileBudgetAndResumes|TestBootstrapStallCycleGuardPersistsAndFailsHard' \
  -count=1                                                       PASS
scripts/check-contract-surface.sh                                PASS
  SDK parity check passed
  contract check passed
scripts/test-bootstrap-convergence-v01039.sh                     PASS
```

## Safe rollout and recovery for `rw_7ccfea89`

This runbook was not executed as part of issue #424.

1. Schedule an owner-controlled maintenance window. Record the deployed binary
   checksum/version and the exact supervisor command/environment. Stop launches
   only through the mount's normal supervisor procedure and confirm no process
   holds the private state or mirror lease.
2. Create a mode-0600 backup in a new operator-owned directory. Take a
   consistent filesystem snapshot (or equivalent recoverable copy) of the
   private state, the mirror including `.relay/outbox`, and supervisor config.
   Validate the backup can be read and record checksums. Never copy it into the
   repository or an agent-visible shared path.
3. Capture the starting tuple: first queued directory, page cursor, page offset,
   pending-directory count, files-synced count, `BootstrapComplete`, stall
   cycles, and pending/acked/failed outbox counts.
4. Install the release containing `7bed3208aa7d` or its reviewed successor.
   Deliberately re-arm by launching that binary once with the unchanged mirror,
   private-state path, scopes, credentials, and existing operational limits.
   Do not clear the frontier, cursor, page offset, stall record, file index, or
   outbox. A real checkpoint advance resets the consecutive-stall counter; a
   limit increase is neither required nor part of this recovery.
5. During each bounded cycle, compare the full checkpoint tuple. Require
   monotonic evidence: page offset or cursor advances, the first directory is
   eventually removed, pending directories decline, and durable files synced
   increase. Confirm exited processes leave no child, lease, or request alive.
   Confirm pending outbox records remain pending or move to explicit `acked/`
   or `failed/` receipts; a disappearing record is a rollback trigger.
6. Keep mount status at `bootstrapping` until private state reports
   `BootstrapComplete=true`. Only then accept `mount: healthy`, restore normal
   supervision, and retain the backup through an observation window.
7. Roll back if two bounded cycles repeat the identical full checkpoint, a
   request/child survives cancellation, the frontier grows unexpectedly, any
   outbox record disappears without a receipt, the mirror reports destructive
   deletes, or status becomes healthy before completion. Stop the candidate,
   preserve its state/logs for diagnosis, and restore the prior binary plus the
   consistent pre-rollout state/mirror snapshot before re-enabling supervision.
