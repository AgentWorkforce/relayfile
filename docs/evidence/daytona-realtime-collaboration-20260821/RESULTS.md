# Relayfile real-time collaboration results

Date: 2026-08-21  
Result: **PASS — three strictly consecutive clean runs** (`clean-r3`, `clean-r4`, `clean-r5`)

## Acceptance result

The qualifying sequence verified 780/780 atomic saves and 2,580/2,580
receiver payload hashes across two independent Daytona agent sandboxes. Every
run passed every directional latency gate, simultaneous disjoint convergence,
and same-path loser preservation.

| Run | 300 B A→B p95 / p99 / max | 300 B B→A p95 / p99 / max | 11-file A→B p95 / max | 11-file B→A p95 / max | Disjoint max | Same-path loser |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| clean-r3 | 174.90 / 176.56 / 202.41 ms | 197.10 / 206.60 / 209.48 ms | 720.54 / 756.16 ms | 922.82 / 981.01 ms | 470.22 ms | preserved once |
| clean-r4 | 182.33 / 311.11 / 433.87 ms | 194.30 / 304.60 / 666.09 ms | 654.73 / 1,116.16 ms | 771.30 / 815.60 ms | 544.00 ms | preserved once |
| clean-r5 | 172.71 / 178.63 / 182.08 ms | 172.13 / 179.61 / 252.92 ms | 753.79 / 763.59 ms | 619.51 / 697.25 ms | 405.88 ms | preserved once |

Across all qualifying runs, the pooled 300-byte population (600 samples) was
p50 151.04 ms, p95 193.13 ms, p99 244.37 ms, max 666.09 ms. The pooled
11-file/13,992-byte population (180 samples) was p50 474.08 ms, p95 764.39 ms,
p99 952.12 ms, max 1,116.16 ms.

The final materializations each contained 2,135 non-Relay metadata files and
12,221,406 bytes. Their complete path/size/content manifests were byte-identical
with SHA-256 `bd268043b5a6481e41b38d6d6740cda514651c6fb4c9c19879c3591dfff36a91`.
Both agents reported WebSocket mode `listening`, fresh heartbeat timestamps,
and the same durable cursor, `evt_4272`.

## Improvement from the original baseline

The original two-agent Daytona baseline had pooled 300-byte p95 909.55 ms and
pooled 11-file p95 10,293.45 ms, with a 68,448.84 ms repo-save maximum and a
127,050.41 ms small-save maximum. The qualifying repaired runs reduced pooled
p95 to 193.13 ms (4.7×) and 764.39 ms (13.5×), while reducing the respective
maxima to 666.09 ms and 1,116.16 ms.

The implementation removes the measured causes rather than tuning the harness:

- durable WebSocket cursors, paginated catch-up, explicit overflow close, and
  heartbeat-driven reconnect eliminate silent event gaps;
- hash-verified inline WebSocket payloads remove the receiver GET for files up
  to 1 MiB;
- local watcher bursts become one bulk write and operation receipts settle
  asynchronously with bounded concurrency;
- incremental recovery reads execute concurrently but apply deterministically;
- healthy watcher/WebSocket mounts avoid stop-the-world full-tree scans while
  unhealthy transports immediately fall back to reconciliation;
- cheap state checkpoints retain full tracked-file state without rescanning the
  materialized tree on every event.

## Invalid and failed attempts retained

No failed tail sample was dropped. `raw/r1` failed on a 6,847.96 ms repo-save
outlier caused by the old periodic stop-the-world reconcile. `raw/r2` failed
after accumulated benchmark paths exposed O(tree) state persistence (small p95
604.32/806.12 ms; repo p95 2,780.95/3,791.73 ms).

`clean-r2` produced valid latency data, but its first concurrency watcher was
configured with a truncated expected SHA-256. Although both files converged and
a corrected new-path trial passed, the run is classified **invalid** rather than
clean under the no-rerun rule. It reset the acceptance sequence; the final claim
therefore uses only clean-r3 through clean-r5. The invalid watcher logs remain in
`raw/clean-r2`.

## Verification and scope

- `go test ./...`: pass
- race detector on `internal/mountsync`, `internal/httpapi`,
  `internal/relayfile`, `cmd/relayfile-cli`, and `cmd/relayfile-mount`: pass
- `scripts/check-contract-surface.sh`: SDK parity and contract checks pass
- `git diff --check`: pass
- deterministic rebuilds of both deployed Linux binaries: byte-identical

This proves the stated thresholds for this exact two-consumer, three-sandbox
Daytona topology. It is not an Excel semantic-merge result, a FUSE benchmark,
a listener-capacity ceiling, or a public-Internet SLA. Same-path edits preserve
the loser as a conflict artifact; they do not semantically merge arbitrary file
formats.
