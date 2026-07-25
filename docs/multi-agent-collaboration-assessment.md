# Multi-Agent Collaboration Assessment (Goal B)

**Scope:** does relayfile today deliver real-time collaboration between two or
more agents running in *different sandboxes* against a shared file set? This
is an architecture assessment, not a bug hunt. Method: read the source,
verify every claim against file:line, and run a real local relayfile stack to
get real numbers for propagation latency and fork contention. No fixes were
implemented. The primary measurements below used standalone daemon processes
in a session scratchpad (not reproducible from this repo); the "Independent
confirmation" subsection under Measurements gives durably reproducible
`go test` equivalents committed at `internal/mountsync/assessment_propagation_test.go`
and `internal/httpapi/assessment_fork_contention_test.go`.

## Verdict

**Status update:** the two gaps below (silent-overwrite on the shared-mount
write path, and false fork contention) that originally motivated the
"partial" verdict have both been fixed and measured — see
`goal-b/phase-0-bulkwrite-revision-check` (PR #370) and
`goal-b/phase-1-per-path-fork-revision`. The verdict paragraph and gap list
below are left in their original form, with inline corrections, so the
document remains an accurate record of what was actually found and in what
order it was fixed — gap #1 and gap #2 are now marked fixed in place rather
than rewritten away.

**Originally partial, and the biggest reason was not what the original
hypothesis guessed.** The tension the brief worried about — shared mount
gives visibility, forks withhold it — is real, but it was not the primary
blocker. The primary blocker was that the shared-mount path, the one goal B
actually depends on for "agents see each other's writes," had **no working
conflict detection on its main write path**. Two sandboxes editing the same
file converge in ~120ms with no explicit refresh (this part always worked,
and works well) *when there is no collision*. **Correction to an earlier
framing of this finding:** it was not a narrow race window. `Store.BulkWrite`
(`store.go:1409-1510`) never compared against the existing file's revision
at all — see the "Independent confirmation" measurement below, which
reproduces the silent overwrite deterministically on every run, not as a
timing-sensitive race. Any push that hadn't first pulled the other side's
write silently overwrote it, whether the two writes were 10ms or 10 minutes
apart (e.g. a sandbox that was offline, or just hadn't hit its next
reconcile). So the loser's write was silently discarded — no error, no
`.relay/conflicts/` artifact, no signal anywhere. This directly contradicted
`docs/guides/collaboration.md`, which documents exactly this scenario and
claimed the opposite outcome. **Fixed in gap #1 below.** Forks solved
the correctness problem but were effectively unusable at any real
concurrency: two agents touching completely disjoint directories failed
~39% of fork commits, and the failure had no recovery primitive short of
discard-and-redo. **The false-contention half of this is fixed in gap #2
below** (0.0% measured for disjoint paths after the fix); the missing
recovery primitive for genuine same-path conflicts (gap #3, rebase) remains
open.

## The two modes, corrected

### Shared mount

**Guarantee it now provides (as of gap #1 / PR #370):** fast, roughly-realtime
propagation of independent writes, **with** conflict detection on the path
real agents use — a concurrent write to a path that moved since the writer
last saw it is rejected and materialized as a `.relay/conflicts/` artifact
instead of silently lost. **Before PR #370** (the state this section
originally documented, kept below for the record) there was no isolation and
no reliable conflict detection at all on that path.

- Propagation is push-based, not the documented 30s poll floor. Websocket
  streaming is on by default (`websocketEnabled := true`,
  `internal/mountsync/syncer.go:1581-1583`; `MaintainWebSocket`,
  `syncer.go:2926`; `connectWebSocket`/`readWebSocketLoop`,
  `syncer.go:3027-3101`). A remote change triggers a targeted per-path GET
  on the receiving mount rather than waiting for the next 30s reconcile
  cycle — confirmed both by log evidence (`mount-a.log`/`mount-b.log`
  showing a single-file GET arrive within the same second as the write, not
  at the next :00/:30 boundary) and by the live measurement below. The
  30s interval (`cmd/relayfile-cli/main.go` mount help text; `-interval`
  flag default in `cmd/relayfile-mount/main.go`) is the **fallback poll
  cadence when the websocket is down**, not the steady-state number — the
  CLI help text itself undersells this ("polls the cloud for every 30s")
  and should be corrected. (Unaffected by gap #1; still true.)
- **Originally**, the write path a real agent uses — edit a file under the
  local mirror — went through `pushLocal` → `POST /v1/workspaces/{id}/fs/bulk`
  (`internal/httpapi/server.go:1581`, `handleBulkWrite`) →
  `Store.BulkWrite` (`internal/relayfile/store.go:1409-1510`), and
  `BulkWriteFile` (`store.go:191-198`) had no revision/If-Match field at all
  — `BulkWrite`'s per-file logic unconditionally overwrote, no
  read-compare-write, no `ConflictError`. **Fixed in gap #1:**
  `BulkWriteFile` now carries an optional `ifMatch`, `BulkWrite` checks it
  the same way `Store.WriteFile` (`store.go:1328`) always did for the
  single-file path, and `mountsync`'s outbox now populates it from the
  locally-tracked base revision.
- **Originally**, `internal/mountsync/syncer.go`'s conflict-artifact
  machinery (`materializeConflict`, `syncer.go:2624`; triggered only by
  `errors.Is(err, ErrConflict)` at `syncer.go:2580`, reachable only from an
  HTTP 409 the bulk path could never produce) was effectively dead code for
  the ordinary shared-mount edit workflow. **Fixed in gap #1:** `BulkWrite`
  now returns a `"conflict"`-coded error the outbox path already knew how to
  turn into a materialized artifact — that machinery needed zero changes,
  it just never received a conflict to react to before.
- `docs/guides/collaboration.md:54-76` documents the "Machine A and Machine
  B both edit `src/main.go`" scenario and asserts: "concurrent changes are
  detected instead of silently overwritten" (line 61) and "RelayFile should
  surface a conflict as a failed or stale write, not as silent data loss"
  (line 75). **This was not what the code did; it is now** (verified live,
  including post-fix, in Measurements below).

### Forks

**Guarantee it actually provides:** true isolation (read-your-writes, not
snapshot — overlay misses fall through to the *live* parent,
`readForkFileLocked`, `store.go:4510`) and atomic, all-or-nothing landing.
No other agent sees anything until commit (confirmed, no events fire
pre-commit). **Originally**, the cost was a single global
optimistic-concurrency gate that serialized **all** forks in a workspace
against each other, regardless of which paths they touch — **fixed in Phase
1 (gap #2 below)**, which narrowed this to per-path. What remains: a
genuine same-path conflict still has no recovery except discard and start
over (gap #3, rebase primitive, still open).

- `Store` has one `revCounter` (`store.go:494`) generating a single
  monotonic `ws.Revision` (`nextRevisionLocked`, `store.go:3531-3534`).
  *Every* write path bumps it — `WriteFile` (`store.go:1359-1372`),
  `BulkWrite` (`store.go:1467-1481`), rename/move
  (`store.go:1468-1481`/`4121-4122`), and a fork's own atomic apply
  (`CommitForkWithValidator`, `store.go:1769-1818`).
  `CreateFork` still snapshots `ParentRevision:
  s.currentWorkspaceRevisionLocked(...)` at open time (`store.go:1675`) —
  kept for a future rebase primitive, but as of Phase 1 it is no longer what
  gates commit. `ForkOverlayEntry` now records a first-touch `BaseRevision`
  per overlaid path, and `CommitForkWithValidator` compares each path's
  `BaseRevision` against the live `ws.Files[path].Revision` instead of the
  single workspace counter. A fork touching `/agent-a/*` now proceeds when
  `/agent-b/*` changes elsewhere; it still correctly fails if `/agent-a/*`
  itself changed live in the meantime (verified: genuine same-path conflicts
  between two forks still return `parent_moved`, and a live parent
  delete/rename of a path a fork touched is still detected).
- There is **no rebase/refresh primitive anywhere in the stack** — store,
  HTTP, TypeScript SDK, CLI, or `packages/core`. Full fork API surface:
  `CreateFork:1641`, `DiscardFork:1693`, `CommitFork:1712`/
  `CommitForkWithValidator:1716`, `ReadForkFile:1835`, `WriteForkFile:1853`,
  `BulkWriteFork:1901`, `DeleteForkFile:1976`, `ListForkTree:2016`,
  `QueryForkFiles:2027` (all `internal/relayfile/store.go`). None re-pin
  `ParentRevision`. HTTP surfaces `parent_moved` as a plain `409` with no
  retry semantics (`server.go:1436-1447`, covered by
  `TestForkCommitReturnsParentMovedConflict`). The TypeScript SDK's error
  dispatcher (`packages/sdk/typescript/src/client.ts:2584-2654`) special-cases
  `revision_conflict`, `invalid_state`, `queue_full`, `payload_too_large` —
  **`parent_moved` has no branch** and falls through to a generic thrown
  error at line 2648. `cmd/relayfile-cli/` has zero fork commands — forks
  are SDK/API-only. A caller that wants to "rebase" must manually snapshot
  its own overlay before discarding (the 409 payload only contains
  `currentRevision`, not the stale overlay) and replay writes into a fresh
  fork — nothing in the codebase does this; no SDK helper, no example.
- `docs/fork-architecture.md` (introduced alongside the fork API,
  commit `8aff6c5`) documents the workspace-wide compare-and-set explicitly
  as a deliberate simplification: *"Commit still compares the stored
  `parentRevision` with the current parent workspace revision and returns
  `parent_moved` if the parent changed."* It gives the reason: *"this
  server does not have point-in-time file reads"* — i.e. forks were
  retrofitted onto a Go `Store` that was never a Durable Object /
  event-sourced design, and workspace-level revisioning is what that Store
  already had lying around, not something chosen after weighing
  contention risk. See gap #5 below (same question for the CRDT/OT decision).

## Measurements

Method: local relayfile server (`RELAYFILE_BACKEND_PROFILE=memory`), local
`relayauth` dev token issuer, two independent `relayfile-mount` daemon
processes (`--mode poll`, default `--interval 30s`, websocket **on**,
separate `--local-dir`/`--state-dir`) simulating two sandboxes on the same
host (loopback network — these are floor numbers, not WAN numbers). Scripts
under `/private/tmp/claude-501/relayfile-assess/` (`poll_latency_multi.py`,
`local_write_latency.py`, `fork_contention.py`).

**Propagation latency, end-to-end agent-writes-locally → visible-in-other-sandbox**
(n=8, sandbox A local file write → sandbox B mirror shows new content):

```
min=0.112s  median=0.119s  max=0.125s  mean=0.119s
```

Tight variance (~13ms spread), consistent with a fixed pipeline: fsnotify
detect → `pushLocal` bulk POST → server apply → websocket broadcast →
targeted GET → local materialize. This is the real goal-B propagation
number, and it is good — sub-second, no explicit refresh needed, as
`evals/suites/concurrency/cases.md`'s `concurrency.realtime-sync` case
expects.

**Direct-API write → visible in both mirrors** (n=15, bypasses the local
push leg to isolate server→client push latency): effectively at poll
resolution, <10ms in every trial — the write's HTTP response and the
websocket-triggered local materialization on both sandboxes land within the
same 10-20ms window. Confirms the propagation floor is the websocket leg,
not the poll interval.

**Same-path race, shared mount (Q2.2/Q2.3):** two sandboxes given a
converged base file, then both write the same path within the same
instant (background shell jobs, no artificial delay). Server log shows both
pushes landing one second apart as two sequential `POST .../fs/bulk` calls,
each succeeding (`rev_82` then `rev_83`, both `status` fields showed
success, no error entries in either response). Final state: **B's write
vanished with zero trace** — no `.relay/conflicts/*` file on either side, no
error surfaced to B's mount, no operation-feed entry indicating a conflict.
This is not a timing artifact of my test: it is the direct, structural
consequence of `BulkWrite` having no revision check (previous section) —
repeatable every time two bulk pushes to the same path race, regardless of
how close together they land, not just "within one sync cycle."

**Fork contention baseline (Q3, before Phase 1), N agents each looping
create-fork → write 3 files under their own disjoint `/agent-N/` directory →
commit, retrying (discard + recreate) on `parent_moved` up to 20 times:**

| agents | commit attempts | parent_moved | rate | wasted wall-time |
|---|---|---|---|---|
| 2 | 49 | 19 | 38.8% | 35.8% |
| 4 | 177 | 118 | 66.7% | 70.7% |
| 8 | 627 | 516 | 82.3% | 84.4% |
| 2 + one unrelated background writer (plain `PUT /fs/file` every 50ms, no forks involved) | 556 | 549 | 98.7% | 98.9% (median 21 retries, most attempts never succeeded within the retry cap) | 

This is despite every agent writing to **completely disjoint paths** — the
whole-workspace CAS means contention scales with *total* workspace write
activity, not with actual overlap. The last row is the more damning number
for goal B: it doesn't take multiple fork-using agents to break this — a
single ordinary shared-mount writer (the *other* collaboration mode) is
enough to make forks nearly unusable in the same workspace.

**Phase 1 confirmation:** the updated in-process `TestAssessForkContention`
harness now asserts a disjoint-path `parent_moved` rate below 5% instead of
only logging it. Measured **0.0%** for 2, 4, and 8 agents in both the
tight-loop and 20ms-think-time variants (all attempts succeeded; no other
errors) — the false-contention baseline above is gone.

**Independent confirmation (in-repo, durably reproducible):** the measurements
above were produced with standalone daemon processes and shell scripts under a
session scratchpad path, which is not part of this repository and will not
survive past that session. As a second, independent check using a different
method — real `internal/httpapi.Server` + real `internal/mountsync.Syncer`
instances (real HTTP, real WebSocket, real `Store`) driven in-process via Go
tests committed to this repo — the same root causes reproduce:

- `go test ./internal/mountsync/ -run TestAssessPropagationLatencyWebSocket -v`
  (`internal/mountsync/assessment_propagation_test.go`): websocket-push
  propagation over loopback, n=25, median=1ms, p95=4ms, max=4ms. Lower than
  the 112-125ms figure above because this harness skips the local
  fsnotify-detect and `pushLocal` bulk-POST legs and measures only the
  server-write → websocket-broadcast → local-materialize leg in a single
  process; the two numbers together bound where the ~120ms end-to-end figure
  is actually spent (mostly in the local push leg, not the server/websocket
  leg).
- `go test ./internal/mountsync/ -run TestAssessPropagationLatencyPollOnly -v`:
  with websocket disabled, convergence latency tracks the poll interval
  exactly (interval=1s → observed≈1.005s), confirming the poll path is a
  simple "wait for next tick," with no additional processing delay.
- `go test ./internal/mountsync/ -run TestAssessSameFileNearSimultaneousWrite -v`:
  deterministically reproduces the silent-overwrite finding — two sandboxes
  pushing conflicting content to the same path both return no error, no
  `.relay/conflicts/*` artifact is created on either side, each sandbox's
  local working copy keeps showing its own (partially lost) edit, and the
  server ends up with whichever write happened to reach `Store.BulkWrite`
  last. This is not timing-sensitive — it reproduces on every run, because
  `BulkWrite` never compares against the existing revision at all (see gap
  #1 below).
- `RELAYFILE_RUN_CONTENTION_BENCH=1 go test ./internal/httpapi/ -run TestAssessForkContention -v`
  (`internal/httpapi/assessment_fork_contention_test.go`; gated behind this
  env var so it doesn't tax every default `go test ./...` CI run — see gap
  #4's CI-hygiene note): fork commit contention among agents on fully
  disjoint paths.
  **Before Phase 1**, tight commit loop over 3s bursts: agents=1 → 0%
  `parent_moved`; agents=2 → 38.2%; agents=4 → 51.1%; agents=8 → 59.4%. A
  second variant adds a 20ms "think time" between fork-open and commit to
  approximate a real agent doing a few writes before committing (rather
  than committing in the same tick it opened the fork): agents=2 → 50.0%;
  agents=4 → 75.0%; agents=8 → 87.5% — and, notably, the raw count of
  *successful* commits stayed flat (~139-140) regardless of agent count,
  while attempts and waste scaled with N. In other words: adding more
  concurrent forking agents did not increase throughput, it only increased
  the fraction of wasted work, because the whole-workspace compare-and-set
  capped how many commits per second the workspace could possibly absorb,
  independent of how many agents were trying.
  **After Phase 1**, the same test (now asserting `parent_moved < 5%` for
  the disjoint-path case rather than only logging it) measures **0.0%** for
  agents=2/4/8 in both the tight-loop and 20ms-think-time variants — the
  false-contention effect described above is gone; the workspace-wide cap
  no longer applies to disjoint work.

Same qualitative shape as the daemon-process measurements above (monotonic
degradation with agent count, worse with longer fork lifetimes), via a
completely independent code path (no shell scripts, no separate OS
processes, no scratchpad state) — two different measurement methods landing
on the same conclusion is stronger evidence than either alone.

**Real cross-machine confirmation (two different physical sandboxes, real
network):** all measurements above ran on one host (loopback or in-process).
To directly test the "real network latency — not verified" gap, the same
propagation and same-path-race experiments were re-run between two actually
separate machines over Tailscale (WireGuard mesh): this laptop
(`khaliqs-macbook-pro`) running the server, and `finn-mac-mini`, a distinct
physical Mac reached via `tailscale ssh` / `100.116.10.46`
(`sf-mac-mini`, a third machine, was offline for this run — last seen 7h
prior — so this is a two-machine, not three-machine, confirmation).

- **Propagation latency, local write → visible on the other physical
  machine:** a persistent remote watcher process on `finn-mac-mini` polled
  its local mirror directory at 5ms resolution and recorded arrival
  wall-clock time; the local machine recorded write wall-clock time; deltas
  were corrected for the two machines' clock skew (~55ms, measured
  separately via paired `date +%s.%N` calls — a rough correction, since the
  skew measurement itself has SSH round-trip noise, but small relative to
  the effect size here). n=10: **min=125ms, median=166ms, max=197ms,
  mean=164ms.** This is the mechanism working correctly end-to-end over a
  real network between real hardware — about 40-70ms higher than the
  ~120ms same-host figure above, consistent with genuine WireGuard-mesh
  network RTT rather than loopback. Still comfortably sub-second, still no
  explicit refresh needed.
- **Same-path race, two physical machines:** repeated the earlier same-path
  race test with the two writers on genuinely different computers (one
  local `echo >` write, one triggered via `ssh finn-mac-mini "echo ... >
  ..."`, launched concurrently). Result: identical to every other run of
  this experiment — one writer's content silently wins
  (`finn-mini-write-...`), the other's is gone with **zero**
  `.relay/conflicts/*` artifacts on either machine. Three independent
  methods (external processes on one host, in-process Go tests, and two
  genuinely separate physical machines over a real network) now agree: this
  is not a timing artifact of any one test harness, it is the code as
  written.

## Gap list (ordered by impact on goal B)

1. **FIXED (`goal-b/phase-0-bulkwrite-revision-check`, PR #370).**
   **Shared-mount concurrent writes to the same path silently overwrite,
   contradicting the documented behavior.** Evidence: `store.go:191-198`
   (`BulkWriteFile` has no revision field), `store.go:1409-1510`
   (`BulkWrite`, no compare-and-set), live race test above, doc
   contradiction at `docs/guides/collaboration.md:56-76`. **Fix:** give
   `BulkWriteFile` an optional `ifMatch`/base-revision field, thread it
   through `pushLocal`'s outbox record (`syncer.go` — the tracked
   `trackedFile.Revision` already exists per-file, `syncer.go` state
   struct, so the client-side data needed is already there), and have
   `BulkWrite` return a per-file `revision_conflict` `BulkWriteError` the
   same way `WriteFile` does. **Size:** days — the per-path revision-check
   logic already exists in `WriteFile`; this is porting it into the bulk
   path plus wiring `handleWriteError`'s existing (already-correct)
   `ErrConflict` → `materializeConflict` branch, which today just never
   receives a conflict to handle. **Risk:** low; this closes a gap rather
   than changing a contract, and the client-side conflict-artifact code
   already exists and is tested for the permission-denial case, so the
   pattern is proven. **This is the highest-impact, lowest-risk fix on the
   list and should happen before anything else.**

2. **FIXED (`goal-b/phase-1-per-path-fork-revision`).** **Fork commit
   contention makes forks impractical above trivial concurrency, and gets
   worse with any unrelated workspace activity.**
   Evidence: measurements above; root cause `store.go:1730-1734` comparing
   a single `ws.Revision` (`store.go:494`) instead of per-path state. A
   per-path base revision is already computed on every fork write
   (`WriteForkFile`'s `If-Match` check against the merged view,
   `store.go:1879-1885`) — it's checked but not *recorded* on the overlay
   entry (`ForkOverlayEntry`, `store.go:114-119` stores the revision the
   write *produced*, not the one it was *based on*). **Fix (implemented):**
   `ForkOverlayEntry` gained a `BaseRevision` field, populated at
   first-touch in `writeForkOverlayLocked` and `DeleteForkFile` (pinned —
   later writes/deletes to the same path within the same fork do not update
   it), and the commit check now iterates `fork.Overlay` comparing each
   path's `BaseRevision` against the live `ws.Files[path].Revision` (a
   missing live path compares as revision `0`) instead of the single
   workspace counter. `fork.ParentRevision` is kept on the fork, unused for
   gating, reserved for gap #3's future rebase primitive. **Measured
   result:** the updated `TestAssessForkContention` harness (now asserting,
   not just logging) measured **0.0% `parent_moved`** for 2, 4, and 8 agents
   on disjoint paths, in both the tight-loop and 20ms-think-time variants —
   down from the 38.2-87.5% baseline above. Genuine same-path conflicts
   between two forks, and a live parent delete/rename of a path a fork
   touched, are both still correctly detected (new tests:
   `TestForkCommitReturnsParentMovedConflict`,
   `TestCommitForkDetectsLiveDirectoryChildRemoval`). **Size (actual):** a
   contained days-scale change, as predicted — one new struct field, pinned
   at first-touch in two call sites (write, delete), plus the commit-check
   loop. **Risk (actual):** low — ACL/permission resolution at commit time
   already read live state fresh (`store.go:1754`), so narrowing did not
   weaken authorization, and no correctness elsewhere turned out to depend
   on `ws.Revision` being a single serialized counter.

3. **No rebase primitive.** Evidence: full API-surface audit above.
   Compounds gap #2 — even after narrowing the check, a fork that
   genuinely does collide on a path it touched still has no recovery
   better than discard-and-redo. **Fix (design, not sized for
   implementation):** a `RebaseFork` endpoint that re-pins `ParentRevision`
   to current, diffs each overlay entry's `BaseRevision` against the live
   file, and returns clean-vs-conflicting paths as structured data instead
   of throwing — reusing `CommitForkWithValidator`'s atomic-apply loop
   (`store.go:1769-1818`) for the actual land. Hard cases needing an
   explicit policy: path deleted upstream (write-after-delete vs
   delete-delete), ACL/schema changed on a path the overlay didn't touch,
   tombstone-vs-upstream-write ordering. **Size:** weeks — this is new
   surface across store, HTTP, and both SDKs, not a local fix. **Do this
   only after #2**, since narrowing the check first shrinks how often
   rebase is even needed.

4. **Docs oversell current behavior.** `docs/guides/collaboration.md:54-76`
   claims conflict-safety the code doesn't have (see gap #1);
   `cmd/relayfile-cli/`'s mount help text undersells actual propagation
   speed (says "polls... every 30s", actual steady-state is sub-200ms via
   websocket). Cheap to fix, should happen alongside #1 so the doc becomes
   true rather than requiring a retraction later.

5. **No same-file simultaneous co-editing (CRDT/OT).** **Correction to an
   earlier framing of this finding:** this *was* scoped and explicitly
   rejected, not merely unaddressed. `docs/relayfile-v1-spec.md:30-35`
   ("Non-Goals (v1)", doc dated 2026-02-17, status "Draft for
   implementation kickoff") lists as goal #4: *"Generic CRDT editing across
   arbitrary providers."* That predates the fork API's implementation
   (`docs/fork-architecture.md`, commit `8aff6c5`, 2026-04-21) by about two
   months — CRDT was ruled out for v1 *before* isolation-via-forks was
   built, which reads as forks being the chosen alternative to merge-based
   co-editing, not a gap nobody thought about. (The fork-API trajectory
   itself — `git show f1ed3e4:.trajectories/active/traj_dnzgabyc2ijd.json`
   — indeed never re-litigates CRDT/OT, consistent with the decision having
   already been made two months earlier rather than being reconsidered at
   fork-design time.) Given gap #1 exists, adding CRDT/OT before fixing
   basic last-write-safety would be solving a problem one layer past the
   one that's actually broken. **Recommendation: not in scope until #1 and
   #2 are fixed** — isolation (forks, once contention is fixed) is almost
   certainly the better answer for agent-to-agent collaboration than
   merge-based co-editing, and this now has an explicit prior decision
   behind it, not just this assessment's opinion; true simultaneous
   same-line editing is a human-collaboration problem (Google-Docs-style)
   that doesn't obviously apply to agents, who don't need sub-second
   keystroke-level merge, they need "did my change survive."

## Recommendation

Gaps #1 and #2 are both fixed (see the gap list above) — the shared-mount
mode is now honest about what it guarantees, and forks no longer
false-contend on disjoint paths. A rebase primitive (gap #3) is the
remaining multi-week investment for genuine same-path conflict recovery,
and is worth less now than it would have been before Phase 1, since the
correctly-narrowed check already eliminated most of what rebase would have
needed to solve for disjoint-path work.

Don't invest in CRDT/OT — it was explicitly scoped out of v1
(`docs/relayfile-v1-spec.md:30-35`, see gap #5), and the two gaps above are
more fundamental anyway: an agent that can't trust its write survived
doesn't need finer-grained merge, it needs the write to survive.

## What was NOT verified (would change the verdict if wrong)

- ~~**Real network latency.**~~ **Now verified** — see "Real cross-machine
  confirmation" above (two physical Macs over Tailscale, median 166ms
  propagation, same silent-overwrite result on the same-path race). What's
  *still* unverified: a third machine (`sf-mac-mini` was offline for this
  run), a non-mesh-VPN network path (Tailscale's WireGuard mesh has its own
  latency/routing profile, not identical to two sandboxes on raw public
  internet or in the same cloud region), and higher-latency/lossy links
  (mobile networks, cross-continent).
- **FUSE mode (`--mode=fuse`)**, only the recommended `--mode=poll` synced
  mirror was tested. FUSE has its own content-cache TTL
  (`-fuse-content-ttl`, default 30s) which could materially change
  propagation and staleness behavior; not measured here.
- **Directory-level delete/rename revision behavior** in the fork overlay
  model — needed to fully validate gap #2's "degrades gracefully" claim.
  Traced the flat `ws.Files` layout and per-file revisioning but did not
  directly exercise a directory delete against a live fork to confirm.
- **Writeback-to-provider path** (adapters writing back to Linear/Notion/
  GitHub/etc.) as a *third* source of concurrent writes into a workspace —
  only mount-to-mount and direct-API writers were used as contention
  sources. Given gap #1's root cause (no revision field on `BulkWriteFile`
  at all) is structural, writeback almost certainly hits the same gap, but
  this wasn't independently exercised.
- **Higher fork concurrency (16+, sustained load)** — only 2/4/8 agents
  over ~15-20s bursts were measured. The trend (38.8% → 66.7% → 82.3%) is
  clearly super-linear-ish but the exact curve shape at higher N, or under
  sustained (not burst) load, wasn't characterized.
- **Whether any private/cloud-only branch changes this picture.** Per prior
  project context, `internal/relayfile/store.go` in this repo is a
  reference implementation and the production cloud service may diverge —
  this assessment is scoped to what's in this repository as of the
  measurements above (commit `071fb80`).
