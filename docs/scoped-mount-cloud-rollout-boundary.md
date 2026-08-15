# Scoped Mount Cloud Rollout Boundary

The 2026-08-15 Cloud rollback boundary remains the paired Relayfile v0.10.35
images:

- full: `relay-orchestrator-sdk-11.4.1-relayfile-v0.10.35-runtime-4.1.41`
- lite: `relay-sandbox-lite-sdk-11.4.1-relayfile-v0.10.35-runtime-4.1.41`

Keep both production pins on that pair until the scoped-mount candidate passes
all of the following gates:

1. built `relayfile` and `relayfile-mount` process tests for root, single-path,
   and multi-path mounts, including restart and cancellation;
2. scoped operator status/readiness and writeback receipt/failure aggregation;
3. the Cloud mount-script contract for a root mount and a multi-provider
   persona;
4. active full and lite Daytona image smoke tests; and
5. `e2e-per-agent-sandbox` plus `e2e-cloud-sync`, with successful initial sync
   and a non-empty sync patch.

Promotion is atomic at the operational boundary: update the full and lite pins
together only after every gate is green. If either image or either post-promotion
probe fails, restore both v0.10.35 pins above rather than leaving Cloud on a
mixed full/lite pair. Relayfile v0.10.43 remains a valid exact-root release and
is not the scoped-layout promotion target.
