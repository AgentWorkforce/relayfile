# One-way mount propagation latency — methodology

Date: 2026-08-07
Author: `herdr-t2-relayfile-latency-b1`
Status: written **before** any trial was run, per the measurement brief.

## Why this run exists

The public claim is currently:

> sub-200ms end-to-end including measurement overhead that exceeds the signal

That wording is deliberately hedged because the number behind it is not a
one-way measurement. It comes from
`docs/evidence/real-time-collaboration-2026-07-26/`, which measured a **round
trip** — `sf-initiated` median 315.526 ms / p95 372.479 ms over n=12, and
`finn-initiated` median 373.230 ms over n=12 — using
`scripts/measure-mount-latency.rb`. That script's own header says the
initiator "measures ping-to-ack time with its own monotonic clock, so
separate-machine wall-clock skew is absent". Avoiding skew that way is sound,
but it means:

1. The one-way figure was inferred by halving a round trip. A round trip is
   not symmetric: the ack leg includes a *second* full write-and-propagate,
   plus the responder's scheduling delay.
2. The responder polled the directory every 5 ms
   (`measure-mount-latency.rb:39,93`), so every sample carries up to 5 ms of
   quantisation on each leg.
3. n=12 per direction is too small for a credible p95.

This run replaces the inference with a directly measured one-way number.

## What is being measured

**End-to-end one-way propagation:** from the instant a writer on the sender
host issues a file write, to the instant that file's content is readable on
the receiver host's mounted workspace.

The receiver is `sf-mac-mini` (Tailscale `100.102.30.76`), as required by the
brief. The sender and the relayfile server are `khaliqs-macbook-pro`
(Tailscale `100.89.219.17`).

### Which latency the watcher actually observes

This matters, and it was settled by reading the delivery path before any trial
was run.

`relayfile-cli mount` runs in `poll` mode by default
(`cmd/relayfile-cli/main.go:58`). Despite the name, `poll` does **not** mean
"poll the server for changes" — it means "materialise a mirror of real files
on local disk", as opposed to `fuse` (which this build hard-refuses,
`cmd/relayfile-cli/main.go:6687-6689`). Remote changes arrive over a
websocket subscription to `/v1/workspaces/{id}/fs/ws`
(`internal/mountsync/syncer.go:3251-3260`). On each event the daemon calls
`applyWebSocketEvent` (`syncer.go:3310`), which does `ReadFile`
(`syncer.go:3320`) then `applyRemoteFile` (`syncer.go:3339`), which
`writeFileAtomic`s the bytes to the local path (`syncer.go:6033-6051`).

Two consequences:

- This is a **push**, not a pull. The receiver's `stat`/`read` on the mirror
  is a purely local syscall with no network hop. So an in-box watcher on the
  receiver measures propagation, not its own fetch. Had the mount been FUSE,
  the websocket would only *invalidate* cache
  (`internal/mountfuse/wsinvalidate.go:158-165`) and the next lookup would
  trigger a synchronous server fetch — a watcher would then have been
  measuring its own pull, and the number would have been meaningless.
- The measured interval therefore covers: sender write → server ingest and
  `publishEvent` → websocket fan-out → receiver daemon `ReadFile` round trip →
  `writeFileAtomic` to local disk → watcher observation. That is the full
  chain an agent on the receiver actually waits for, which is what "end-to-end"
  should mean.

### Known windows on the path, and how they are handled

| Window | Default | Handling |
|---|---|---|
| Server envelope coalesce | 3 s (`internal/relayfile/store.go:883-885`) | Applies to duplicate inbound *provider envelopes* (`store.go:3605`), not to direct fs writes. Neutralised anyway: every trial writes a unique path, and trials are spaced beyond the window. |
| Mount reconcile tick | 30 s, and only every 10th cycle when websocket is on ≈ 5 min (`cmd/relayfile-cli/main.go:57,13209-13211`) | This is the safety net, not the delivery path. Any sample anywhere near these values means the websocket did not deliver; such samples are reported, never silently dropped. |
| SDK `subscribe()` coalesce | 200 ms (`packages/sdk/typescript/src/client.ts:189`) | Not on this path — the SDK is not used. Noted because it would otherwise silently add 200 ms to an SDK-based measurement. |
| Receive-path debounce | none in poll mode | `applyWebSocketEvent` applies inline with no timer. |

## Clock handling

The two hosts' clocks are **not** assumed equal, and were not equal: sf-mini's
realtime clock measured **6.441 ms behind** this laptop. On a ~150 ms signal
that is a ~4% systematic error, and on any faster path it would matter much
more.

