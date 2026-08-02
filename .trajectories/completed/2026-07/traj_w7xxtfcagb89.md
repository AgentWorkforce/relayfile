# Trajectory: Preserve exact root when runtime state is unavailable

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 94%
> **Started:** July 30, 2026 at 01:52 AM
> **Completed:** July 30, 2026 at 01:52 AM

---

## Summary

Made exact aggregate status preserve the persisted non-root path when runtime state is absent or unreadable; focused topology tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Distinguish absent runtime topology from the root path
- **Chose:** Distinguish absent runtime topology from the root path
- **Reasoning:** The legacy helper returns / for both a real root mount and missing/unreadable state. Aggregate exact status now uses a presence-aware read and falls back to the persisted exact root only when runtime state makes no claim.

---

## Chapters

### 1. Work
*Agent: default*

- Distinguish absent runtime topology from the root path: Distinguish absent runtime topology from the root path
