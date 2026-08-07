# One-way mount propagation latency — measured result

Date: 2026-08-07
Base commit: `ea67a73` (`chore(release): v0.10.39`)
Sender: `khaliqs-macbook-pro`, Tailscale `100.89.219.17`
Receiver: `sf-mac-mini` ("sf-mini"), Tailscale `100.102.30.76`
Method: [`METHODOLOGY.md`](METHODOLOGY.md) — written before any trial was run
Assertions: 26/26 pass (`harness/assertions.py`)

---

## The headline

**A realistic repo-sized change set does not propagate in under 200 ms.**

Two populations were measured, and they land on opposite sides of the claim
that was being checked:

| Change set | n | median | p95 | min | max |
|---|---|---|---|---|---|
| Single small file (~300 B) | 20 | **20.2 ms** | 161.7 ms | 12.3 ms | 183.3 ms |
| Repo-sized change set (11 files, ~14 KB) | 20 | **216.7 ms** | 303.9 ms | 165.9 ms | 328.9 ms |

**Every number in this table holds only under this precondition: the relayfile
server ran on the sender's own machine, so the sender→server leg was loopback,
and the only network hop was server→receiver across a Tailscale LAN between two
Macs.** This is a best case, not the product's path. See
[Topology precondition](#topology-precondition) — it is repeated beside every
figure in this document deliberately, so no figure can be lifted out without it.

### Quotable statement

> Measured 2026-08-07 on relayfile `ea67a73`: with the server on the sender's
> own machine and a Tailscale LAN between two Macs, a single small file
> propagates to a second machine's mount in a median of 20.2 ms (p95 161.7 ms,
> n=20), while a repo-sized change set of 11 files / ~14 KB takes a median of
> 216.7 ms (p95 303.9 ms, n=20). These are LAN best-case figures with one
> network hop; they are not measurements of the hosted product path.

## What this does to the existing claim

The claim under test was:

> sub-200ms end-to-end including measurement overhead that exceeds the signal

Both halves are wrong, in opposite directions.

**"sub-200ms end-to-end" is false for realistic change sets.** It holds only
for a single small file. A repo-sized change set — the shape actual agent work
produces — has a median of 216.7 ms and a p95 of 303.9 ms, and that is *with*
the sender→server leg on loopback. Add a real network hop on that leg and the
figure can only get worse. Small single files were never the interesting case;
agents commit change sets.

**"measurement overhead that exceeds the signal" is false by more than an
order of magnitude, and is retired by this run.** The watcher's own detection
delay was measured, not assumed: 25 local create/detect pairs on the receiver,
same clock, same watcher code, same filesystem, no network, published by atomic
rename to match how the mount daemon materialises remote content.

| Watcher detection delay | min | median | p95 | max |
|---|---|---|---|---|
| Control, n=25 | 0.360 ms | **1.225 ms** | 2.349 ms | 2.455 ms |

1.2 ms of overhead against a 20.2 ms small-file signal and a 216.7 ms
change-set signal. The overhead is ~6% of the smaller signal and ~0.6% of the
larger one. No result here is overhead-limited, and the hedge should not be
repeated.

### Where the old number came from

