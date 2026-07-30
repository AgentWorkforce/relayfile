# Trajectory: Attribute private mount state during legacy migration

> **Status:** ✅ Completed
> **Task:** relayfile#386 review
> **Confidence:** 92%
> **Started:** July 30, 2026 at 02:59 AM
> **Completed:** July 30, 2026 at 02:59 AM

---

## Summary

Added private-state workspace/root identity, ignored attributable unrelated state, and retained fail-closed handling for identity-less legacy files.

**Approach:** Standard approach

---

## Key Decisions

### Persist private-state ownership metadata and keep identity-less state conservative
- **Chose:** Persist private-state ownership metadata and keep identity-less state conservative
- **Reasoning:** Workspace/local-root metadata lets migration ignore unrelated state without guessing. Existing identity-less legacy state remains unknown and therefore still requires --rehome.

---

## Chapters

### 1. Work
*Agent: default*

- Persist private-state ownership metadata and keep identity-less state conservative: Persist private-state ownership metadata and keep identity-less state conservative
