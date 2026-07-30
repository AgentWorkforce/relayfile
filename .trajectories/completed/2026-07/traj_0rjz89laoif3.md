# Trajectory: Read scoped writeback status from private state

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 93%
> **Started:** July 30, 2026 at 01:43 AM
> **Completed:** July 30, 2026 at 01:43 AM

---

## Summary

Made aggregate writeback status count dirty files from each scope's persisted private mount state; regression proves bogus public pending counts are ignored.

**Approach:** Standard approach

---

## Key Decisions

### Treat private mount state as the pending-writeback source of truth
- **Chose:** Treat private mount state as the pending-writeback source of truth
- **Reasoning:** The public child snapshot historically derives pending counts from an obsolete in-mirror legacy file. Operator status now resolves the exact private state identity for each scope and counts dirty tracked files there.

---

## Chapters

### 1. Work
*Agent: default*

- Treat private mount state as the pending-writeback source of truth: Treat private mount state as the pending-writeback source of truth
