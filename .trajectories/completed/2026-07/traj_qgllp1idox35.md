# Trajectory: Preserve local infrastructure and centralize cross-scope dead letters

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 06:58 AM
> **Completed:** July 30, 2026 at 07:00 AM

---

## Summary

Centralized multi-root dead letters at the catalog root and preserved mount-excluded infrastructure during scoped disconnect cleanup.

**Approach:** Standard approach

---

## Key Decisions

### Store cross-scope dead-letter records at the catalog root
- **Chose:** Store cross-scope dead-letter records at the catalog root
- **Reasoning:** A sibling provider's disconnect preflight cannot discover a bulk record stored only under the first child's runtime root.

### Preserve mount-excluded infrastructure during scoped disconnect
- **Chose:** Preserve mount-excluded infrastructure during scoped disconnect
- **Reasoning:** Entries such as .git are intentionally outside mount synchronization and therefore cannot be deleted as mirrored provider content.

---

## Chapters

### 1. Work
*Agent: default*

- Store cross-scope dead-letter records at the catalog root: Store cross-scope dead-letter records at the catalog root
- Preserve mount-excluded infrastructure during scoped disconnect: Preserve mount-excluded infrastructure during scoped disconnect
