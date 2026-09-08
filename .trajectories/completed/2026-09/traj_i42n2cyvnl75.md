# Trajectory: Fix PR 477 resumable readiness timeout contract

> **Status:** ✅ Completed
> **Task:** relayfile#477
> **Confidence:** 94%
> **Started:** September 8, 2026 at 10:05 PM
> **Completed:** September 8, 2026 at 10:12 PM

---

## Summary

Fixed exit-75 deadline handling and eliminated the shutdown-test unhandled-rejection window; focused tests passed 20 repeated runs plus the full launcher test and SDK typecheck.

**Approach:** Standard approach

---

## Key Decisions

### Timeout must win over generic early exit for a resumable exit-75 child unless explicit shutdown is already in progress

- **Chose:** Timeout must win over generic early exit for a resumable exit-75 child unless explicit shutdown is already in progress
- **Reasoning:** The public readyTimeoutMs contract requires MountReadyTimeoutError plus stop cleanup once retry budget expires. A concurrent explicit stop retains shutdown's terminal launch-failure precedence, and ordinary non-resumable exits retain their specific launch failure.

---

## Chapters

### 1. Work

_Agent: default_

- Timeout must win over generic early exit for a resumable exit-75 child unless explicit shutdown is already in progress: Timeout must win over generic early exit for a resumable exit-75 child unless explicit shutdown is already in progress
