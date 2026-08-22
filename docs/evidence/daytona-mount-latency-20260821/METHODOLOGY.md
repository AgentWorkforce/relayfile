# Daytona two-agent mount latency — methodology

Date: 2026-08-21
Status: protocol written before timed trials; factual environment notes appended

## Question

How long does Relayfile v0.10.45 take to propagate a local filesystem save
from one Daytona agent sandbox into a second Daytona agent sandbox that mounts
the same workspace?

## Topology

Three newly created Daytona sandboxes run in the `us` region:

- `server`: Relayfile at repository commit
  `f89d152502d3bc15161e0673b96cb84f419cd30a`
- `agent-a`: released `relayfile-mount` v0.10.45, build commit
  `b3a06ffb3eea68fe27f7cedf0f90924efe61fa13`
- `agent-b`: the same released mount binary

The server is deliberately separate from both agents. Both directions traverse
the Daytona HTTPS/WebSocket preview proxy. The full tracked repository is
seeded into a fresh workspace before the mounts start (1,272 files and
11,321,811 bytes; `.git` is not included).

Preflight initially left `RELAYFILE_EXTERNAL_WRITEBACK=1` without an external
consumer. That made all 1,272 seed operations remain pending and caused mounts
to poll the entire pending operation set during the trial. Those exploratory
runs were rejected and are excluded from the checked-in latency populations.
The measured workspace runs with
`RELAYFILE_EXTERNAL_WRITEBACK=0`, whose built-in no-op provider writer advances
operations to `succeeded`; this models an active external consumer without
adding an unrelated provider service to the sync path.

The local Daytona CLI is v0.183.0 while the control-plane API reports v0.207.0.
This mismatch is recorded, but the CLI is not on the measured data path after
the sandboxes and preview URLs are created.

## Measurement boundary

The primary latency is:

> completion of an atomic local save on the sender mount to complete content
> becoming readable in the receiver mount

This includes source filesystem notification/debounce, source upload, server
commit and WebSocket fan-out, receiver file fetch, and receiver atomic write.
It excludes the agent's time to decide what to write. The harness also records
the start of the local save so the local write cost can be reported separately.

Every trial writes unique paths under
`/testdata/daytona-sync-benchmark/<run-id>/`; no arrival can be satisfied by a
previous trial. Multi-file completion is the time its last file is observed.

## Populations

Each direction is measured independently:

- 100 single-file saves of 300 bytes; report min, p50, p95, p99, and max.
- 30 eleven-file saves totaling approximately 14 KB; report min, p50, p95,
  p99, and max, while noting that n=30 is not a stable tail estimate.

For the isolated-latency populations, the sender waits for an out-of-band HTTP
presence check against the receiver mirror before starting the next trial, then
applies the configured quiet spacing. This prevents queue buildup from being
misreported as single-edit latency. Raw records are append-only JSONL and
flushed after every observation. One reverse-direction trial crossed the
original 30-second harness wait; it was retained, allowed to recover without
mount intervention, and followed by the remaining unique trials. It was not
rerun away.

After latency trials, separate correctness probes cover simultaneous disjoint
edits and a simultaneous same-path edit. Correctness outcomes are not mixed
into the latency populations.

## Clocks and observation

Each receiving sandbox runs an in-box watcher using `CLOCK_REALTIME` and a
1 ms target polling interval over its local materialized mirror. Watcher scan
cost and effective loop period are recorded.

Sender and receiver clocks are not assumed equal. Before and after each trial
block, an NTP-style four-timestamp exchange runs over a warmed persistent HTTPS
connection to an in-box receiver clock endpoint. Each anchor uses the median
of all observed offsets, not the offset attached to one minimum-delay sample,
and that robust offset is linearly interpolated to each trial. Every raw anchor
reports its full offset range and median absolute deviation. Residual
uncertainty is the largest of half the minimum round-trip delay or the distance
from the median to either observed offset extreme, so bimodal routing regimes
are visible and bounded rather than collapsed. HTTPS path asymmetry and
nonlinear clock changes between anchors remain unbounded and are disclosed.

Preflight rejected two alternatives before any timed edit trial: new HTTPS
connections had a minimum 54.8 ms round trip (at least +/-27.4 ms uncertainty),
and outbound UDP/SNTP is blocked in these sandboxes. The retained clock probe
therefore warms and reuses one HTTPS connection; its measured uncertainty is
reported with the results.

## Failure rules

- Every expected path must have exactly one arrival observation.
- Missing, duplicate, negative-after-correction, or nonconvergent trials are
  named and excluded rather than silently discarded.
- A sample at polling-fallback scale is retained and reported.
- No latency claim is made if either mount dies, WebSocket delivery is absent,
  or the two mirrors do not converge byte-for-byte after the run.
- Results describe this exact Daytona topology and are not generalized into a
  public Internet SLA.
- The benchmark exercises two consumers in materialized `poll` mode. It does
  not establish listener-capacity limits, FUSE-mode performance, or an Excel
  semantic merge contract.
