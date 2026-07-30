# Trajectory: Aggregate scoped mount operator surfaces

> **Status:** ✅ Completed
> **Task:** relayfile#379-unit-c
> **Confidence:** 93%
> **Started:** July 30, 2026 at 01:08 AM
> **Completed:** July 30, 2026 at 01:16 AM

---

## Summary

Made scoped mount operator surfaces truthful: typed exact/scoped/unknown status topology, aggregate health/status/writeback/dead-letter/replay, shared-limit skip-stuck, and aggregate writeback list. Full Go validation passes.

**Approach:** Standard approach

---

## Key Decisions

### Represent aggregate mount topology as exact, scoped, or unknown variants
- **Chose:** Represent aggregate mount topology as exact, scoped, or unknown variants
- **Reasoning:** A per-child sync state truthfully has one remoteRoot, but an aggregate scoped status has remoteRoots and a legacy blank layout proves no root at all. Separate Go response types make a fabricated singular aggregate root unrepresentable while preserving the child state schema.

### Aggregate writeback list from persisted runtime roots
- **Chose:** Aggregate writeback list from persisted runtime roots
- **Reasoning:** The mechanical consumer inventory found writeback list after four review rounds missed it. Pending rows come from active child roots; dead rows also sweep the catalog root for legacy compatibility, with deterministic deduplication.

---

## Chapters

### 1. Work
*Agent: default*

- Represent aggregate mount topology as exact, scoped, or unknown variants: Represent aggregate mount topology as exact, scoped, or unknown variants
- Aggregate writeback list from persisted runtime roots: Aggregate writeback list from persisted runtime roots
- The mechanical inventory collapsed the remaining multi-root consumer work to aggregate status topology, writeback/dead-letter consumers, skip-stuck, and writeback list. All four are implemented; full go vet and go test ./... pass. The stack constraint remains necessary because B alone still exposes lying root-level operator surfaces.
