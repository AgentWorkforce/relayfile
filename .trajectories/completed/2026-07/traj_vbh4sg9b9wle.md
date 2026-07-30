# Trajectory: Fix reserved-boundary review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 93%
> **Started:** July 30, 2026 at 03:58 AM
> **Completed:** July 30, 2026 at 04:00 AM

---

## Summary

Separated case-folded planning collisions from exact runtime reservations, reserved catalog artifacts only for root mounts, and rejected runtime sentinels at every scoped path segment.

**Approach:** Standard approach

---

## Key Decisions

### Separate collision identity from runtime reservation and scope catalog-root artifacts to root mounts
- **Chose:** Separate collision identity from runtime reservation and scope catalog-root artifacts to root mounts
- **Reasoning:** Scoped planning must conservatively reject case-folded local collisions, while runtime sync must preserve case-distinct provider paths and treat catalog artifacts as reserved only at remote root '/'. Mount-runtime sentinels remain reserved at every segment and every scope.

---

## Chapters

### 1. Work
*Agent: default*

- Separate collision identity from runtime reservation and scope catalog-root artifacts to root mounts: Separate collision identity from runtime reservation and scope catalog-root artifacts to root mounts
