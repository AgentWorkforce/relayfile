# Trajectory: Repair ACL pathless provider deletion reconciliation

> **Status:** ✅ Completed
> **Task:** Relayfile ACL-423
> **Confidence:** 92%
> **Started:** September 8, 2026 at 05:41 PM
> **Completed:** September 8, 2026 at 05:47 PM

---

## Summary

Repaired Relayfile ACL pathless provider deletion handling with sync.reconcile control events, durable prompt reconciliation, no-path fail-closed filtering, SDK/file-observer parity, and regression tests.

**Approach:** Standard approach

---

## Key Decisions

### Use a pathless sync.reconcile control event for unresolved provider deletes
- **Chose:** Use a pathless sync.reconcile control event for unresolved provider deletes
- **Reasoning:** Never guess or disclose hidden paths; filtered mounts receive an authoritative full reconciliation in the same sync cycle while malformed empty-path file.deleted events remain fail-closed.

---

## Chapters

### 1. Work
*Agent: default*

- Use a pathless sync.reconcile control event for unresolved provider deletes: Use a pathless sync.reconcile control event for unresolved provider deletes
- Pathless provider deletion now emits only a durable sync.reconcile control when no ACL-backed path can be named; mounts persist the prompt, reconcile authoritatively in-cycle, and filtered consumers receive no path. Focused/full serialized Go and package gates are green; parallel full Go and mountsync race expose inherited test-environment races.
