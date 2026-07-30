# Trajectory: Extract shared mount scope contract from PR #384

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 92%
> **Started:** July 30, 2026 at 12:20 AM
> **Completed:** July 30, 2026 at 12:23 AM

---

## Summary

Extracted the standalone daemon's path parsing and scoped layout planning into internal/mountscope, added overlap/blank/path-file protections and multi-path FUSE refusal, and clarified daemon-only scoped usage. No shipped relayfile CLI behavior changes. Full go test ./... -count=1, focused vet, and focused race tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Split #379 into a three-PR review stack without partial rollout
- **Chose:** Split #379 into a three-PR review stack without partial rollout
- **Reasoning:** PR #384 accumulated same-class multi-root findings across four review rounds. Unit A can land independently because it only centralizes the existing daemon scope contract. Units B and C are stacked for reviewability, but B must not merge until C is ready because a working scoped mount with single-root operator reporting would misrepresent a security-relevant boundary.

---

## Chapters

### 1. Work
*Agent: default*

- Split #379 into a three-PR review stack without partial rollout: Split #379 into a three-PR review stack without partial rollout
