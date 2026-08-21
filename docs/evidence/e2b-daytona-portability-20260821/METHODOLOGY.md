# E2B ↔ Daytona Relayfile portability methodology

Date: 2026-08-21
Status: baseline, release-candidate, and controlled-core gates frozen before their timed samples

## Objective

Prove that two isolated agent sandboxes from different providers can edit local
filesystem replicas of one Relayfile workspace in both directions, with every
measured save becoming hash-correct on the peer. Separately profile the local
watcher, Relayfile service, network transport, remote materialization, and
observation costs before changing the synchronization implementation.

## Topology

- one ephemeral Relayfile service in a dedicated E2B sandbox;
- one E2B agent sandbox with a local materialized workspace;
- one Daytona agent sandbox with a local materialized workspace;
- HTTPS/WSS only between providers; no shared volume or FUSE mount;
- one read-only hash probe per agent, reading that agent's local filesystem.

All resources are recorded by ID in `PROVENANCE.md`. Two E2B sandboxes and the
dedicated core sandbox were created for this benchmark. The cross-provider
Daytona agent was an existing benchmark-owned sandbox; unrelated resources
were not modified.

## Measurement

Each trial atomically replaces one or eleven files on the sender's local disk.
The sender then polls the receiver's hash probe over one persistent HTTPS
connection. Latency starts after the sender's final `fsync` and `rename`, and
ends when a response proves every expected byte is present on the receiver's
local disk. The measurement uses only the sender's monotonic clock, so it does
not subtract timestamps from different machines.

The metric deliberately includes provider proxying and the receiver-probe
round trip. It is therefore a conservative upper bound on file visibility, not
a pure Relayfile transport latency.

Two save shapes are used:

- small: one deterministic 300-byte file;
- repository-shaped: eleven deterministic source files totaling about 14 KB.

After one excluded warm-up per direction, each qualifying run contains 50
small saves and 10 repository-shaped saves in each direction. Three
consecutive qualifying runs are required.

The release-candidate cross-provider sequence repeats the identical workload
and gates with the final mount binary. A replacement warm-up passed after each
binary deployment, then `final-r1` through `final-r3` ran consecutively. No
qualifying sample was removed or retried. Earlier candidate sequences are
retained under `raw/invalid-*` because later Linux watcher tests caused those
binaries to be superseded.

## Frozen baseline gates

Every qualifying run must independently satisfy:

- 60/60 E2B-to-Daytona saves become hash-correct;
- 60/60 Daytona-to-E2B saves become hash-correct;
- small-save p95 <= 1,500 ms in each direction;
- repository-shaped p95 <= 4,000 ms in each direction;
- no missing, corrupt, negative, discarded, or retried timed sample;
- both mounts remain WebSocket-connected with advancing durable cursors;
- the final public trees have identical file count, byte count, and SHA-256
  manifest;
- a same-path cross-provider collision retains the losing bytes under
  `.relay/conflicts` rather than silently discarding them;
- a stopped and restarted mount resumes from its durable cursor and converges.

## Controlled 3–9 ms core target

The 3–9 ms target is qualified only on a controlled same-host core path and is
reported separately from public cross-provider convergence. Two independent
mount processes use local HTTP/WebSocket connections to an intentionally
volatile Relayfile service with no persistence backend; their workspace and
private-state roots are on `tmpfs`. This removes durable-server commit cost,
state-backend cloning, provider gateways,
geographic RTT, and slow overlay storage so the run measures the protocol,
watcher, writeback, fanout, and peer-materialization floor. It is not evidence
that the current durable deployment or two arbitrary sandboxes converge in
3–9 ms.

The target metric begins after the sender's completed `fsync` plus atomic
rename and ends when a same-host read proves the receiver materialized the
expected SHA-256 bytes. It therefore includes watcher settling and batching,
but excludes a network hash-probe round trip.

Each run's shared parent directory is created before the timed sequence and a
250 ms preparation window lets the recursive watcher attach. The target is the
steady-state replication floor for writes inside an established workspace;
first discovery of a brand-new directory remains on the conservative stable-
file path and is not represented by the 3–9 ms figure.

The dedicated Daytona core sandbox had 4 vCPUs and 4 GiB RAM. The certification
mounts used an explicit 1 ns atomic-settle timer and 1 ns batch window, which
means "schedule immediately" at Go/Linux timer granularity. Production defaults
remain 1 ms and 5 ms, respectively. These tuned values expose the core floor;
they are not the cross-provider configuration.

After one excluded warm-up, three consecutive runs of 100 distinct 300-byte
atomic saves must each achieve p95 <= 9 ms with 100/100 hash-correct
materializations. The pooled distribution is supporting evidence; each run
must pass independently.

## Portability definition

"Portable" means the synchronization protocol and mount binary contain no
E2B- or Daytona-specific behavior. Provider adapters may provision processes,
transfer the portable binary, expose ports, and clean up owned resources. A
provider is certified only after the same correctness harness passes there.
This evidence certifies the exact E2B and Daytona environments in
`PROVENANCE.md`; it does not use two providers to claim every provider has been
tested.
