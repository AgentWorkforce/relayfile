# Trajectory: Harden descending clone cursor pagination

> **Status:** ✅ Completed
> **Task:** relayfile#524 review follow-up
> **Confidence:** 99%
> **Started:** October 3, 2026 at 12:31 AM
> **Completed:** October 3, 2026 at 12:33 AM

---

## Summary

Added fail-fast repeated-cursor detection to descending clone-manifest event pagination and exercised both multi-page success and cursor-cycle failure.

**Approach:** Applied the existing MalformedPaginationError pattern used by other Relayfile pagination loops and covered it with focused regression tests.

---

## Key Decisions

### Reject repeated descending event cursors immediately
- **Chose:** Reject repeated descending event cursors immediately
- **Rejected:** Rely on the idle watchdog
- **Reasoning:** A malformed or buggy server cursor could otherwise churn requests until the bootstrap watchdog fires; existing tree and incremental pagers already fail fast with MalformedPaginationError.

---

## Chapters

### 1. Work
*Agent: default*

- Reject repeated descending event cursors immediately: Reject repeated descending event cursors immediately
