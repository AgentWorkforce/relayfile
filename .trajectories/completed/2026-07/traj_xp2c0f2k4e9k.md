# Trajectory: Normalize provider filters to VFS roots

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 94%
> **Started:** July 30, 2026 at 08:41 AM
> **Completed:** July 30, 2026 at 08:43 AM

---

## Summary

Mapped public provider IDs to VFS roots in mountscope so valid aliased multi-root provider filters pass and CLI cleanup shares the same mapping.

**Approach:** Standard approach

---

## Key Decisions

### Centralize provider-to-VFS-root mapping in mountscope
- **Chose:** Centralize provider-to-VFS-root mapping in mountscope
- **Reasoning:** Planning validates VFS path segments while event APIs use provider ids; one shared mapping keeps CLI cleanup and both mount entrypoints aligned for aliases.

---

## Chapters

### 1. Work
*Agent: default*

- Centralize provider-to-VFS-root mapping in mountscope: Centralize provider-to-VFS-root mapping in mountscope
