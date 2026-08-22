# Provider portability contract

Relayfile's portability boundary is a normal local filesystem plus outbound
HTTPS/WebSocket access. The mount binary owns watching, batching, durable
outbox state, synchronization, cursor resume, materialization, and conflict
artifacts. A sandbox adapter owns only lifecycle operations around that binary.

## Adapter responsibilities

A provider integration must be able to:

1. create or select an isolated Linux sandbox;
2. copy or install the unchanged `relayfile-mount` artifact;
3. start it with a local directory, private state directory, workspace ID,
   Relayfile URL, and scoped credential;
4. expose a read-only benchmark probe or otherwise return receiver-local hashes;
5. stop, restart, inspect, and clean up only resources owned by the run.

No provider SDK belongs in Relayfile's replication or conflict-resolution path.
Provisioning scripts may use a provider SDK, but the mounted process must not
branch on provider identity.

## Certification gates

A new provider pair is certified only when the retained harness proves:

- bidirectional atomic-save visibility with sender-monotonic timing;
- exact receiver-local bytes for every sample;
- zero missing, corrupt, negative, or silently discarded samples;
- cursor advance, WebSocket connection, and restart resume;
- identical final public manifests;
- simultaneous same-path convergence with losing bytes retained;
- identical deployed mount-binary hashes across providers;
- no provider name in the product's Go implementation.

E2B and Daytona pass this contract in the current evidence. The architecture is
provider-neutral, but "universal" should mean this repeatable contract can be
implemented anywhere with the five lifecycle capabilities above—not that all
providers or all network conditions have already been measured.
