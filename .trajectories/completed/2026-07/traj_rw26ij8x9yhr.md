# Trajectory: Reject case-folded scoped root collisions

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 95%
> **Started:** July 30, 2026 at 01:56 AM
> **Completed:** July 30, 2026 at 02:00 AM

---

## Summary

Rejected case-folded scoped-root overlaps and mixed-case reserved runtime directories; focused mountscope tests and full go vet/go test suite pass.

**Approach:** Standard approach

---

## Key Decisions

### Reject scoped roots that alias after case folding
- **Chose:** Reject scoped roots that alias after case folding
- **Reasoning:** Relayfile configurations move between hosts; accepting /github and /GitHub on a case-sensitive machine creates concurrent Syncers over one directory when used on common case-insensitive macOS or Windows filesystems. Rejecting at the shared scope boundary makes the portable configuration safe by construction.

---

## Chapters

### 1. Work
*Agent: default*

- Reject scoped roots that alias after case folding: Reject scoped roots that alias after case folding
