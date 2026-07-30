# Trajectory: Fix Unit B exact-head review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 93%
> **Started:** July 30, 2026 at 03:41 AM
> **Completed:** July 30, 2026 at 03:45 AM

---

## Summary

Closed exact-head review gaps by rejecting nested runtime-owned scoped roots and handing detached mounts the parent-validated allowlist instead of mutable paths-file input.

**Approach:** Standard approach

---

## Key Decisions

### Reject scoped roots containing mount-runtime segments and serialize resolved background allowlists into child argv
- **Chose:** Reject scoped roots containing mount-runtime segments and serialize resolved background allowlists into child argv
- **Reasoning:** The syncer prunes runtime markers anywhere in a remote path, so validation must share that predicate; detached children must consume the parent's validated immutable topology rather than re-reading a mutable paths file or inherited env default.

---

## Chapters

### 1. Work
*Agent: default*

- Reject scoped roots containing mount-runtime segments and serialize resolved background allowlists into child argv: Reject scoped roots containing mount-runtime segments and serialize resolved background allowlists into child argv
