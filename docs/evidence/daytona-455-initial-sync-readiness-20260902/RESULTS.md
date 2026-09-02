# Results — relayfile#455 initial-sync readiness, live Daytona sandbox

Date: 2026-09-02
Subject: PR #457 head `4e3c1105` (`fix/455-state-json-single-writer`)

## Verdict: CONFIRMED

The #457 claim is true against a real sandbox. The barrier is structural, not a
race, and the fix clears it.

## Both exit codes

Decisive pair, remote root `/github/repos/AgentWorkforce/relay`, same sandbox,
same subtree, same generated command; the arms differ only in which
`relayfile-mount` is first on `PATH`.

| | arm A2 — snapshot-baked binary | arm B2 — built from `4e3c1105` |
|---|---|---|
| **guard exit code** | **75** | **0** |
| files mirrored on disk | 2,000 | 5,685 |
| `bootstrap` in state.json | non-null, `filesSynced: 2000` | **key absent** |
| `lastSuccessfulReconcileAt` | `2026-09-02T12:18:24.303486294Z` | `2026-09-02T12:47:48.742036968Z` |
| `status` | `bootstrapping` | `ready` |
| state.json size | 324,833 bytes | 1,028,186 bytes |
| state.json sha256 | `3d59ae71dff2…37dc1d43` | `39329c7c14f5…14dad359` |

Raw bytes: `raw/armA2-state.json.gz`, `raw/armB2-state.json.gz` (they
decompress to exactly the sha256s above, taken inside the sandbox — see
`raw/SHA256SUMS`). Full logs: `raw/arm*-initial-sync.log`.

## File count, and how it was counted

**5,685 files**, well over the 2,000-per-cycle budget. Counted three
independent ways, all agreeing:

1. arm A2's own traversal summary — `files_seen=2000` with
   `traversal_complete=false` and 887 directories still pending;
2. `find` on the arm B2 mirror after completion — 5,685 files;
3. arm B2's `files` map in state.json — 5,685 entries.

The mechanism was therefore genuinely reached: the budget was hit and the
traversal yielded incomplete. This is not an estimate over a subtree that
"pages deep" — it is the instrument under test reporting that it stopped at its
own limit.

## The mechanism, verbatim from arm A2

```
12:18:22 bootstrap file budget reached (2000 files this cycle, max 2000); yielding at entry 170 of the current server page
12:18:22 mount full-tree traversal summary remote_root="/github/repos/AgentWorkforce/relay"
         list_calls=6 entries_seen=3986 files_seen=2000 directories_seen=1986
         bytes_seen=26734874 traversal_complete=false traversal_failed=false duration_ms=103291
12:18:24 mount sync cycle completed          <-- --once reports SUCCESS
relayfile initial sync paused before complete readiness   <-- guard exits 75
```

All four claimed steps are visible in one run: the 2,000 budget is hit; the
cycle reports success and `--once` exits 0; `lastSuccessfulReconcileAt` is
stamped anyway; `bootstrap` is still non-null; the guard exits 75.

## The fix, verbatim from arm B2

```
12:18:25 bootstrap file budget reached (2000 files this cycle, max 2000); yielding at entry 170 of the current server page
12:18:27 initial sync: bootstrap incomplete after first cycle (2000 files synced); resuming from the persisted checkpoint
12:19:13 resuming bootstrap bounded-tree pull ... page offset 170 (887 directories pending, 2000 files already synced)
12:32:30 bootstrap file budget reached (2000 files this cycle, max 2000); yielding at entry 1 of the current server page
12:33:12 resuming bootstrap bounded-tree pull ... (474 directories pending, 4000 files already synced)
12:47:49 initial sync: bootstrap complete
```

Three cycles: 2,000 → 4,000 → 5,685, then complete. Total wall clock ~31 min.

**The control is as tight as it gets.** Both arms' first cycles are numerically
identical — `list_calls=6 entries_seen=3986 files_seen=2000
directories_seen=1986 bytes_seen=26734874`, both yielding at entry 170 with 887
directories pending. Same fixture, same budget, same stopping point. They
diverge only in what happens next: A2 exits, B2 resumes.

## Second, larger pair (reported for completeness)

Remote root `/github/repos/AgentWorkforce` (>15,000 directories):

- arm A: **exit 75** at `files_seen=2000`, `traversal_complete=false`, 2,006
  files on disk, `bootstrap` non-null. Same mechanism.
- arm B: still resuming when this was written (14,000+ files across 7+ cycles,
  advancing normally). **Reported as incomplete, not as a pass.**

## Notes worth carrying forward

1. **The fixed build removes the `bootstrap` key rather than setting it to
   `null`.** The guard tests `state.bootstrap != null`, and loose `!=` treats
   `undefined` as null, so this passes. A strict `!== null` would have failed
   it. The guard and the writer agree today by virtue of a loose comparison.

2. **Key ordering changed.** Arm A2's state.json preserves Go struct field
   order; arm B2's is alphabetically sorted, because `mountstate.MergeFunc`
   round-trips the document through a map. Benign for the guard (it parses
   JSON), but it makes byte-level diffs between old and new builds noisy.

3. **An exit 75 does not by itself prove the budget mechanism.** An early run
   here returned 75 with `filesSynced: 0` because the token carried the wrong
   scopes (`403 missing required scope: fs:read`). The guard returns 75 for any
   incomplete bootstrap, including one that never started. 75 must always be
   read alongside `files_seen=2000` and `traversal_complete=false`.

## Delivery-path findings (outside the claim, but they change what "fixed" means)

1. **The mount binary is baked into the Daytona snapshot, not resolved at
   provision time.** Snapshot names encode it
   (`…-relayfile-v0.10.50-runtime-4.1.52`), and the newest snapshot built on
   2026-09-02 still carries v0.10.50. Merging #457 and publishing to npm is
   therefore **not** sufficient: a snapshot rebuild and a fleet snapshot-pin
   bump are required before any sandbox sees the fix.

2. **The snapshot's version labelling does not match its contents.** The
   snapshot is named `v0.10.50`, but `@relayfile/mount-linux-x64` inside it is
   **0.10.51**, and three *different* `relayfile-mount` binaries are present
   with three different sha256s:
   - `/usr/local/bin/relayfile-mount` — `c39bbd0845adeea9…`
   - `…/node_modules/@relayfile/mount-linux-x64/bin/relayfile-mount` — `7af63c60c3792be1…`
   - `…/@relayflows/core/node_modules/@relayfile/mount-linux-x64/bin/…` — `0ad3f7b0215d6d6a…`

   Which one runs depends on `PATH`. "Which mount version is in production" is
   currently not answerable from the snapshot name.

3. **`tokenIngress: 'env'` in `@agent-relay/sandbox` is broken.** It renders
   `RELAYFILE_MOUNT_TOKEN=…`, but `cmd/relayfile-mount/main.go` reads only
   `RELAYFILE_TOKEN` (and `RELAYFILE_MOUNT_CREDS_FILE`). No relayfile-mount
   build — including this PR's head — reads `RELAYFILE_MOUNT_TOKEN`. Observed
   directly: the arm died with
   `token is required (--token, RELAYFILE_TOKEN, or --creds-file)`, exit 1,
   before the guard ever ran. The package's own doc comment says "Confirm that
   capability before flipping the default to `'env'`" — it is wrong today.
   `'creds-file'` (what fleet uses) works and was used for these runs.
