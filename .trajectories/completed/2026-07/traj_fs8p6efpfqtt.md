# Trajectory: Resolve scoped operator state through persisted private identities

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 91%
> **Started:** July 30, 2026 at 01:32 AM
> **Completed:** July 30, 2026 at 01:34 AM

---

## Summary

Resolved scoped status, health, dead-letter, skip-stuck, and writeback reads through each persisted private mount-state identity; regression fixtures now use production hashed paths; full vet and Go suite pass.

**Approach:** Standard approach

---

## Key Decisions

### Resolve every scoped operator read from the persisted mount identity
- **Chose:** Resolve every scoped operator read from the persisted mount identity
- **Reasoning:** Normal mounts store private state under hashed MountStateDir paths. Operator commands must derive the same workspace, remote root, local root, state-file/state-dir, and mount-kind identity instead of assuming legacy files under each child.

---

## Chapters

### 1. Work
*Agent: default*

- Resolve every scoped operator read from the persisted mount identity: Resolve every scoped operator read from the persisted mount identity
