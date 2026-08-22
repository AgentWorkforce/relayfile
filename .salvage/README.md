# Salvage — slack-writeback-deadletter-0820 (2026-08-21)

This lane was editing the **shared** `relayfile` checkout directly on
`feat/realtime-agent-collaboration` — PR #435's branch — while a different
agent owns that PR's review feedback. It then went idle for ~21h and, being
past the relay#1593 deafness boundary, could not be told to stop or hand off.

`slack-writeback-deadletter-0820.tracked.patch` is its uncommitted tracked
diff, captured verbatim before release.

## What is already upstream and what is not

Applied with `git apply --3way` against `a9cb114`:

| file | result |
|---|---|
| `.trajectories/index.json` | clean |
| `cmd/relayfile-mount/main.go` | clean |
| `internal/mountsync/syncer.go` | clean |
| `internal/mountsync/watcher.go` | **conflict — 52 net new lines** |
| `internal/mountsync/watcher_test.go` | **conflict — 13 net new lines** |

So most of this work is already in `a9cb114 feat: accelerate real-time mount
replication`. The residue is 65 lines across the watcher and its test.

## Why the conflicts were NOT resolved here

Resolving them means guessing the lane's intent, and the lane cannot be asked.
Whoever owns `internal/mountsync` should decide whether those 65 lines are
superseded by what landed in `a9cb114` or still wanted. Do not merge this
branch as-is.
