# Trajectory: Implement backend-neutral Relayfile conformance and E2E suite

> **Status:** ✅ Completed
> **Task:** relayfile-durable-actors-e2e
> **Confidence:** 92%
> **Started:** October 2, 2026 at 11:34 PM
> **Completed:** October 3, 2026 at 01:19 AM

---

## Summary

Implemented the deterministic backend-neutral Relayfile conformance harness, Go reference-oracle CI gate, hosted Cloudflare and controlled Terse target contracts, redacted evidence, compatibility matrix, and review hardening.

**Approach:** Built public HTTP/WebSocket black-box scenarios around capability-gated target descriptors and narrow clock, provider, state, lifecycle, migration, failover, auth, and mount controls; consolidated legacy coverage; iterated through independent and automated exact-head review.

---

## Key Decisions

### Use the OSS Go durable-local server as the executable reference oracle and keep Cloudflare/Terse as public-edge black-box targets behind one control adapter contract
- **Chose:** Use the OSS Go durable-local server as the executable reference oracle and keep Cloudflare/Terse as public-edge black-box targets behind one control adapter contract
- **Rejected:** Assume private Cloudflare internals, Test only hosted Cloudflare, Create backend-specific suites
- **Reasoning:** The public repository contains no Cloudflare Durable Object implementation; backend internals are neither available nor a portable contract. Public HTTP/WebSocket behavior plus explicit fault, clock, lifecycle, migration, and mount hooks keeps the gate backend-neutral.

### Exclude tester-army/e2e from deterministic conformance gates
- **Chose:** Exclude tester-army/e2e from deterministic conformance gates
- **Rejected:** Make tester-army a required gate, Add an optional dashboard smoke now
- **Reasoning:** Pinned tester-army/e2e is model-driven browser/mobile record-replay, has no API contract focus, enables telemetry by default, and is pre-1.0. It does not improve the required HTTP/WebSocket/runtime invariants; an optional dashboard smoke can be reconsidered later with telemetry disabled.

### Validate every public harness response against the repository OpenAPI document and emit redacted JSON/JSONL/JUnit evidence
- **Chose:** Validate every public harness response against the repository OpenAPI document and emit redacted JSON/JSONL/JUnit evidence
- **Rejected:** Rely only on check-contract-surface.sh, Validate a small error subset
- **Reasoning:** Status-only assertions miss shape drift. Runtime validation found an invalid allOf/additionalProperties composition in conflict schemas, which is now represented as explicit response schemas.

### Use an out-of-band state.inspect control operation as the post-clock proof point
- **Chose:** Use an out-of-band state.inspect control operation as the post-clock proof point
- **Rejected:** Poll public GETs, Use provider.calls for every state type
- **Reasoning:** Polling the public API can wake an actor and mask a missing alarm/scheduler. state.inspect is required to read durable backing state without gateway or actor invocation before any public verification.

---

## Chapters

### 1. Work
*Agent: default*

- Use the OSS Go durable-local server as the executable reference oracle and keep Cloudflare/Terse as public-edge black-box targets behind one control adapter contract: Use the OSS Go durable-local server as the executable reference oracle and keep Cloudflare/Terse as public-edge black-box targets behind one control adapter contract
- Exclude tester-army/e2e from deterministic conformance gates: Exclude tester-army/e2e from deterministic conformance gates
- Validate every public harness response against the repository OpenAPI document and emit redacted JSON/JSONL/JUnit evidence: Validate every public harness response against the repository OpenAPI document and emit redacted JSON/JSONL/JUnit evidence
- A local, self-reported and non-gating Go-oracle run exposed one receipt assertion with intentional correlation/written-count differences; the invariant was narrowed to stable opId and revision. Advanced runtime cases are adapter-gated and full profiles convert every missing applicable capability into a failure. Authoritative command output remains in CI rather than this reflection record.
- Use an out-of-band state.inspect control operation as the post-clock proof point: Use an out-of-band state.inspect control operation as the post-clock proof point
- Exact-head review found false-pass topology in pagination, poison HOL, timer wakeups, reconnect, and socket-effect limit cases. The remediation now pages the full feed, proves HOL before retry exhaustion, inspects state out-of-band after clock advance, disconnects before reconnect backlog, holds a live socket through eviction, and subscribes during the 513-event bulk.
- Automated ready-for-review checks found safety and false-pass gaps beyond the independent review: destructive fixed digest writes, missing crash/failover barriers, incomplete migration replay proof, reset evidence loss, permissive target/CLI validation, and artifact robustness. Each valid finding was incorporated locally. A self-reported, non-gating local run passed the expanded 21-case harness tests, Go oracle, typecheck, and contract checks; the authoritative result is the replacement-head CI recorded outside this trajectory.

---

## Artifacts

**Commits:** d3aafdc2, 83be9940, 87a721c0, 95c1b0cf, 439d7961, 12c54e1d, b5172760
**Files changed:** 27
