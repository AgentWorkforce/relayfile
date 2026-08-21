# Relayfile five-agent real-time collaboration results

Date: 2026-08-21
Result: **PASS — three consecutive qualifying five-agent runs**

## Acceptance result

Five independent Daytona agent sandboxes simultaneously edited one
Relayfile-backed materialized workspace populated from `relayfile`, `../relay`,
and `../cloud`. The pinned baseline contained 7,195 regular files and
142,713,477 bytes. A sixth independent sandbox hosted Relayfile; no agent shared
a local filesystem or process with another agent.

Across three consecutive runs, all 900 atomic saves became hash-correct on all
four peer mounts: 3,600/3,600 pairwise deliveries and 9,600/9,600 content hashes.
Every frozen latency gate passed independently in every run.

| Run | Saves / deliveries / hashes | 300 B pairwise p95 / p99 / max | 300 B all-peer p95 / max | 11-file pairwise p95 / p99 / max | 11-file all-peer p95 / max |
| --- | ---: | ---: | ---: | ---: | ---: |
| `large-r1` | 300 / 1,200 / 3,200 | 603.47 / 701.40 / 1,091.63 ms | 669.62 / 1,091.63 ms | 1,195.48 / 1,325.69 / 1,389.41 ms | 1,315.42 / 1,389.41 ms |
| `large-r2` | 300 / 1,200 / 3,200 | 624.83 / 666.91 / 714.35 ms | 665.72 / 714.35 ms | 1,295.70 / 1,394.12 / 1,410.57 ms | 1,381.56 / 1,410.57 ms |
| `large-r3` | 300 / 1,200 / 3,200 | 667.03 / 711.75 / 1,238.83 ms | 707.99 / 1,238.83 ms | 1,365.23 / 1,505.91 / 1,549.71 ms | 1,504.03 / 1,549.71 ms |

The pooled distributions below are computed from individual retained samples,
not by averaging run percentiles.

| Save shape | Pairwise p50 / p95 / p99 / max | All-four-peer p50 / p95 / p99 / max |
| --- | ---: | ---: |
| 300 B | 504.03 / 635.64 / 699.74 / 1,238.83 ms | 584.92 / 695.73 / 759.07 / 1,238.83 ms |
| 11 files / 13,992 B | 929.02 / 1,313.42 / 1,410.91 / 1,549.71 ms | 1,116.75 / 1,402.13 / 1,513.25 / 1,549.71 ms |

One peer probe hit its one-second Daytona HTTPS transport deadline in
`large-r3`. The sample was not discarded or restarted: observation continued
on a fresh connection, the delivery remained hash-correct, and its full
1,238.83 ms latency is included above. There were zero controller retries in
the qualifying sequence.

## Conflict and durability result

A barrier released five distinct writers against the same path. All five
mounts converged to the same canonical bytes, while the four losing byte
sequences were preserved under `.relay/conflicts`; all five contenders were
accounted for. The final public manifests were identical:

- 7,996 files and 143,488,108 bytes per mount;
- manifest SHA-256
  `0e33a6088df39c4660e58e779cd5c7afa8a83f95941a409e690ed73ad1c2b982`;
- durable cursor `evt_19312` on every mount;
- zero conventional ephemeral atomic-save paths on every mount.

Relayfile was then terminated and restarted from the segmented filesystem
backend. All five listeners reconnected, and a fresh read-only inspection
reproduced the same manifest, cursor, canonical value, and four conflict
artifacts. The committed backend generation remained `1468`, with 9,656
immutable content blobs and counters `rev_9656`, `op_9656`, and `evt_19312`.

## Invalidated attempts retained

No failing tail was removed from a qualifying result. Earlier attempts are
retained separately to show why implementation and instrumentation changed:

- `invalid-control-r1` exposed a roughly 36-second cursor stall when a
  superseded temporary-file event resolved to 404;
- `invalid-medium-fix1` exposed 3,497 ms repository-shaped p95 from rewriting
  the full JSON state on every mutation;
- `invalid-control-fix2` and `invalid-large-r2-probe-deadline` exposed preview
  transport stalls and caused the probe's one-second hard wall deadline;
- `invalid-phantom-*` passed latency gates but was invalidated after full-tree
  inspection found three leaked `*.writer-tmp-*` staging files;
- pre-final diagnostic runs never contribute to the three-run aggregate.

The qualifying sequence started from a new fleet after the last product and
instrumentation correction.

## Scope

This proves five-consumer, hash-correct materialized-filesystem convergence for
the exact six-sandbox Daytona topology and repository population described in
`METHODOLOGY.md`. It is not a listener-capacity ceiling, an Excel semantic-merge
claim, a FUSE benchmark, or a public-Internet SLA. Relayfile preserves arbitrary
same-path losers; format-aware merge remains a higher-level concern.

## Repository validation

- `go test ./...`: pass
- `go test -race ./...`: pass
- new segmented-backend tests under the race detector after the final test-only
  crash-boundary assertion: pass
- `go vet ./...`: pass
- `scripts/check-contract-surface.sh`: SDK parity and contract checks pass
- all retained JSON parses, all Python harness files compile, `git diff --check`
  passes, and the evidence secret scan is empty
- final Linux server and mount rebuilds are byte-identical to the deployed
  binaries
