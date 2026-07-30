# Trajectory: Address final Unit B review findings

> **Status:** ✅ Completed
> **Task:** relayfile#386
> **Confidence:** 93%
> **Started:** July 30, 2026 at 02:48 AM
> **Completed:** July 30, 2026 at 02:54 AM

---

## Summary

Fixed remote-root-aware basename handling across watcher, scan, and path mapping; corrected TS refusal text; scrubbed machine-specific project IDs; proved RemotePaths replacement; full Go and contract/SDK validation passed.

**Approach:** Standard approach

---

## Key Decisions

### Make mount-basename collision checks depend on the active remote root
- **Chose:** Make mount-basename collision checks depend on the active remote root
- **Reasoning:** The collision round-trips onto the mount directory only for remoteRoot=/; under a scoped non-root mount, a same-named child is a valid descendant and must reach both watcher and scan paths.

### Reject CodeRabbit's stale-RemotePaths finding as not present
- **Chose:** Reject CodeRabbit's stale-RemotePaths finding as not present
- **Reasoning:** mergeWorkspaceRecords already replaces RemotePaths from the update rather than unioning prior values; added a regression test that proves retired paths are removed.

---

## Chapters

### 1. Work
*Agent: default*

- Make mount-basename collision checks depend on the active remote root: Make mount-basename collision checks depend on the active remote root
- Reject CodeRabbit's stale-RemotePaths finding as not present: Reject CodeRabbit's stale-RemotePaths finding as not present
