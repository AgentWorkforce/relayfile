# Trajectory: Address PR #384 scoped mount review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 92%
> **Started:** July 29, 2026 at 11:43 PM
> **Completed:** July 29, 2026 at 11:43 PM

---

## Summary

Closed PR #384 review gaps: blank/null paths cannot widen a scoped allowlist, CLI status aggregates sibling runtime roots, and dead-letter list/refresh/retry/replay traverse persisted scoped roots

**Approach:** Standard approach

---

## Key Decisions

### Treat persisted scoped roots as the runtime-state boundary and sweep the catalog root only for legacy dead letters
- **Chose:** Treat persisted scoped roots as the runtime-state boundary and sweep the catalog root only for legacy dead letters
- **Reasoning:** Each scoped syncer owns child .relay state; aggregating the common root would double-count stale exact-layout state, while dead-letter compatibility must still discover pre-scoped root records

### Ignore blank path candidates before normalization and apply fallback only when no valid candidates remain
- **Chose:** Ignore blank path candidates before normalization and apply fallback only when no valid candidates remain
- **Reasoning:** Relay intentionally treats / as a real full-workspace root, so blank/null display input must never become indistinguishable from an explicit root selection

---

## Chapters

### 1. Work
*Agent: default*

- Treat persisted scoped roots as the runtime-state boundary and sweep the catalog root only for legacy dead letters: Treat persisted scoped roots as the runtime-state boundary and sweep the catalog root only for legacy dead letters
- Ignore blank path candidates before normalization and apply fallback only when no valid candidates remain: Ignore blank path candidates before normalization and apply fallback only when no valid candidates remain
- All three review findings are addressed with behavioral tests: scoped allowlists cannot widen through blanks, runtime status aggregates sibling roots, and dead-letter list/refresh/retry/replay remain discoverable