Offset is measured with NTP's four-timestamp formula over a raw TCP exchange
on the Tailscale LAN (`harness/clock-offset.py`):

    delay  = (t3 - t0) - (t2 - t1)
    offset = ((t1 - t0) + (t2 - t3)) / 2      # receiver_clock - sender_clock

Both formulas assume path symmetry, which is weakest under queueing, so many
samples are taken and the one with the **smallest delay** is selected — the
least-queued sample is the least asymmetric. Residual uncertainty is bounded
at ±delay/2 and is reported alongside the result, so the final latency carries
an honest error bar rather than a false precision.

An ssh-based clock comparison was rejected: its round trip is of the same order
as the signal being measured, so it could not bound the offset usefully.

Offset is measured **before and after** the trial block. The difference bounds
relative clock drift over the run; if drift is material it is reported as part
of the uncertainty rather than ignored.

## Receiver watcher

An in-box resident watcher runs on sf-mini and records arrival timestamps
**locally**, using sf-mini's own `CLOCK_REALTIME` via `time.time_ns()`. No
timestamp is taken over ssh, because ssh round-trip would be added to every
sample.

Detection uses a tight `stat` poll loop over the local mirror. Since the mirror
is real local disk (established above), each poll is a local syscall costing
microseconds, so the loop can run at a ~1 ms period without meaningful cost.
That 1 ms is the quantisation floor, versus 5 ms in the prior run.

**Measurement overhead is itself measured, not assumed.** A control experiment
creates files locally on sf-mini — same directory, same watcher, no network
involved — and records the watcher's own detection delay distribution. The
reported latency is quoted both raw and with this control subtracted, so the
"measurement overhead exceeds the signal" hedge can be either retired or
confirmed with a number instead of a guess.

## Trial design

Two populations, per the brief:

- **Small-file trials (n ≥ 20).** A few hundred bytes. Isolates propagation
  latency with transfer time near zero.
- **Realistic repo-sized change-set trials (n ≥ 20).** A change set with the
  size and file-count profile of an actual commit in this repository, so the
  number quoted is one a reader can expect from real agent work rather than
  from a synthetic best case.

Each trial writes a unique path, so no trial can be coalesced into, or
satisfied by, a previous one. Raw per-trial records are appended to
`raw/*.jsonl` **immediately** as each trial completes, so a mid-run tool or
host failure leaves usable evidence rather than nothing.

## Liveness gate

sf-mini's participation is gated on its **own `lastHeartbeatAt` advancing**
across ≥90 s, sampled before the trials and again at result time.

Two weaker signals are explicitly rejected:

- **Absence from a fleet listing is not evidence of offline.** The listing
  returns nondeterministic subsets.
- **`status` and `live` are registration fields, not liveness fields.** During
  the pre-trial gate window sf-mini's `status` flipped `online`↔`offline` four
  times while its heartbeat advanced monotonically, and an MCP `query_nodes`
  call at 11:08Z reported it `offline`/`live:false` while its heartbeat was
  38 s old and advancing. Only monotonic advance is trusted.

## Failure policy

If the propagation path or either liveness gate fails, partial raw data is
preserved and an explicit blocker artifact is written. **No median is salvaged
from a failed run.** Until valid results exist the public claim stays exactly:

> sub-200ms end-to-end including measurement overhead that exceeds the signal

and is never stated as sub-100ms.

**Outcome (added after the run):** both liveness gates passed, the propagation
path held for all 52 trials, and 26/26 named assertions pass. Results are in
[`RESULTS.md`](RESULTS.md). Both halves of the claim above turned out to be
wrong — "sub-200ms" is false for realistic repo-sized change sets (median
216.7 ms), and the measurement overhead is ~1.2 ms against signals of 20.2 ms
and 216.7 ms, so it does not exceed the signal and that wording is retired. No
sub-100ms claim is made: the figures come from a loopback-plus-LAN topology
that does not represent the hosted product path.

## Isolation and cleanup

The pre-existing `.dev-collab-stack/` and `.salvaged-from-minis/` directories,
their processes, ports, state, and the existing sf-mini mounts are **not
touched or reused**. This run stands up a fresh server on a separate port with
a separate state directory, a fresh workspace, and a distinct receiver mount
path. Cleanup instructions are recorded in `CLEANUP.md`.

Test credentials are minted fresh for this run, are short-lived, and are never
written to any artifact or transmitted over Relay.
