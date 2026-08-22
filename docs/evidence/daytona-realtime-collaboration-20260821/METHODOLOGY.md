# Daytona real-time collaboration acceptance methodology

Date: 2026-08-21
Status: complete; qualifying sequence clean-r3, clean-r4, clean-r5

## Objective

Prove that the repaired Relayfile materialized mount is continuously real time
for two independent agents sharing one workspace through separate Daytona
sandboxes, without weakening same-path conflict preservation.

## Isolated topology

Each attempt uses three newly-created 2 CPU / 2 GiB Daytona sandboxes in the
`us` region: one Relayfile server and two agent mounts. Both agent directions
cross the Daytona HTTPS/WebSocket preview proxy; neither agent shares a local
filesystem or process with the server or the other agent. The workspace is
seeded with the same tracked repository snapshot used by the 2026-08-21
baseline: 1,272 regular files, 11,321,811 bytes, excluding `.git` and its one
tracked symlink.

The server and both mounts are built from the same local working tree. The
evidence records the base Git commit, the complete source diff hash, binary
SHA-256 hashes, sandbox IDs, runtime versions, and process environment. The
server runs with `RELAYFILE_EXTERNAL_WRITEBACK=0` so provider receipt handling
does not depend on an absent external consumer.

## Timed acceptance populations

An exploratory smoke run is diagnostic only and is excluded from acceptance.
Three consecutive clean acceptance runs must then pass. Every acceptance run
contains, independently in each direction:

- 100 atomic saves of one unique 300-byte file.
- 30 atomic saves of 11 unique files totaling 13,992 bytes.

Before each clean acceptance run, the disposable server state and both local
materializations are reset and reseeded to the exact 1,272-file / 11,321,811-byte
baseline. Reset/bootstrap time is excluded. The same three sandbox identities,
resource limits, binaries, network path, and tokens remain in place. This keeps
the three populations comparable and prevents earlier benchmark artifacts from
changing the tree size under test.

The sender waits for an out-of-band receiver-presence check before beginning
the next trial, then leaves a short quiet interval. Every trial uses a new path
under `/testdata/daytona-sync-benchmark/<run-id>/`; an old arrival cannot
satisfy a new sample. Receiver observation polls its materialized filesystem at
a 1 ms target interval and records content size and SHA-256.

The latency boundary is completion of the atomic save on the sender to the
first complete, hash-correct read on the receiver. Sender and receiver
`CLOCK_REALTIME` values are corrected with the same warmed persistent HTTPS
four-timestamp anchors used by the baseline. Each anchor uses the median of all
offset samples and reports the full observed offset range; uncertainty expands
to cover both range extremes when the proxy path is bimodal. Raw clock
uncertainty and watcher scan overhead remain part of the evidence.

## Required performance gates

Every one of the three clean runs must independently satisfy:

- 300-byte saves, each direction: p95 <= 500 ms, p99 <= 1,000 ms, max <= 2,000 ms.
- 11-file saves, each direction: p95 <= 2,000 ms, max <= 5,000 ms.
- Simultaneous disjoint edits converge to both mounts within 2,000 ms.
- A simultaneous same-path edit chooses one canonical winner and preserves the
  exact losing bytes under `.relay/conflicts`; silent overwrite is forbidden.

## Integrity and failure rules

- Every expected path must have exactly one first-arrival observation.
- Every observed byte count and SHA-256 must match deterministic expected data.
- Missing, duplicate, ambiguous, negative-after-correction, extrapolated, or
  corrupt samples fail the run; no tail sample may be silently rerun or dropped.
- Both mounts must remain alive and WebSocket-connected, their durable event
  cursors must advance, and final mirrors must converge byte-for-byte.
- A failed measured run resets the consecutive-pass count. Diagnostic reruns
  are named and retained separately from acceptance populations.
- A benchmark instrumentation fault invalidates the run and also resets the
  consecutive-pass count; a corrected retry cannot make that run clean.
- Sandboxes are deleted after evidence collection, and deletion is verified.

The result applies to this exact two-consumer Daytona topology. It is not an
Excel semantic-merge claim, a FUSE benchmark, a listener-capacity limit, or a
public-Internet SLA.
