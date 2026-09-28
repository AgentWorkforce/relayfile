# Trajectory: Implement content-hash client cache and jittered Retry-After handling

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** September 27, 2026 at 07:55 PM
> **Completed:** September 27, 2026 at 08:01 PM

---

## Summary

Added SDK content-hash caches, persistent mount object reuse, polite full-jitter retries, bounded bootstrap fan-out, and cursor-safe listen reconnects

**Approach:** Standard approach

---

## Key Decisions

### Use content hashes rather than the existing revision ETag for conditional cache identity
- **Chose:** Use content hashes rather than the existing revision ETag for conditional cache identity
- **Reasoning:** Current ETag semantics are revision-based while contentHash is already in response bodies and tree entries.

### Include the FUSE websocket invalidator but defer unrelated write and control-plane retry loops
- **Chose:** Include the FUSE websocket invalidator but defer unrelated write and control-plane retry loops
- **Reasoning:** FUSE reproduces the shipped reconnect-storm path; setup retries, agents reconnect, outbox writes, and polite polling are outside the read fan-in failure mode.

---

## Chapters

### 1. Work
*Agent: default*

- Use content hashes rather than the existing revision ETag for conditional cache identity: Use content hashes rather than the existing revision ETag for conditional cache identity
- Include the FUSE websocket invalidator but defer unrelated write and control-plane retry loops: Include the FUSE websocket invalidator but defer unrelated write and control-plane retry loops
