# Hosted mount one-way latency results — 2026-08-07

## Result

On the measured hosted deployment and topology, a single 300-byte file took a
median **1,475.6 ms** to become visible at the receiver (p95 **1,929.9 ms**,
n=20). A realistic 11-file / 13,992-byte change set took a median **12,446.7
ms** (p95 **20,501.8 ms**, n=22).

These are direct one-way measurements. Nothing was halved.

| Shape | Complete / sent | Median | p95 | Min | Max |
|---|---:|---:|---:|---:|---:|
| One 300-byte file | 20 / 20 | **1,475.6 ms** | **1,929.9 ms** | 1,357.2 ms | 2,859.6 ms |
| 11 files / 13,992 bytes | 22 / 22 | **12,446.7 ms** | **20,501.8 ms** | 11,506.5 ms | 25,229.7 ms |

The n=20 p95 is linearly interpolated from the two largest small-file
observations. The n=22 repo p95 also rests on only the upper few observations;
neither is a high-confidence tail estimate.

## Leg decomposition

Leg A is sender → hosted service acknowledgement. Leg B is hosted
acknowledgement → receiver's last exact final-file observation.

| Shape | Leg A median | Leg A p95 | Leg B median | Leg B p95 |
|---|---:|---:|---:|---:|
| One 300-byte file | 1,037.9 ms | 1,417.5 ms | 422.1 ms | 536.3 ms |
| 11-file change set | 6,383.7 ms | 6,976.1 ms | 6,194.2 ms | 14,726.6 ms |

Medians of the two legs do not algebraically sum to the median total because
each is a separate marginal distribution. Per-trial legs and totals are in the
committed analysis JSON.

The hosted sender request is already over one second at the small-file median.
For the 11-file bulk request, both hosted acknowledgement time and subsequent
delivery/apply time are material. This differs sharply from the LAN baseline,
where leg A was a ~3 ms loopback operation and receiver-side per-file fetches
dominated the change-set result.

## All attempts, including failures

The clean headline population was not the only population attempted.

| Attempt | Shape | Sent | HTTP 202 | Complete observations | Incomplete | Non-202 | Use |
|---|---|---:|---:|---:|---:|---:|---|
| warmup 1 | small | 1 | 0 | 0 | 0 | 1 (403/1010) | compatibility finding |
| warmup 2 | small | 1 | 1 | 1 | 0 | 0 | readiness only |
| headline small | small | 20 | 20 | 20 | 0 | 0 | headline |
| r1 | repo | 20 | 20 | 14 | 6 | 0 | incomplete; watcher duration exhausted |
| r2 | repo | 20 | 19 | 12 | 7 | 1 (500) | incomplete |
| r3 | repo | 25 | 25 | 15 | 10 | 0 | incomplete; websocket/reconcile stalled |
| r4 | repo | 22 | 22 | 22 | 0 | 0 | headline after fresh scoped-mount restart |

An incomplete observation means the watcher did not see every expected final
path within its recorded window. It is not silently counted as a loss or a
latency sample. In r2 the server returned one explicit HTTP 500 describing a
Durable Object storage reset. During r3 the receiver logged websocket EOF,
repeated reconcile timeouts, and a ten-minute stall before the mount was
restarted. The raw JSONL retains the send and arrival side of every attempt.

The r1, r2, and r3 conditional medians in `analysis/` are diagnostic only. They
are not headline figures because incomplete populations preferentially omit
late or never-observed trials.

## Clock correction

| Anchor | Selected offset (receiver − sender) | Minimum exchange delay | Symmetry bound | n |
|---|---:|---:|---:|---:|
| Before measured blocks | +1.173 ms | 3.996 ms | ±1.998 ms | 200 |
| After measured blocks | +1.436 ms | 4.458 ms | ±2.229 ms | 200 |

The selected offset changed by 0.263 ms across the run. Each trial uses linear
interpolation between anchors. The dataset does not bound an intervening clock
step or nonlinear slew, so clock-model error between anchors remains
unbounded. The endpoint symmetry bounds are therefore not total error bars.

## Receiver watcher control

All 25 receiver-local control publishes paired with watcher observations.

| Control distribution | Median | p95 | Min | Max |
|---|---:|---:|---:|---:|
| Detection delay upper bound (from publish start) | **1.693 ms** | 3.501 ms | 0.417 ms | 3.621 ms |
| Detection delay lower bound (from publish end) | **1.371 ms** | 3.130 ms | −0.022 ms | 3.219 ms |
| Atomic publish interval | 0.368 ms | 0.426 ms | 0.076 ms | 0.439 ms |

The small negative lower-bound minimum means the watcher observed the file
during the measured publish interval, before the publishing thread recorded
its end timestamp. No overhead is subtracted from the hosted distributions.

The headline small/r1 watcher scan cost was 1.419 ms median and 3.718 ms p95.
The r4 watcher scanned a larger accumulated `/trials` subtree at 3.848 ms
median and 5.183 ms p95. These measured scan costs are small beside both
headline signals but are included in the unadjusted totals.

## Deployment identity and claim boundary

The deployment was `https://api.relayfile.dev`. Its health endpoint returned
HTTP 200, but neither its body nor response headers exposed an application
build or version. The deployed artifact is therefore recorded as **build
undetermined**. The receiver CLI source commit is recorded in `METHODOLOGY.md`
and must not be mistaken for the hosted build.

This evidence licenses the narrow factual statement that, on this deployment,
workspace, date, and topology, the observed complete populations had the
medians, p95s, and n values above. It also records concrete 403, 500,
websocket, reconcile, and incomplete-observation outcomes encountered while
obtaining those populations.

It does **not** license a product-wide SLA, a tail-latency guarantee, a claim
about an identifiable release, or extrapolation to other regions, workspace
sizes, clients, or dates. In particular, the completed-trial percentiles must
not be used to erase the failed and incomplete attempts. The measured hosted
figures are far above 200 ms for both shapes, so this run supplies no support
for a sub-200 ms hosted-product claim.

## Evidence inventory

| Path | Contents |
|---|---|
| `raw/clock-offset-pre.jsonl`, `raw/clock-offset-post.jsonl` | 200 NTP-style samples plus one summary at each anchor |
| `raw/trials-small.jsonl` | headline small sends |
| `raw/trials-repo-r1.jsonl` … `raw/trials-repo-r4.jsonl` | every repo send attempt, including the HTTP 500 |
| `raw/trials-warmup.jsonl` | both warmups, including Cloudflare 403/1010 |
| `raw/mount-watch-*.jsonl` | receiver observations; interrupted files intentionally lack a terminal record |
| `raw/control-create.jsonl`, `raw/control-watch.jsonl` | receiver-local watcher control |
| `analysis/*.json` | regenerated headline and incomplete-attempt summaries |
| `harness/` | exact measurement, analysis, and assertion code used |

From this directory, `python3 harness/assertions.py` regenerates the analyses
and gates the claims against raw evidence; 54/54 named assertions pass.
