# Trajectory: Preserve scoped runtime state on integration disconnect

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 02:02 AM
> **Completed:** July 30, 2026 at 02:02 AM

---

## Summary

Changed integration disconnect to remove scoped provider mirror content without erasing child .relay operational state; focused disconnect and adopt-marker tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Preserve scoped .relay state while deleting disconnected provider content
- **Chose:** Preserve scoped .relay state while deleting disconnected provider content
- **Reasoning:** A scoped child directory combines mirrored provider data with operational conflicts, dead letters, outbox, and cursor state. Whole-subtree deletion is unsafe; disconnect removes non-.relay entries at provider scopes and keeps sibling scopes untouched.

---

## Chapters

### 1. Work
*Agent: default*

- Preserve scoped .relay state while deleting disconnected provider content: Preserve scoped .relay state while deleting disconnected provider content
