# Trajectory: Reject explicitly empty scoped allowlists

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 96%
> **Started:** July 30, 2026 at 03:27 AM
> **Completed:** July 30, 2026 at 03:32 AM

---

## Summary

Rejected configured paths files with no usable roots while preserving the unset default; covered both arms and all empty-file forms; full Go validation passes.

**Approach:** Standard approach

---

## Key Decisions

### Distinguish configured empty allowlists from absent configuration
- **Chose:** Distinguish configured empty allowlists from absent configuration
- **Reasoning:** An unset paths-file retains the historical root fallback; a configured file with no usable roots is an explicit restriction and must refuse before initialization.

---

## Chapters

### 1. Work
*Agent: default*

- Distinguish configured empty allowlists from absent configuration: Distinguish configured empty allowlists from absent configuration
