# Daytona five-agent real-time collaboration methodology

Date: 2026-08-21  
Status: acceptance contract frozen before Daytona provisioning

## Objective

Measure whether five independent agent sandboxes can edit one Relayfile-backed
workspace simultaneously and make every save hash-correct on all four peer
sandboxes without polling the Relayfile API from the benchmark controller.

## Isolated topology

The fleet contains six newly created Daytona sandboxes in the `us` region: one
Relayfile server and five agent mounts (`a` through `e`). Each sandbox has 2
CPU, 2 GiB memory, and 5 GiB disk. The agents share no local filesystem or
process. Relayfile HTTP/WebSocket traffic and benchmark visibility probes cross
separate signed Daytona HTTPS preview endpoints.

Only the six IDs recorded by this run may be deleted during cleanup. Existing
Daytona resources are out of scope.

## Real repository populations

The shared workspace grows through three pinned, tracked-only Git archives.
Dirty or untracked working-tree content is excluded.

| Stage | Repository | Pinned commit | Tracked blobs before extraction |
| --- | --- | --- | ---: |
| control | `relayfile` | `f89d152502d3bc15161e0673b96cb84f419cd30a` | 1,272 files / 11,321,811 bytes after extraction |
| medium | `../relay` | `9cb8a5e0972f7013d035838c763fce4a50a92dd9` | 1,836 blobs / 21,022,834 bytes |
| large | `../cloud` | `04392ad080d44573f3a4c32c1e02eb5f2a221a0e` | 4,097 blobs / 110,369,079 bytes |

Relayfile is extracted at the workspace root for comparison with the existing
two-agent benchmark. Relay and Cloud are added under `/scale/relay` and
`/scale/cloud`. Exact regular-file counts, byte counts, and manifests are
recorded after extraction and after all five mounts converge.

## Timed workload

One central HTTP barrier releases all five writers only after all roles arrive.
Each writer then atomically saves a unique path while the other four agents are
active. A sender polls a hash probe on every peer over four persistent HTTPS
connections. Each peer probe reads only its local materialized filesystem.

Latency is measured on the sender clock from completion of the local atomic
save to completion of the first HTTP response proving every expected byte is
hash-correct on that peer. No timestamps from different machines are
subtracted. The metric therefore includes probe request/response time and is a
conservative upper bound on filesystem visibility.

Two save shapes are measured:

- small: one deterministic 300-byte file;
- repository-shaped: 11 deterministic source files totaling 13,992 bytes.

Control and medium stages each run 20 small rounds and 5 repository-shaped
rounds. The large stage first performs one excluded warm-up over the fixed
acceptance path set, then performs three consecutive qualifying runs. Each
qualifying run contains 50 small rounds and 10 repository-shaped rounds. The
same fixed paths are overwritten across qualifying runs so tree growth cannot
change the later population.

Each qualifying large run therefore contains 300 simultaneous-agent saves,
1,200 sender-to-peer deliveries, and 3,200 peer content-hash checks.

## Frozen performance gates

Every qualifying large run must independently satisfy:

- small pairwise p95 <= 1,000 ms and p99 <= 2,000 ms;
- small all-four-peer p95 <= 1,500 ms and max <= 5,000 ms;
- repository-shaped pairwise p95 <= 3,000 ms;
- repository-shaped all-four-peer p95 <= 4,000 ms and max <= 8,000 ms.

No missing, corrupt, negative, duplicate, retried, or dropped timed sample is
allowed. A failed or instrumentation-invalid run resets the consecutive-pass
count.

## Correctness gates

- Every timed save must become hash-correct on all four peers.
- All five mounts must remain alive and WebSocket-connected with advancing
  durable cursors.
- After a quiet period, all five public trees excluding `.relay` must have the
  same file count, byte count, and manifest SHA-256.
- A five-way same-path edit must converge to one canonical value on all five
  agents and preserve all four losing byte sequences under `.relay/conflicts`
  across the fleet; silent loss fails acceptance.
- Runtime tokens, signed preview URLs, and private keys are excluded from the
  retained evidence.

The result is a measured five-consumer materialized-filesystem claim. It is not
a public listener-capacity ceiling, an Excel semantic-merge claim, a FUSE
benchmark, or a public-Internet SLA.
