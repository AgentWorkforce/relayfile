# Trajectory: Repair PR #477 resumable exit classification

> **Status:** ✅ Completed
> **Task:** PR-477
> **Confidence:** 92%
> **Started:** September 8, 2026 at 06:34 PM
> **Completed:** September 8, 2026 at 06:42 PM

---

## Summary

Repaired PR #477 once-mode classification: explicit resumable bootstrap markers now yield EX_TEMPFAIL only for all-resumable error trees; provider/cycle/config failures and mixed joins remain exit 1, and scoped siblings cancel on mixed fatal outcomes. Added regressions and completed focused/full validation.

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
- Focused race tests, full Go, SDK launcher, typecheck/build, contract, release, and secrets checks pass, including the full Node 22 SDK suite.
