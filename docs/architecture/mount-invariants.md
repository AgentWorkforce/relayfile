# Mount Invariants

The relayfile mount daemon mirrors a remote workspace to a local directory.
A May 2026 incident showed how a single same-named file rename, combined
with a cloud-side instability, could replace an entire mount directory
with an ~11MB file on disk. The invariants documented here are the
structural defenses now enforced in `internal/mountsync` so that single
failure cannot recur — not through a logic bug, an adversarial path, a
cloud-side OOM, or a flaky network.

The protected invariants are intentionally redundant: each one is the
last line of defense against a different failure mode.

## I1. Mount root is always a directory

At every interaction with the local filesystem the daemon assumes that
`localRoot` is a directory. Code that uses it as a write target (atomic
rename, `os.Remove`, layout materialization) is the proximate cause of
the original clobber; the defenses are:

- `writeFileAtomic` Lstat-checks the target and refuses to rename a
  temp file over an existing directory
  (`internal/mountsync/syncer.go`, `writeFileAtomic`).
- `assertNotMountRoot` is a shared guard invoked at the top of every
  mutating path operation. It bumps `counters.deniedRootTarget` when
  it fires.
- `remoteToLocalPath` rejects any remote path whose cleaned join lands
  on the mount root.
- `localToRemotePath` rejects the round-trip-onto-root collision (a
  child whose name equals the mount-dir basename).
- `RelativeRemotePath` (`internal/mountsync/paths.go`) is a typed
  newtype with a constructor that makes empty / "." / leading-`..`
  / basename-collision impossible to express. `localToRemotePath`
  routes through it so the boundary is gated by type.

## I2. Mount root must exist and be a directory at every cycle

At daemon startup (`preflightMountRootInvariant` in
`cmd/relayfile-cli/main.go`) and at the top of every sync cycle
(`Syncer.assertMountRootInvariant`) the daemon `os.Lstat`s the root and
refuses to continue if it is missing or non-directory.

- On startup the daemon refuses to (re)create a clobbered mount unless
  the operator passes `--reset-after-clobber` (or sets
  `RELAYFILE_RESET_AFTER_CLOBBER=1`). On acknowledgment the offending
  file is renamed to `<localRoot>.clobbered-<utcTs>` (never deleted)
  and the directory is recreated clean.
- An `INCIDENT-<utcTs>.md` is written under `.relay/` (falling back to
  the parent directory or the system temp dir if the root is gone)
  describing the situation and the recovery procedure.
- A typed `ErrMountRootInvariantBroken` (in
  `internal/mountsync/invariants.go`) lets callers distinguish a
  refuse-to-run condition from transient errors.

## I3. Mount root is never a writeback source

`scanLocalFiles` skips any top-level entry whose name collides with
the mount-dir basename (`reservedTopLevel` plus the basename check in
`internal/mountsync/watcher.go`). Combined with I1 this prevents the
self-feeding loop where the cloud serves back the clobber file and the
daemon enqueues it for writeback.

Writeback also enforces a body size cap
(`RELAYFILE_MAX_WRITEBACK_BYTES`, default 8MB) — pathological payloads
are surfaced and dropped from the queue (`counters.skippedOversizeWriteback`).

## I4. Snapshot deletes only fire after revision advancement

`mountState.LastAppliedRevision` tracks the highest cloud revision the
daemon has reconciled. `applyRemoteSnapshotDeletesRev` refuses to
execute deletes unless the freshly-observed revision strictly advances
past it. Older or equal listings cannot authorize destructive ops, even
if every other guard would otherwise allow them.

## I5. Snapshot deletes use two-phase tombstones

A first observation that a tracked path is missing from the cloud only
writes a tombstone under `.relay/pending-deletes/<sha256>.json`
(`internal/mountsync/tombstones.go`). The actual `os.Remove` and
state cleanup only fire on the second consecutive clean confirmation,
i.e. a non-error, non-partial, ratio-safe, revision-advancing pull
that *again* reports the path missing.

Tombstones reset when a path reappears (a frequent cloud flap) and age
out after 24h (`tombstoneMaxAge`) to avoid unbounded growth.

## I6. Cloud-error circuit breaker

`CloudErrorCircuit` (`internal/mountsync/circuit.go`) tracks 5xx /
gateway-style timeouts / transport-level resets within a configurable
sliding window. When it trips:

- destructive snapshot deletes are refused
  (`counters.snapshotDeleteBlocked`);
- writeback flushes are refused — pending local edits stay dirty and
  flush on the next healthy cycle;
- read-only mirror pulls continue.

Configuration (env): `RELAYFILE_CB_WINDOW` (default 60s),
`RELAYFILE_CB_THRESHOLD` (default 5),
`RELAYFILE_CB_COOLDOWN` (default 30s).

`.relay/state.json` surfaces the breaker state under `circuit` and the
cumulative open events under `counters.circuitOpenEvents`.

## I7. Existing snapshot-delete safety ratio

Predating these changes but still load-bearing:

- `snapshotDeleteUnsafe` refuses the delete pass when the fresh remote
  listing is empty while local state tracks files, or when the listing
  has shrunk past `RELAYFILE_SNAPSHOT_DELETE_MIN_RATIO` (default 0.5)
  with a floor of 10 tracked files. Increments
  `counters.snapshotDeleteBlocked`.

## Telemetry surface

All guard activity is counted in `mountState.Counters` (private state)
and mirrored into `.relay/state.json` (public state) and the CLI's
`relayfile status` surface (`syncStateFile.Guards`). Operators can
watch for `circuitOpenEvents` climbing or `deniedRootTarget` becoming
non-zero as early signals that a defense fired.

## Recovery procedure

1. Stop the daemon (`relayfile stop` / Ctrl-C).
2. Inspect `<localRoot>.clobbered-*` if it exists; back up anything
   you need.
3. Confirm cloud state is healthy.
4. Re-run `relayfile mount ... --reset-after-clobber` once to authorize
   recreating the mount directory.
5. Watch the first cycle: `.relay/state.json` should show
   `circuit.open=false` and `counters.snapshotDeleteBlocked` stable.

See also: `internal/mountsync/syncer.go` (proximate guards),
`internal/mountsync/invariants.go` (I1/I2),
`internal/mountsync/tombstones.go` (I5),
`internal/mountsync/circuit.go` (I6).
