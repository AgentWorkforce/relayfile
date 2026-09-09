# Trajectory: Fix PR 477 shutdown regression unhandled rejection ordering

> **Status:** ✅ Completed
> **Task:** relayfile#477
> **Confidence:** 75%
> **Started:** September 8, 2026 at 09:52 PM
> **Completed:** September 8, 2026 at 09:52 PM

---

## Summary

Attached the SDK shutdown race rejection matcher before advancing the fake retry timer, eliminating strict-runner unhandled rejection exposure. Self-reported and non-gating: launcher repetitions, SDK checks, Go checks, diff review, and secret scans were reported to pass; this trajectory contains no captured command/output evidence.

**Approach:** Standard approach

---

## Key Decisions

### Attach the ready rejection assertion before advancing the fake backoff timer
- **Chose:** Attach the ready rejection assertion before advancing the fake backoff timer
- **Reasoning:** The waitForReady promise rejects in the timer continuation; attaching expect afterward can surface an unhandled rejection under strict runners.

---

## Chapters

### 1. Work
*Agent: default*

- Attach the ready rejection assertion before advancing the fake backoff timer: Attach the ready rejection assertion before advancing the fake backoff timer

---

## Artifacts

**Commits:** c0d9a80e
**Files changed:** 1
