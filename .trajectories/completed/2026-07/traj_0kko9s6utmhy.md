# Trajectory: Close exact-root and scoped-disconnect review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 06:46 AM
> **Completed:** July 30, 2026 at 06:48 AM

---

## Summary

Made live exact runtime topology authoritative and added scoped catalog-root compatibility-state inspection before provider disconnect.

**Approach:** Standard approach

---

## Key Decisions

### Treat live exact runtime root as authoritative and catalog topology as fallback
- **Chose:** Treat live exact runtime root as authoritative and catalog topology as fallback
- **Reasoning:** Exact mounts can retain stale catalog RemotePaths after remount; operator surfaces must follow the active root recorded in .relay/state.json.

### Inspect scoped catalog-root compatibility state during provider disconnect
- **Chose:** Inspect scoped catalog-root compatibility state during provider disconnect
- **Reasoning:** Cross-scope bulk dead letters are deliberately stored at the catalog root, so child-only inspection can falsely declare a destructive disconnect safe.

---

## Chapters

### 1. Work
*Agent: default*

- Treat live exact runtime root as authoritative and catalog topology as fallback: Treat live exact runtime root as authoritative and catalog topology as fallback
- Inspect scoped catalog-root compatibility state during provider disconnect: Inspect scoped catalog-root compatibility state during provider disconnect
