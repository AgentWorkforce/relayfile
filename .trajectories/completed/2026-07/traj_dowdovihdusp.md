# Trajectory: Keep delete-pending writebacks visible

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 92%
> **Started:** July 30, 2026 at 07:23 AM
> **Completed:** July 30, 2026 at 07:29 AM

---

## Summary

Centralized tracked-file JSON parsing and pending-writeback semantics, including delete-pending list coverage

**Approach:** Standard approach

---

## Key Decisions

### Treat deletePending as authoritative pending state
- **Chose:** Treat deletePending as authoritative pending state
- **Reasoning:** A queued delete can have neither a local file nor a stored hash; dropping its explicit state hides a destructive operation and makes list disagree with status.

### Make mountsync own the persisted tracked-file shape and pending predicate
- **Chose:** Make mountsync own the persisted tracked-file shape and pending predicate
- **Reasoning:** Four consumers (writer public status, CLI aggregate status, disconnect preflight, and writeback list) must not re-declare either the JSON fields or Dirty/DeletePending semantics independently.

---

## Chapters

### 1. Work
*Agent: default*

- Treat deletePending as authoritative pending state: Treat deletePending as authoritative pending state
- Make mountsync own the persisted tracked-file shape and pending predicate: Make mountsync own the persisted tracked-file shape and pending predicate
