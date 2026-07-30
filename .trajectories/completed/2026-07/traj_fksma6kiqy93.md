# Trajectory: Enumerate shared B/C mount concepts and settle Unit B exact head

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 08:46 AM
> **Completed:** July 30, 2026 at 08:51 AM

---

## Summary

Enumerated 13 B/C cross-boundary concepts; centralized provider identity/VFS-root mapping and remote-path canonicalization in mountscope; focused and full validation passed

**Approach:** Standard approach

---

## Key Decisions

### Centralized provider identity/root and remote-path normalization in mountscope
- **Chose:** Centralized provider identity/root and remote-path normalization in mountscope
- **Reasoning:** The B/C concept sweep found provider aliases and remote paths computed independently across CLI, Syncer, FUSE, and fallback catalog. mountscope is already the dependency shared by all three Go runtime surfaces, so delegating there removes the fifth-instance failure class without changing the persisted contract.

---

## Chapters

### 1. Work
*Agent: default*

- Centralized provider identity/root and remote-path normalization in mountscope: Centralized provider identity/root and remote-path normalization in mountscope
