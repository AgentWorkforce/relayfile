# Trajectory: Fix relayfile once timeout semantics

> **Status:** ✅ Completed
> **Confidence:** 93%
> **Started:** September 8, 2026 at 10:30 AM
> **Completed:** September 8, 2026 at 03:44 PM

---

## Summary

Fixed --once bootstrap timeout-yield handling with typed fatal/yield outcomes and deterministic regression coverage.

**Approach:** Standard approach

---

## Key Decisions

### Treat --once bootstrap failures as fatal
- **Chose:** Treat --once bootstrap failures as fatal
- **Reasoning:** False-success bug meant --once exited 0 even when bootstrapping timed out; explicit error preserves resumable checkpoint while signaling the incomplete state

### Represent deadline bootstrap yields with a typed cycle outcome marker
- **Chose:** Represent deadline bootstrap yields with a typed cycle outcome marker
- **Reasoning:** The once resume gate must distinguish an in-progress timeout yield from a fatal cycle error while preserving errors.Is/errors.As through the underlying cause.

---

## Chapters

### 1. Work
*Agent: default*

- Treat --once bootstrap failures as fatal: Treat --once bootstrap failures as fatal
- Represent deadline bootstrap yields with a typed cycle outcome marker: Represent deadline bootstrap yields with a typed cycle outcome marker
- The once bootstrap path now carries an explicit typed distinction between fatal cycle failures and DeadlineExceeded yields with a persisted in-progress checkpoint; focused regression coverage confirms resume, fatal chains, cancellation precedence, prior completion, and bounds.

---

## Artifacts

**Commits:** 856ed95a, 43c8bb96, 24846ae5
**Files changed:** 5
