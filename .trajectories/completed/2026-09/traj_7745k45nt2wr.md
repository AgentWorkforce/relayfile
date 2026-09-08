# Trajectory: Repair PR #477 resumable exit classification

> **Status:** ✅ Completed
> **Task:** PR-477
> **Confidence:** 75%
> **Started:** September 8, 2026 at 06:34 PM
> **Completed:** September 8, 2026 at 06:42 PM

---

## Summary

Repaired PR #477 once-mode classification: explicit resumable bootstrap markers now yield EX_TEMPFAIL only for all-resumable error trees; provider/cycle/config failures and mixed joins remain exit 1, and scoped siblings cancel on mixed fatal outcomes. Self-reported and non-gating: regressions and focused/full validation were reported complete, but this trajectory contains no captured command/output evidence.

**Approach:** Standard approach

---

## Key Decisions

### Mark resumable bootstrap outcomes explicitly so fatal once failures remain non-retryable
- **Chose:** Mark resumable bootstrap outcomes explicitly so fatal once failures remain non-retryable
- **Reasoning:** The process exit and scoped sibling policy must distinguish bounded checkpoint/cancellation outcomes from provider, configuration, and cycle failures even when errors are wrapped or joined. An explicit marker plus an all-branches predicate preserves diagnostic wrapping while keeping fatal and mixed aggregates at exit 1.

---

## Chapters

### 1. Work
*Agent: default*

- Mark resumable bootstrap outcomes explicitly so fatal once failures remain non-retryable: Mark resumable bootstrap outcomes explicitly so fatal once failures remain non-retryable
- Self-reported and non-gating: focused race tests, Go, SDK launcher, typecheck/build, contract, release, and secrets checks were reported to pass; one Node 22 SDK run was reported at 291/291 and an independent repeat reproduced a known environment-only ErrorEvent baseline failure. This trajectory contains no captured command/output evidence.
