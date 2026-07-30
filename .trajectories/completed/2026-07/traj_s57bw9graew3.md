# Trajectory: Refuse integration disconnect with unsynced scoped state

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 93%
> **Started:** July 30, 2026 at 02:05 AM
> **Completed:** July 30, 2026 at 02:07 AM

---

## Summary

Made integration disconnect preflight all provider-owned runtime roots before Cloud mutation, refuse active mounts and counted unsynced state, and preserve all mirror/runtime files on refusal. Focused disconnect and inherited case-fold boundary tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Refuse disconnect before Cloud mutation when local work is pending
- **Chose:** Refuse disconnect before Cloud mutation when local work is pending
- **Reasoning:** Conflicts, dead letters, and pending/failed/attention outbox records are the only copies of work not accepted remotely. The preflight must run before the Cloud DELETE and again before local cleanup; it reports counts and preserves every file on refusal. Active or unverified mounts also refuse to close the check-delete race.

---

## Chapters

### 1. Work
*Agent: default*

- Refuse disconnect before Cloud mutation when local work is pending: Refuse disconnect before Cloud mutation when local work is pending
