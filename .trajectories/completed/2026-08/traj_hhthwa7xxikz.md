# Trajectory: Fix #445 SQL variable limit data loss

> **Status:** ✅ Completed
> **Task:** AgentWorkforce/relayfile#445
> **Confidence:** 90%
> **Started:** August 24, 2026 at 11:12 AM
> **Completed:** August 24, 2026 at 11:35 AM

---

## Summary

Fixed #445 by capping bulk write requests at 200 files, covering durable mount outbox maintenance and all public client bulk-write methods; added over-limit red/green regression coverage and verified all Go/TypeScript/Python suites.

**Approach:** Standard approach

---

## Key Decisions

### Cap bulk writes at 200 files across durable outbox and client boundaries
- **Chose:** Cap bulk writes at 200 files across durable outbox and client boundaries
- **Reasoning:** The outbox maintenance path chunks only by 8 MiB, so 1,454 small records can be reassembled into one request. A fixed 200-file cap stays well below SQLite's conservative 999-variable limit, preserves byte chunking, and protects foreground, maintenance, and direct client callers.

---

## Chapters

### 1. Work
*Agent: default*

- Cap bulk writes at 200 files across durable outbox and client boundaries: Cap bulk writes at 200 files across durable outbox and client boundaries
- Reproduced the production signature with 1,001 small workspace files; count chunking is green across Go mount, direct Go HTTP, TypeScript SDK, and sync/async Python SDK paths.
