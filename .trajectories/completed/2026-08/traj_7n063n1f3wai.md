# Trajectory: Fix TypeScript SDK write-attempt read-cache invalidation

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** August 27, 2026 at 04:19 PM
> **Completed:** August 27, 2026 at 04:20 PM

---

## Summary

Bracketed SDK file mutations with pre-request and finally cache eviction; added populated-cache 409 and sibling mutation regressions; SDK build, typecheck, and 277 tests pass; success-only source ablation fails all 5 targeted regressions.

**Approach:** Standard approach

---

## Key Decisions

### Bracket cache-aware file mutations with pre-request and finally eviction
- **Chose:** Bracket cache-aware file mutations with pre-request and finally eviction
- **Reasoning:** A write attempt invalidates revision knowledge regardless of outcome. Pre-eviction prevents cached hits once the attempt starts; finally eviction removes entries or in-flight reads repopulated during the concurrent-reader window. This favors correctness over retaining a possibly valid entry after auth or network failures.

---

## Chapters

### 1. Work
*Agent: default*

- Bracket cache-aware file mutations with pre-request and finally eviction: Bracket cache-aware file mutations with pre-request and finally eviction
