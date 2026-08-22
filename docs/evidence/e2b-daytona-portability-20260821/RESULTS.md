# Relayfile E2B ↔ Daytona portability results

Date: 2026-08-21
Result: **PASS — bidirectional cross-provider convergence and a 2.23 ms p95 controlled core floor**

## Cross-provider result

An E2B agent and a Daytona agent edited independent local materializations of
one Relayfile workspace over HTTPS/WebSocket. Across three consecutive
release-candidate runs in both directions, all 360 saves became visible, all 960 file
hashes matched, and there were zero transport errors.

| Direction | 300 B p50 / p95 / p99 / max | 11-file p50 / p95 / p99 / max |
| --- | ---: | ---: |
| E2B → Daytona | 235.31 / 274.75 / 323.31 / 354.42 ms | 236.23 / 267.56 / 269.67 / 270.00 ms |
| Daytona → E2B | 177.16 / 356.67 / 401.13 / 858.13 ms | 197.62 / 376.11 / 383.49 / 386.08 ms |
| Pooled | 233.38 / 336.55 / 372.31 / 858.13 ms | 235.35 / 365.59 / 380.81 / 386.08 ms |

The figures are calculated from individual samples, not averaged run
percentiles. The receiver was verified by content hash on its own local
filesystem. The conservative visibility metric begins after the sender's
`fsync` and atomic rename and includes the receiver's provider HTTPS hash-probe
round trip.

The worst release-candidate sample took 858.13 ms and remains in the retained
data. The defensible public statement is therefore "337 ms pooled p95 and every
observed save under one second in this run," not a universal sub-second SLA.

The pre-change baseline also passed all 360 saves and 960 hash checks, with
pooled p95 of 371.72 ms for 300-byte saves and 434.01 ms for 11-file saves. The
release candidate reduced those pooled p95 values by 9.5% and 15.8%, respectively.

## Controlled core result

Three independent qualifying runs of 100 saves each used two mount processes,
a volatile in-memory Relayfile service, loopback HTTP/WebSocket, and `tmpfs`
workspaces. Every one of the 300 saves became hash-correct on the peer.

| Run | p50 | p95 | p99 | max |
| --- | ---: | ---: | ---: | ---: |
| `core-qualifying-r1` | 1.126 | 2.202 | 3.259 | 3.272 ms |
| `core-qualifying-r2` | 1.115 | 2.227 | 2.252 | 2.252 ms |
| `core-qualifying-r3` | 2.187 | 2.230 | 3.206 | 3.357 ms |
| Pooled | 2.142 | 2.227 | 3.205 | 3.357 ms |

This beats the 3–9 ms Relayfile core-path target under the controlled topology.
It does not prove that a durable deployment or arbitrary remote sandboxes
converge in 3–9 ms.

## Conflict, restart, and portability

The baseline and candidate binaries each passed a simultaneous E2B/Daytona
same-path collision: both mounts converged on one canonical value and retained
the losing bytes under `.relay/conflicts`. The baseline Daytona mount also
resumed from its durable cursor after termination and received an offline E2B
write in 407.19 ms after restart.

The same Linux/amd64 mount binary, SHA-256
`177420bbfc90ea7b878ef2340c1955e9400dce469d047b7e107742e0ec810126`,
ran unchanged on both providers. Relayfile's Go product code contains no E2B or
Daytona branches. This certifies the tested E2B and Daytona topology; broader
provider support remains a certification claim, not an untested universal
performance claim.

## Public claims supported by this evidence

- "Verified bidirectional file collaboration between E2B and Daytona."
- "360/360 cross-provider saves visible; 960/960 hashes correct."
- "337 ms pooled p95 for 300-byte cross-provider saves in this run."
- "2.227 ms pooled p95 on Relayfile's controlled core path."

Do not turn the last statement into "2.2 ms across E2B and Daytona." This is
not an Excel semantic-merge result, a FUSE benchmark, a listener-capacity
ceiling, a long-duration soak, or a public-Internet SLA.
