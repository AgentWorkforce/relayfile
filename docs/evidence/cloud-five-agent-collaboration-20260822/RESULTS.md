# Relayfile Cloud five-agent results

Measured 2026-08-22. Latency is sender atomic-save completion to hash-correct
peer filesystem visibility; it is not API acknowledgement time.

## Outcome

Relayfile Cloud delivered every accepted save to all four peers on both the
homogeneous Daytona fleet and the mixed E2B/Daytona fleet:

- 85/85 saves observed per run;
- 340/340 pairwise deliveries per run;
- 1,740/1,740 content-hash assertions per run; and
- no negative latency, stale-content, or missing-delivery samples.

| Fleet and run | Save shape | Pairwise p50 | Pairwise p95 | Pairwise p99 | Max | All-peer p95 |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 5 Daytona, final reviewed deployment, `cloud-final-reviewed-qualifier-1` | 300 B | 190.09 ms | 265.80 ms | 572.09 ms | 573.61 ms | 571.82 ms |
| 5 Daytona, final reviewed deployment, `cloud-final-reviewed-qualifier-1` | 11 files / 14 KB | 251.92 ms | 341.44 ms | 442.37 ms | 513.21 ms | 440.31 ms |
| 5 Daytona, native limiter, warm `cloud-native-rate-limit-qualifier-2` | 300 B | 182.78 ms | 353.26 ms | 370.00 ms | 370.39 ms | 369.73 ms |
| 5 Daytona, native limiter, warm `cloud-native-rate-limit-qualifier-2` | 11 files / 14 KB | 254.84 ms | 370.27 ms | 473.23 ms | 533.02 ms | 468.31 ms |
| 5 Daytona, `cloud-final-qualifier-2` | 300 B | 166.89 ms | 230.09 ms | 243.80 ms | 244.78 ms | 243.80 ms |
| 5 Daytona, `cloud-final-qualifier-2` | 11 files / 14 KB | 228.97 ms | 335.13 ms | 418.64 ms | 461.84 ms | 403.03 ms |
| 2 E2B + 3 Daytona, `mixed-e2b-daytona-qualifier-1` | 300 B | 185.17 ms | 243.16 ms | 256.42 ms | 286.53 ms | 244.14 ms |
| 2 E2B + 3 Daytona, `mixed-e2b-daytona-qualifier-1` | 11 files / 14 KB | 254.21 ms | 323.51 ms | 345.58 ms | 356.45 ms | 344.97 ms |

The mixed-provider penalty was 13.1 ms at small-file p95. Its repository-shaped
p95 was 11.6 ms faster than the homogeneous run. In this sample, portability
did not create a material latency cliff.

The native-limiter build was rerun twice after deployment: 170/170 saves,
680/680 pairwise deliveries, and 3,480/3,480 hash assertions passed. The first
run's cold round followed the Worker/WebSocket restart and ranged from 466.6 to
650.8 ms; its remaining small rounds clustered between 155.3 and 245.2 ms,
apart from one 512.0 ms receiver delay. The second, fully warm aggregate is in
the table. This exposes startup and host-scheduling variance instead of
silently discarding it.

The exact reviewed source was then redeployed as API version
`ca314797-4c34-4581-9858-1df6a37dcb76` with archive consumer version
`a299f81b-738b-40c8-832e-8704f831f577`. Its complete 85-save run delivered
340/340 peer observations and verified 1,740/1,740 hashes. Every propagation,
hash, latency, and clock SLO passed; only the independent Daytona process-launch
spread gate failed (145.81 ms p95 against the 15 ms scheduling target).

Local filesystem write p95 was 9.61 ms for 300-byte files and 40.82 ms for the
11-file save on the five-Daytona run. On the mixed run it was 4.91 ms and
14.46 ms respectively. Those local numbers explain where a 3–9 ms observation
can occur, but they are not cross-sandbox convergence latency.

## Conflict correctness

`cloud-direct-hot-read-conflict-1` was a fully qualified five-Daytona
same-path collision:

- five scheduled writers completed within a 2.84 ms window;
- one canonical value converged on all five mounts;
- the four distinct losing values were preserved under `.relay/conflicts`;
- every contender was accounted for exactly;
- public manifests were identical;
- all five listeners remained live; and
- no atomic-save temporary path leaked into public state.

The mixed-provider collision `mixed-e2b-daytona-conflict-1` passed all of those
correctness assertions. Its cross-provider process release spread was 36.51 ms,
so it does not pass the harness's stricter `<=15 ms` simultaneity label and is
not represented as a fully qualified scheduled-collision sample.

The post-review native-limiter collision
`cloud-native-rate-limit-conflict-1` also converged to one canonical value,
preserved all four losers, retained identical manifests, kept all listeners
live, and leaked no temporary paths. Its sandbox processes completed over a
54.97 ms window, so only the harness's `<=15 ms` launch-scheduling gate failed.

## Gate interpretation

The homogeneous fanout run passed every propagation, hash, and clock gate. Its
aggregate file-write completion spread p95 was 53.81 ms, above the harness's
15 ms synchronized-launch target, so the stored `qualified` field is false.
The mixed fanout run similarly missed launch-spread and had 17.18 ms maximum
clock uncertainty against a 15 ms target. These are orchestration-quality
failures, not missing or slow propagation samples; both runs passed every
actual synchronization SLO.

The final reviewed run has the same interpretation: all synchronization gates
passed, while its host-process completion spread p95 was 145.81 ms. Release
spread is reported separately because it measures five CLI/process launches,
not how long Relayfile took to converge after each save completed.

Keeping those failures visible is deliberate. Marketing may claim the exact
latency distributions and correctness counts above, but may not relabel the
runs or claim universal 3–9 ms convergence.

## Cloud path changes required

Achieving these results required architectural changes in Relayfile Cloud, not
just a faster polling interval:

1. Commit small content and revision metadata atomically into workspace SQLite,
   fan it out inline over WebSockets, then archive to R2 asynchronously.
2. Make exports and direct reads hot-tier aware during the SQLite-to-R2 handoff.
3. Return per-file revisions from bulk writes so mounts can advance state
   without a follow-up read.
4. Page cursor replay globally and close live/replay gaps without duplicating
   events.
5. Move mount write idempotency into authoritative workspace SQLite instead of
   a remote KV round trip.
6. Bound/parallelize R2 and KV work, debounce derived digest regeneration, and
   keep alarms off the foreground collaboration path.
7. Preserve conditional-write conflicts as explicit losing artifacts while
   refreshing every peer to the canonical revision.
8. Replace per-request KV rate counters with Cloudflare's location-local Rate
   Limiting bindings while retaining the legacy KV limiter as a compatibility
   fallback.
9. Lease asynchronous archive handoffs so a message that exhausts queue retries
   is safely re-enqueued, and retain R2 objects referenced by historical events
   after rapid overwrites.

The final benchmark deployment used workspace-local write admission plus the
native rate-limit bindings. Wrangler showed both bindings on the deployed
Worker, and no 429 response occurred in either 85-save post-deployment run.
