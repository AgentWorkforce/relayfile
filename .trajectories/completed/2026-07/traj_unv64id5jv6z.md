# Trajectory: Route cross-scope dead-letter retries per scoped runtime root

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 06:39 AM
> **Completed:** July 30, 2026 at 06:42 AM

---

## Summary

Grouped comma-joined dead-letter paths by scoped runtime root, prevalidated the complete plan, and routed one Syncer retry per group with order-independent coverage.

**Approach:** Standard approach

---

## Key Decisions

### Precompute and validate every retry group before invoking any Syncer
- **Chose:** Precompute and validate every retry group before invoking any Syncer
- **Reasoning:** A comma-joined dead-letter can span scoped roots; selecting one Syncer makes later paths unretryable, while resolving the complete plan first prevents partial retries when any path is invalid.

---

## Chapters

### 1. Work
*Agent: default*

- Precompute and validate every retry group before invoking any Syncer: Precompute and validate every retry group before invoking any Syncer
