# Trajectory: Agree CLI writeback contract for relayfile#287

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** June 16, 2026 at 08:34 PM
> **Completed:** June 16, 2026 at 08:47 PM

---

## Summary

Implemented relayfile CLI writeback update/delete support, canonical push updates, and failed-op handling for false-success prevention. Validated with full Go suite and OpenAPI contract check.

**Approach:** Standard approach

---

## Key Decisions

### Phase 0 writeback contract accepts draft-create and canonical-update/delete path classification
- **Chose:** Phase 0 writeback contract accepts draft-create and canonical-update/delete path classification
- **Reasoning:** Relayfile currently models provider mutations as file_upsert/file_delete operations; adapter classification by VFS path preserves existing store semantics while enabling CLI update/delete commands.

### Adapter rejection contract requires non-nil errors for non-OK writeback outcomes
- **Chose:** Adapter rejection contract requires non-nil errors for non-OK writeback outcomes
- **Reasoning:** The relayfile CLI observes operation state from /ops/{opId}; if adapters return nil for rejected statuses, relayfile marks the operation succeeded and false-success cannot be fixed at the CLI layer.

---

## Chapters

### 1. Work
*Agent: default*

- Phase 0 writeback contract accepts draft-create and canonical-update/delete path classification: Phase 0 writeback contract accepts draft-create and canonical-update/delete path classification
- Adapter rejection contract requires non-nil errors for non-OK writeback outcomes: Adapter rejection contract requires non-nil errors for non-OK writeback outcomes
- Phase 1 CLI implementation underway: command routing and tests added for canonical update/delete; validating build and behavior now
