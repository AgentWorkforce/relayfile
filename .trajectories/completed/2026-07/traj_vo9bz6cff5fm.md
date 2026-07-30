# Trajectory: Fix scoped background initialization regression

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 95%
> **Started:** July 30, 2026 at 03:20 AM
> **Completed:** July 30, 2026 at 03:23 AM

---

## Summary

Made background mount preparation topology-aware and added a regression proving scoped background setup preserves absence of unsynchronized root artifacts; full validation passes.

**Approach:** Standard approach

---

## Key Decisions

### Keep detached background preparation topology-aware
- **Chose:** Keep detached background preparation topology-aware
- **Reasoning:** The detached spawn boundary repeats layout preparation for self-containment, but must use the persisted local layout so scoped mounts never recreate global digest or skill artifacts.

---

## Chapters

### 1. Work
*Agent: default*

- Keep detached background preparation topology-aware: Keep detached background preparation topology-aware
