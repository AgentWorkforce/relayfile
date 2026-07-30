# Trajectory: Expose multi-subtree mount scoping in shipped CLI

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 92%
> **Started:** July 29, 2026 at 11:06 PM
> **Completed:** July 29, 2026 at 11:21 PM

---

## Summary

Added repeatable remote-path and paths-file scoping to the shipped CLI, centralized mount scope planning with the daemon, persisted allowlists across restarts, serialized delegated-auth refresh across scoped loops, documented the surface, and added end-to-end and unit coverage.

**Approach:** Standard approach

---

## Key Decisions

### Use one scoped sync loop per allowlisted remote root and centralize scope planning
- **Chose:** Use one scoped sync loop per allowlisted remote root and centralize scope planning
- **Reasoning:** A single Syncer has one RemoteRoot and one local state namespace. Running one loop per normalized root preserves that invariant while scoped local directories prevent file and .relay state collisions. Extracting flag parsing, path normalization, layout validation, and planning into internal/mountscope keeps relayfile and relayfile-mount on the same contract.

---

## Chapters

### 1. Work
*Agent: default*

- Use one scoped sync loop per allowlisted remote root and centralize scope planning: Use one scoped sync loop per allowlisted remote root and centralize scope planning
- The shipped CLI now reaches the daemon's multi-subtree capability without duplicating its parsing contract. Focused E2E observed two exports land under separate scoped roots; full CLI and daemon suites pass. Remaining review focus is lifecycle/status behavior of the shared root and background process semantics.
