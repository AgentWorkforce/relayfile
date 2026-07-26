# Trajectory: Real-time two-machine multi-agent collaboration proof with adversarial relayfile mount conditions

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** July 26, 2026 at 08:26 PM
> **Completed:** July 26, 2026 at 09:12 PM

---

## Summary

Proved real-time two-agent collaboration between sf-mac-mini and finn-mac-mini through an isolated relayfile mount: interdependent same-type edits converged, two 20-write storms survived a finn mount restart, and an offline backend write survived a real Tailscale transport partition and replayed with no conflicts or corruption. Captured identical 11-file SHA-256 manifests, prompts, timeline, and logs. The initial 24 cross-host wall-clock latency deltas were superseded during PR review by 24 initiator-monotonic ping/ack round trips (median 315.526 ms initiated from sf and 373.230 ms from finn), eliminating unbounded machine-clock skew. Found a WebSocket dial-error bearer-token log leak; added boundary redaction and regression tests, rejected an incomplete first fix through live replay, then verified the final hash-matched binary with zero raw-token occurrences and three redaction markers during another outage. Local race/vet/contract/fixture gates and all initial GitHub CI gates passed. Opened ready PR #377; merge is gated on the final automated review and post-trajectory CI state. Released the fleet worker, stopped the exact finn broker/mount/tunnel and sf mount/server/auth processes, removed both scratch directories, and restored Agent Relay workspace Default.

**Approach:** Standard approach

---

## Key Decisions

### Use a self-hosted durable-local relayfile server on sf with unique Tailscale port 18080, unique workspace/mount/state directories on both hosts, websocket mounts at 200ms fallback polling, and content hashes plus monotonic timestamps as acceptance evidence
- **Chose:** Use a self-hosted durable-local relayfile server on sf with unique Tailscale port 18080, unique workspace/mount/state directories on both hosts, websocket mounts at 200ms fallback polling, and content hashes plus monotonic timestamps as acceptance evidence
- **Reasoning:** This exercises real network transport and production mount code while isolating all state from the active rw_7ccfea89 sessions; durable-local preserves server truth through mount restarts.

### Fix WebSocket dial error logging by redacting sensitive URL query values before any mount log output
- **Chose:** Fix WebSocket dial error logging by redacting sensitive URL query values before any mount log output
- **Reasoning:** The real partition produced a log line containing the full JWT from the ws?token= URL. The token is required on the wire today but must never survive into logs; redaction at the mount logging boundary covers wrapped network errors without changing transport behavior.

### Redact WebSocket credentials at both Syncer logging and webSocketDialError boundaries
- **Chose:** Redact WebSocket credentials at both Syncer logging and webSocketDialError boundaries
- **Reasoning:** The first argument-only logger patch passed unit tests but the live relayfile-mount caller logged MaintainWebSocket errors directly. A second real Tailscale outage reproduced the leak. Sanitizing the fully formatted Syncer line plus webSocketDialError.Error covers internal and external callers; a third live outage produced three [REDACTED] markers and zero raw token occurrences.

### Publish PR #377 as draft, then promote and merge only after all repository CI and review gates pass
- **Chose:** Publish PR #377 as draft, then promote and merge only after all repository CI and review gates pass
- **Reasoning:** The change fixes a credential exposure found on real hardware and has full local race plus live-replay evidence. The repository's merge-when-proven convention is appropriate, but draft-first prevents merging before E2E and automated review complete.

---

## Chapters

### 1. Work
*Agent: default*

- Use a self-hosted durable-local relayfile server on sf with unique Tailscale port 18080, unique workspace/mount/state directories on both hosts, websocket mounts at 200ms fallback polling, and content hashes plus monotonic timestamps as acceptance evidence: Use a self-hosted durable-local relayfile server on sf with unique Tailscale port 18080, unique workspace/mount/state directories on both hosts, websocket mounts at 200ms fallback polling, and content hashes plus monotonic timestamps as acceptance evidence
- Fresh Agent Relay workspace and finn fleet node are live with spawn:codex on broker 11.2.0. Both 11.1.1 and 11.2.0 fleet enable fail from a bundled SDK capability mismatch, but default workspace policy still permits enrollment and listing, so continue through targeted fleet spawn and preserve the failure as product evidence.
- Fix WebSocket dial error logging by redacting sensitive URL query values before any mount log output: Fix WebSocket dial error logging by redacting sensitive URL query values before any mount log output
- Redact WebSocket credentials at both Syncer logging and webSocketDialError boundaries: Redact WebSocket credentials at both Syncer logging and webSocketDialError boundaries
- The cross-machine collaboration, mount restart, offline write/reconnect, convergence hashes, and bidirectional latency run all passed. Live repair validation caught an incomplete first security fix before publication; the final binary was hash-matched on both hosts and passed a fresh real outage replay.
- Publish PR #377 as draft, then promote and merge only after all repository CI and review gates pass: Publish PR #377 as draft, then promote and merge only after all repository CI and review gates pass

---

## Artifacts

**Commits:** 9718cf1
**Files changed:** 21
