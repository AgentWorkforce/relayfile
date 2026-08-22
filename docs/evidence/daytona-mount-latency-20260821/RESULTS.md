# Daytona two-agent Relayfile benchmark

Date: 2026-08-21

## Verdict

Relayfile successfully let two independent Daytona sandboxes edit a shared,
materialized repository, converged every measured payload byte-for-byte, and
preserved a simultaneous same-path loser as an explicit conflict artifact.
That is a credible collaboration baseline.

It is not yet consistently real time in this topology. The normal single-file
path was about 0.6 seconds, but recovery tails reached 17.03 and 127.07 seconds;
an 11-file save normally took roughly 9–10 seconds and reached 68.47 seconds.
Prospect-facing claims should say "sub-second in the normal single-file path,
with eventual cursor recovery" rather than promise continuously real-time
coauthoring.

## Save-to-visible latency

The boundary is completion of an atomic save inside the sender's mounted
directory to complete content readable in the receiver's mounted directory.
Values are milliseconds. Percentiles use linear interpolation over all samples;
no tail samples were discarded.

| Save shape | Direction | n | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|---:|
| 300-byte file | A to B | 100 | 589.81 | 772.19 | 831.36 | 861.48 |
| 300-byte file | B to A | 100 | 631.69 | 1,080.72 | 18,131.09 | 127,070.39 |
| 300-byte file | pooled | 200 | 605.60 | 929.52 | 1,353.78 | 127,070.39 |
| 11 files / 13,992 bytes | A to B | 30 | 9,142.88 | 10,170.78 | 10,399.84 | 10,461.25 |
| 11 files / 13,992 bytes | B to A | 30 | 9,644.15 | 10,569.75 | 51,740.78 | 68,468.89 |
| 11 files / 13,992 bytes | pooled | 60 | 9,449.91 | 10,313.53 | 34,435.84 | 68,468.89 |

Of 200 single-file saves, 194 (97%) were visible within one second, 198 (99%)
within two seconds, one took 17.03 seconds, and one took 127.07 seconds. Of 60
multi-file saves, 46 (76.7%) completed within ten seconds, 59 within 30 seconds,
and one took 68.47 seconds. The exact per-trial values are in the directional
summary JSON files and `combined-summary.json`.

## Concurrency and correctness

The independent-path writes began 7.12 ms apart. Both files ultimately appeared
with the correct bytes on both mounts and created no conflict artifact, but the
cross-mount probe upper bounds were 46.65 seconds B to A and 91.86 seconds A to
B after the late-run event backlog had accumulated.

The same-path writes began 0.00386 ms apart from a common base. Agent A's content
won and became canonical on both mounts. Agent B received an HTTP 409 from the
merge attempt, retained its exact losing content at
`.relay/conflicts/testdata/daytona-sync-benchmark/concurrency-r1/conflict/shared.txt.rev_2135.local`,
and reported `status: conflict` with one pending conflict. There was no silent
overwrite.

## Integrity and measurement confidence

- 260/260 saves completed, representing 860/860 expected receiver payloads.
- All 860 receiver sizes and SHA-256 content hashes match deterministic expected
  content; there were no missing, duplicate, ambiguous, clock-negative, or
  extrapolated trials.
- Clock offsets were estimated before and after each direction over warmed HTTPS
  using the median of every anchor's samples. The observed proxy routes were
  bimodal, so the committed uncertainty expands to cover the full offset range:
  30.60–55.45 ms across the four anchors. HTTPS path asymmetry remains
  unbounded, so the printed fractional milliseconds are reproducible estimates,
  not claims of sub-millisecond physical accuracy.
- Receiver scan overhead was included. The watcher scan median/p95 was
  6.12/54.49 ms on Agent B and 8.05/57.40 ms on Agent A.

## Operational observations

- Initial materialization of the 1,272-file, 11,321,811-byte repository
  completed in about 26 seconds on Agent B and 27 seconds on Agent A, based on
  second-resolution mount logs.
- Mount logs show cursor catch-up in 50-event pages followed by individual file
  GETs. Eleven-file saves therefore materialized serially at roughly one file
  per second. During recovery, hundreds of queued events took 68–127 seconds to
  drain. This evidence points to cursor freshness, event coalescing, and bounded
  parallel/bulk receiver fetches as the highest-value performance work.
- At the final sample, Agent A/B mount RSS was 22.0/29.3 MiB with high-water
  marks of 59.0/58.9 MiB. The server was 147.7 MiB RSS with a 323.1 MiB
  high-water mark. Each sandbox was limited to two CPUs and 2 GiB RAM.
- The server held 2,135 files, 4,272 filesystem events, and 2,136 operations at
  the end of the run. This benchmark used two consumers; it does not establish
  maximum listener capacity.

## Reproduction and evidence

`METHODOLOGY.md` defines the precommitted protocol. `topology.json` pins the
actual sandbox and binary provenance. `validation.json` is the fail-closed
integrity result. The `raw/` directory contains append-only sends, receiver
arrivals, 100-sample clock anchors, concurrency outcomes, environment metadata,
and compressed complete mount logs. The `harness/` directory contains the
writer, watcher, clock, analysis, combination, and validation scripts.

This was the released v0.10.45 mount in materialized `poll` mode against a
separate Relayfile server at commit
`f89d152502d3bc15161e0673b96cb84f419cd30a`, all through Daytona preview
HTTPS/WebSocket routing in the `us` region. It is a two-consumer benchmark, not
a public Internet SLA, FUSE benchmark, Excel merge benchmark, or long-duration
soak test.
