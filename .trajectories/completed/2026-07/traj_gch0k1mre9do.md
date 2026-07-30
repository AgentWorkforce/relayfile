# Trajectory: Address Unit C exact review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 94%
> **Started:** July 30, 2026 at 02:17 AM
> **Completed:** July 30, 2026 at 02:20 AM

---

## Summary

Addressed all three Unit C review findings: disconnect checks private dirty/delete-pending state, aggregate counts include pending deletes, and writeback list uses persisted scope roots when public state is absent. Focused regressions pass.

**Approach:** Standard approach

---

## Key Decisions

### Treat dirty and deletePending as one pending-mutation state
- **Chose:** Treat dirty and deletePending as one pending-mutation state
- **Reasoning:** The Syncer persists both as authoritative work not yet accepted remotely. Every aggregate count and destructive preflight must share that definition or failed deletes disappear from status and safety checks.

### Pass persisted scope roots into writeback-list reconstruction
- **Chose:** Pass persisted scope roots into writeback-list reconstruction
- **Reasoning:** Private state can be durable before the public child snapshot. The catalog scope is already known and remains authoritative when .relay/state.json is absent, so re-deriving it from an optional artifact creates false root paths.

---

## Chapters

### 1. Work
*Agent: default*

- Treat dirty and deletePending as one pending-mutation state: Treat dirty and deletePending as one pending-mutation state
- Pass persisted scope roots into writeback-list reconstruction: Pass persisted scope roots into writeback-list reconstruction
