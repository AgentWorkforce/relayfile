# Trajectory: Finish scoped multi-path operator surfaces

> **Status:** ✅ Completed
> **Task:** relayfile#379 / Unit C
> **Confidence:** 88%
> **Started:** July 30, 2026 at 02:43 AM
> **Completed:** July 30, 2026 at 06:13 AM

---

## Summary

Implemented Unit C operator surfaces for scoped multi-path mounts, then fixed independent-review findings in legacy topology recovery, catalog compatibility retry routing, child-state precedence, and cloud-only disconnect planning.

**Approach:** Standard approach

---

## Key Decisions

### Represent missing exact remote-root state as unknown instead of silently defaulting to workspace root
- **Chose:** Represent missing exact remote-root state as unknown instead of silently defaulting to workspace root
- **Reasoning:** A singular child root is truthful only when persisted topology or the child snapshot supplies it. Falling back to / converts absent state into an unscoped claim and can misroute pending writeback operations; operator surfaces now refuse when the legacy exact root cannot be established.

### Closed all three Unit C review findings at their trust boundaries
- **Chose:** Closed all three Unit C review findings at their trust boundaries
- **Reasoning:** Cloud-only disconnect now treats missing LocalDir as no local cleanup; legacy exact records recover their child root from persisted runtime state; scoped retry resolves catalog compatibility records to the matching child and child records take precedence over stale catalog duplicates.

---

## Chapters

### 1. Work
*Agent: default*

- Represent missing exact remote-root state as unknown instead of silently defaulting to workspace root: Represent missing exact remote-root state as unknown instead of silently defaulting to workspace root
- Closed all three Unit C review findings at their trust boundaries: Closed all three Unit C review findings at their trust boundaries
- Unit C's first independent review found three consumer defects beyond the inventory's intended behavior; all are now fixed at routing/topology boundaries and the complete Go/TS/contract/Windows validation set is green.

---

## Artifacts

**Commits:** 8293a6f, 73ee96d, 7259b13, 2b665ae, 7f1271e, 7d3f439, 99970e6, c97a789, da4ce69, 0e6711b
**Files changed:** 52