`docs/evidence/real-time-collaboration-2026-07-26/` measured a **round trip**:
sf-initiated median 315.526 ms / p95 372.479 ms (n=12), finn-initiated median
373.230 ms (n=12). The public one-way figure was that round trip halved. Three
problems: a round trip is not symmetric (the ack leg is a second full
write-and-propagate plus the responder's scheduling delay); the responder
polled at 5 ms granularity (`scripts/measure-mount-latency.rb:39,93`); and
n=12 per direction cannot support a p95. That run was honest about avoiding
clock skew — it used a single monotonic clock deliberately — but the cost of
that choice was that it could not produce a one-way number at all.

## Leg decomposition

Because the sender records both its send time and the server's acknowledgement,
the two legs separate cleanly.

| Leg | Small file (n=20) | Repo-sized (n=20) |
|---|---|---|
| A — sender → server (**loopback, not network**) | median 3.1 ms, p95 7.0 ms | median 3.7 ms, p95 8.1 ms |
| B — server → receiver mount (**Tailscale LAN**) | median 15.8 ms, p95 146.8 ms | median 212.4 ms, p95 300.3 ms |
| End-to-end | median 20.2 ms, p95 161.7 ms | median 216.7 ms, p95 303.9 ms |

Leg A is ~3 ms because it never touches a network. In any real deployment leg A
is a WAN request and this decomposition is the reason the end-to-end figures
above cannot be carried over to the product.

Leg B carries essentially all of the change-set cost: 212.4 ms of the 216.7 ms
median. The receive path is `applyWebSocketEvent` → `ReadFile` →
`writeFileAtomic` per file (`internal/mountsync/syncer.go:3310,3320,3339`), so
an 11-file change set costs 11 sequential server round trips on the receiver's
side after the single websocket notification. That is the dominant term, and it
scales with file count rather than with bytes — which is why the change-set
median is ~10× the single-file median for only ~47× the bytes.

## Topology precondition

Repeated here in full because every figure in this document depends on it:

- The relayfile server ran on the **sender's own machine**
  (`100.89.219.17:18299`). The sender→server leg was loopback.
- The only network hop was **server→receiver**, across a Tailscale LAN between
  two Macs on the same tailnet, min RTT ~4.5 ms.
- The receiver was a Mac mini on that same tailnet, not a typical end-user
  machine on a typical network.
- Fresh isolated server, fresh workspace `ws_latency_20260807`, fresh mount.

### What a product claim would require

None of the figures above license a claim about the hosted product. To make
one, the measurement needs all three of:

1. **The server off the sender.** Leg A must be a real request over the
   network, not loopback. As measured, leg A contributes ~3 ms; in production
   it is a WAN round trip and will likely dominate the small-file case.
2. **A real WAN path**, not a same-tailnet LAN with ~4.5 ms RTT. Tailscale
   here negotiated a direct connection over the local network.
3. **A receiver that is not a Mac mini on the sender's own tailnet** — an
   ordinary client on an ordinary network, including the tail of poor
   connectivity that a p95 is supposed to capture.

Until then the honest public position is that the sub-200 ms claim is
unsupported for realistic change sets *and* that no faster claim can be made
either. **This run does not support a sub-100 ms claim and none should be
made**, notwithstanding that the small-file median is 20.2 ms — that figure
describes a loopback-plus-LAN path, not a product.

## Clock handling, and a finding

The two hosts' clocks were **not** equal and **did not stay** at a constant
offset. Measured with NTP's four-timestamp formula over raw TCP on the LAN,
selecting the minimum-delay sample:

| | offset (receiver − sender) | min delay | uncertainty | samples |
|---|---|---|---|---|
| Before trials | −6.441 ms | 4.537 ms | ±2.268 ms | 145 |
| After trials | −14.765 ms | 4.577 ms | ±2.288 ms | 200 |

**The offset moved 8.323 ms across a ~21 minute run** — roughly 8 ppm of
relative drift, and comparable to the entire small-file signal. Pinning a
single offset would have biased every trial by up to the full drift; an
early-run trial and a late-run trial would have been corrected by amounts
differing by more than a third of the small-file median.

The analyser therefore linearly interpolates the offset to **each trial's own
send time** between the two anchors. Zero trials fell outside the anchor span.
This correction only exists because the offset was measured twice; a single
measurement would have looked perfectly reasonable and been quietly wrong.

Residual uncertainty from the symmetry assumption is ±2.3 ms, which is ~11% of
the small-file median and ~1% of the change-set median.

## Liveness gates

sf-mini's participation was gated on its **own `lastHeartbeatAt` advancing**
across ≥90 s, before the trials and again at result time. Both passed.

| Gate | Window | Samples | Distinct heartbeats observed |
|---|---|---|---|
| Pre-trial | 11:10:48Z → 11:13:16Z (148 s) | 14 | 11:09:54Z → 11:11:00Z → 11:12:00Z → 11:13:00Z |
| Post-trial | 11:29:21Z → 11:31:36Z (135 s) | 13 | 11:29:08Z → 11:30:08Z → 11:31:09Z |

The node was present in every sample of both windows.

**Two weaker signals were explicitly rejected, and the run demonstrates why.**
Within the pre-trial window sf-mini's `status`/`live` flipped
`online`↔`offline` four times while its heartbeat advanced monotonically, and
the post-trial window showed the same flapping. Separately, an MCP
`query_nodes` call at 11:08Z reported sf-mini `status: "offline"`,
`live: false`, `handlersLive: false` while its heartbeat was 38 s old and
advancing. Had either signal been trusted, this run would have been abandoned
against a perfectly healthy host. `status` and `live` are registration fields,
not liveness fields; and absence from a fleet listing is not evidence of
offline, because the listing returns nondeterministic subsets.

## Trials and data integrity

| Batch | n | complete | incomplete | non-202 | status |
|---|---|---|---|---|---|
| `r2` small file | 20 | 20 | 0 | 0 | **headline** |
| `r2` repo-sized | 20 | 20 | 0 | 0 | **headline** |
| `run20260807` small file | 12 | 12 | 0 | 0 | **correctness evidence only** |

The `run20260807` batch was cut short at 12 of 20 by an operator interrupt. It
is preserved in the raw record and is **not** a source of any percentile in
this document; it is retained solely as evidence that the path delivered
correctly (12/12 accepted, 12/12 arrived, 0 lost). All headline statistics come
from the clean `r2` batches.

Across all 52 trials: every write returned HTTP 202, every expected file
arrived, and no change set was partially delivered. No sample came near the
~30 s websocket-off reconcile fallback, confirming every measurement is of the
websocket delivery path rather than the polling safety net.

Each trial wrote a unique path, so no trial could be satisfied by a previous
one or collapsed into another by coalescing. A change set counts as complete
when its **last** file arrives; ordering across files is not assumed.

## Reproducing

```sh
cd docs/evidence/mount-latency-20260807
python3 harness/assertions.py          # 26 named assertions over the raw data
python3 harness/analyse.py raw/clock-offset-pre.jsonl raw/clock-offset-post.jsonl \
        raw/trials-repo.jsonl raw/mount-watch.jsonl repo r2
```

Raw evidence, appended live as each trial completed:

| File | Contents |
|---|---|
| `raw/trials-small.jsonl` | sender records, both small-file batches |
| `raw/trials-repo.jsonl` | sender records, repo-sized batch |
| `raw/mount-watch.jsonl` | receiver arrival timestamps, sf-mini's own clock |
| `raw/control-create.jsonl`, `raw/control-watch.jsonl` | watcher-overhead control |
| `raw/clock-offset-pre.jsonl`, `raw/clock-offset-post.jsonl` | clock offset anchors |
| `raw/heartbeat-gate-pre.jsonl`, `raw/heartbeat-gate-post.jsonl` | liveness gates |

Teardown and isolation verification: [`CLEANUP.md`](CLEANUP.md).

## Follow-ups this run surfaced

1. **Per-file sequential fetch on the receive path.** Leg B scales with file
   count, not bytes — 11 files cost ~212 ms while one file costs ~16 ms.
   Batching or parallelising the receiver's `ReadFile` calls after a
   multi-file event is the obvious lever, and would move the change-set median
   more than any transport change.
2. **`docs/guides/collaboration.md` and the mount help text.** The 2026-07-26
   assessment flagged the help text as underselling propagation speed. With
   these numbers the correction is not simply "it's faster" — it is
   size-dependent, and any replacement wording should say so.
3. **The hosted path is still unmeasured.** Both this run and the 2026-07-26
   run put the server on one of the two participating machines. No evidence
   currently exists for the product topology.
