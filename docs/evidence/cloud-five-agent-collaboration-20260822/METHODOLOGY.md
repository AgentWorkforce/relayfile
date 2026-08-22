# Relayfile Cloud five-agent methodology

## Topology

The API was a deployed Cloudflare Worker backed by the real Relayfile Cloud
Durable Object, D1, R2, Queue, and WebSocket path. Each consumer was an
independent Linux sandbox running the same statically linked
`relayfile-mount` binary. Sandboxes shared no local disk.

The homogeneous fleet mounted `/benchmark/corpus`, seeded from pinned Relayfile,
Relay, and Cloud repository snapshots. The baseline was 7,195 regular files
and 142,713,477 bytes before generated benchmark files. The mixed-provider
fleet mounted the fresh `/benchmark/mixed` prefix to prove that E2B needed no
provider-specific transport or mount implementation.

## Fanout workload

Every agent was the writer for ten 300-byte atomic saves and seven
repository-shaped saves. A repository-shaped save atomically replaced eleven
files totaling approximately 14 KB. Each round used a unique path so the
fanout benchmark measured propagation rather than conflict resolution.

For every save, the harness retained:

- writer start/completion timestamps and local write duration;
- pre/post clock calibration and uncertainty;
- the expected SHA-256 for every file;
- first hash-correct visibility time on each of the four peers; and
- every intermediate hash observation used to reject stale visibility.

This produces 85 saves, 340 pairwise deliveries, and 1,740 peer hash assertions
per run.

## Clock method

Each sandbox calibrated against the same edge clock Worker before and after its
write. Calibration used Cristian midpoint estimates and retained the seven
lowest-RTT samples of fifteen. Pairwise latency subtracts the calibrated sender
completion epoch from the calibrated peer visibility epoch. Runs record maximum
uncertainty and pre/post offset drift rather than assuming synchronized host
clocks.

## Conflict workload

All five processes scheduled an atomic replacement of the same path with a
role-distinct payload. After settling, a read-only inspector on every sandbox
verified the canonical bytes, all role-distinct conflict artifacts, public-tree
manifest, mount listener health, and absence of temporary save paths.

The strict scheduled-collision gate requires a write-completion spread no
greater than 15 ms. Conflict correctness results remain separately inspectable
when provider process scheduling misses that gate.

## Acceptance gates

- every save and every pairwise delivery present;
- every peer file hash correct;
- no negative calibrated latency;
- small-file pairwise p95 <=500 ms and p99 <=750 ms;
- small-file all-peer p95 <=750 ms;
- repository-shaped pairwise p95 <=1,000 ms;
- repository-shaped all-peer p95 <=1,500 ms;
- maximum clock uncertainty <=15 ms and offset drift <=3 ms; and
- scheduled writer completion spread p95 <=15 ms.

The last three orchestration/clock fields are retained independently so a run
cannot silently turn weak measurement quality into a product-performance claim.

## Deployment warm-up treatment

`cloud-native-rate-limit-qualifier-1` began immediately after a Worker deploy,
which deliberately restarted the five WebSocket sessions. Its first round is
retained as cold-path evidence. `cloud-native-rate-limit-qualifier-2` began only
after all five listeners had reconnected and is the reported steady-state
native-limiter run. No round was removed from either aggregate.
