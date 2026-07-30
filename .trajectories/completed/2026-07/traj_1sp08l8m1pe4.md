# Trajectory: Make retry and health independent of compatibility storage root

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 06:52 AM
> **Completed:** July 30, 2026 at 06:54 AM

---

## Summary

Made scoped retry grouping independent of dead-letter storage root and included compatibility-root outbox state in workspace health.

**Approach:** Standard approach

---

## Key Decisions

### Route every scoped retry path from the persisted allowlist
- **Chose:** Route every scoped retry path from the persisted allowlist
- **Reasoning:** Dead-letter storage placement is a compatibility detail and cannot safely determine the Syncer for a comma-joined multi-root operation.

### Aggregate outbox health across workspaceStateDirs
- **Chose:** Aggregate outbox health across workspaceStateDirs
- **Reasoning:** Scoped child roots are active runtime owners, but compatibility outbox records can remain at the catalog root and must stay observable.

---

## Chapters

### 1. Work
*Agent: default*

- Route every scoped retry path from the persisted allowlist: Route every scoped retry path from the persisted allowlist
- Aggregate outbox health across workspaceStateDirs: Aggregate outbox health across workspaceStateDirs
