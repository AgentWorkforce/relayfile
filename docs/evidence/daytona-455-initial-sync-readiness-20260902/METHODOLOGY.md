# Methodology — relayfile#455 initial-sync readiness, live Daytona sandbox

Date: 2026-09-02
Subject: AgentWorkforce/relayfile PR #457, head `4e3c1105`
(`fix/455-state-json-single-writer`)

## What was being tested

#455 was a 100% failure rate on JIT sandbox provisioning. PR #457 claims the
mechanism is structural, not a race:

1. one `Reconcile` mirrors at most `defaultBootstrapMaxFilesPerCycle` = **2000**
   files (`internal/mountsync/syncer.go`), persists a resume cursor and yields
   with `traversal_complete=false`;
2. `markSyncSuccess()` still stamps `lastSuccessfulReconcileAt` and `run()`
   returns nil, so `--once` **exits 0**;
3. but `.relay/state.json` still carries a non-null `bootstrap` block;
4. the sandbox readiness guard requires `bootstrap == null`, so it **exits 75**.

Therefore any workspace larger than 2000 files could never satisfy the barrier.

## Why the existing evidence was not enough

PR #457's own evidence is `sandboxInitialSyncGuard` in
`cmd/relayfile-mount/initial_sync_readiness_test.go` — a Go **transcription** of
the guard. The transcription is faithful, but a transcription cannot show that
a sandbox provisions. **No sandbox had ever provisioned with this build.** This
run closes exactly that gap.

## The guard is imported, never reimplemented

The readiness guard used here is the real one:

```
buildRelayfileMountInitialSyncCompletionGuardShell   (via the launcher below)
```

imported from `AgentWorkforce/sandbox` at `origin/main` `cfdf801`
(`chore(release): 0.1.14`) — the exact version `cloud/packages/core` depends on
(`"@agent-relay/sandbox": "0.1.14"`). It is invoked the way provisioning invokes
it, through `buildRelayfileMountInitialSyncBackgroundShell`, the **detached**
launcher. That matters: Daytona's exec proxy read-times-out around 120s, so a
single foreground exec cannot host a real initial sync; production launches
detached and polls an exit sentinel, and so does this run.

Guard contract, verbatim from the emitted shell: a root is incomplete when
`state.bootstrap != null`, or `lastSuccessfulReconcileAt` is not a non-empty
string, or the file cannot be read/parsed. Incomplete ⇒ `process.exit(75)`
(`sysexits.h` TEMPFAIL).

## Environment

| | |
|---|---|
| Daytona API | reachable; `GET /api/sandbox` → 200 with the org key, **401 with a deliberately bogus key** (the probe discriminates) |
| Sandbox | `8a7d6049-ca63-447b-abdf-f750f121c994`, snapshot `relay-orchestrator-sdk-11.8.2-relayfile-v0.10.50-runtime-4.1.52` |
| Sandbox host | x86_64, Debian GNU/Linux 13 (trixie), node v25.6.0, user `daytona` |
| Driver host | local macOS (Darwin 25.5.0, arm64), go1.26.1 |
| `go build ./...` at `4e3c1105` | exit 0 |
| Remote workspace | `rw_7ccfea89` on `https://file.agentrelay.com` |

## The two arms

Both arms run the **identical generated command** against the **identical
remote subtree**. They differ in exactly one thing: which `relayfile-mount`
binary is first on `PATH`.

| Arm | Binary | sha256 | Expected |
|---|---|---|---|
| A | snapshot-baked `/usr/local/bin/relayfile-mount` | `c39bbd0845adeea9fa3c72987a147e6fb35ef8ca5e32a4f1563057798678c84a` | exit **75** |
| B | built from `4e3c1105`, `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` | `d3fe2cb55b4bcf262355bdbd4a3ef546e964265d1b75c0d3590bebd4df7ced78` | exit **0**, `bootstrap: null` |

Arm B's binary is placed in `/home/daytona/binB` and reached with
`PATH=/home/daytona/binB:$PATH`, so the shell string itself is byte-identical
between arms.

Two scopes were run:

- **bounded pair (A2/B2)** — `/github/repos/AgentWorkforce/relay`. This is the
  decisive pair: large enough to cross the 2000 budget, small enough to finish.
- **large pair (A/B)** — `/github/repos/AgentWorkforce`, >15,000 directories.
  Arm A completed; arm B was still resuming when the run was reported.

## Proving the fixture actually reached the mechanism

A passing proof is worthless if the fixture never crossed the budget, so the
file count is taken from the instrument under test rather than estimated. Arm
A2's own traversal summary reports:

```
bootstrap file budget reached (2000 files this cycle, max 2000); yielding at entry 170 of the current server page
mount full-tree traversal summary remote_root="/github/repos/AgentWorkforce/relay"
  list_calls=6 entries_seen=3986 files_seen=2000 directories_seen=1986
  bytes_seen=26734874 traversal_complete=false traversal_failed=false
```

`traversal_complete=false` with 887 directories still pending is the proof the
subtree exceeds 2000 files; arm B2 then mirrored past 2000 on the same subtree,
which is the independent confirmation.

## Rules followed

- **Exit codes, not output.** Every arm's verdict is the exit sentinel written
  by the detached runner, not a string in stdout.
- **Every probe bounded**; a timeout is reported UNKNOWN, never a pass.
- **UNKNOWN is a valid answer.** No local reproduction was substituted for a
  sandbox run at any point.

## Two false signals encountered, and how they were separated

Recording these because each would have produced a confident wrong answer.

1. **An exit 75 that was not the mechanism.** The first arm-A run exited 75 —
   the expected code — but the log read
   `http 403 forbidden: missing required scope: fs:read` and `filesSynced: 0`.
   The token had been minted with `files:read`-style scopes instead of
   relayfile's `fs:read`/`fs:write`/`sync:read`/`sync:trigger`. The guard
   returns 75 for *any* incomplete bootstrap, including one that never started,
   so **75 alone does not prove the budget mechanism** — it must be paired with
   `files_seen=2000` and `traversal_complete=false`.

2. **A 401 that was an artifact of the instrument.** Reproducing the
   workspace-create failure with `curl` returned 401. That was not the server:
   `agent-relay cloud session --json` **masks** the token (12 chars, contains an
   ellipsis). Raw evidence was obtained instead by building a patched
   `relayfile-cli` from `4e3c1105` that dumps status and body, so it reuses the
   real session rather than extracting a credential.

## Reproducing

`scripts/test-sandbox-initial-sync-readiness-e2e.mjs` runs both arms and
asserts A fails 75 / B passes 0. It exits 2 (UNKNOWN) — not 0 — when it cannot
provision, when an arm times out, or when arm A *passes*, since a must-fail
control that does not fail means the fixture never reached the mechanism.
