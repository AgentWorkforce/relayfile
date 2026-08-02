# Trajectory: Refuse disconnect on unobserved local drift

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 07:40 AM
> **Completed:** July 30, 2026 at 08:33 AM

---

## Summary

Closed stopped-mount drift deletion, enumerated post-preflight cleanup, timestamp ordering, incomplete filesystem counts, and incomplete dead-letter feed pruning for Unit C.

**Approach:** Standard approach

---

## Key Decisions

### Use the writeback-list filesystem scan as disconnect evidence
- **Chose:** Use the writeback-list filesystem scan as disconnect evidence
- **Reasoning:** Persisted dirty flags alone do not cover edits or new files made while the watcher is stopped; destructive cleanup must prove there is no pending local obligation.

### Parse RFC3339Nano before selecting aggregate timestamps
- **Chose:** Parse RFC3339Nano before selecting aggregate timestamps
- **Reasoning:** Variable-width fractional seconds are not lexicographically ordered.

### Treat an absent scoped local directory as an empty local scan
- **Chose:** Treat an absent scoped local directory as an empty local scan
- **Reasoning:** A private state file can exist before its child mirror is materialized; absence proves there are no local files, while permission and non-directory errors still refuse.

### Make disconnect cleanup an observed allow-list
- **Chose:** Make disconnect cleanup an observed allow-list
- **Reasoning:** Preflight cannot safely re-read after Cloud DELETE, so execution must delete only paths enumerated before the mutation and must verify observed file/symlink fingerprints; non-recursive directory removal preserves later arrivals.

### Make malformed aggregate timestamps unknown
- **Chose:** Make malformed aggregate timestamps unknown
- **Reasoning:** Selecting a valid sibling timestamp when any child timestamp is unparseable would report a confident latest value from partial state.

---

## Chapters

### 1. Work
*Agent: default*

- Use the writeback-list filesystem scan as disconnect evidence: Use the writeback-list filesystem scan as disconnect evidence
- Parse RFC3339Nano before selecting aggregate timestamps: Parse RFC3339Nano before selecting aggregate timestamps
- Treat an absent scoped local directory as an empty local scan: Treat an absent scoped local directory as an empty local scan
- Make disconnect cleanup an observed allow-list: Make disconnect cleanup an observed allow-list
- Make malformed aggregate timestamps unknown: Make malformed aggregate timestamps unknown
- The destructive-decision sweep expanded the same absent-evidence class beyond cursor flags
