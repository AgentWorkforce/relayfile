# Trajectory: Make Relayfile PR 477 shutdown retry regression deterministic

> **Status:** ✅ Completed
> **Task:** relayfile#477
> **Confidence:** 97%
> **Started:** September 8, 2026 at 09:13 PM
> **Completed:** September 8, 2026 at 09:14 PM

---

## Summary

Made PR 477 once shutdown/retry regression deterministic with scoped fake timers and local readiness state; runtime unchanged. Focused test passed 50 repetitions, SDK 293/293, typecheck/build, Go tests, diff/secret checks passed.

**Approach:** Standard approach

---

## Key Decisions

### Use fake timers and a local ready-state probe to control the resumable backoff test
- **Chose:** Use fake timers and a local ready-state probe to control the resumable backoff test
- **Reasoning:** This proves stop occurs before retry timer advancement without relying on a real-time 20ms scheduling window or network retry timing.

---

## Chapters

### 1. Work
*Agent: default*

- Use fake timers and a local ready-state probe to control the resumable backoff test: Use fake timers and a local ready-state probe to control the resumable backoff test

---

## Artifacts

**Commits:** bcb1e652
**Files changed:** 2
