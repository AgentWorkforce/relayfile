# Trajectory: Harden release workflow action pinning and dispatch input handling

> **Status:** ✅ Completed
> **Task:** #478
> **Confidence:** 95%
> **Started:** September 8, 2026 at 06:39 PM
> **Completed:** September 8, 2026 at 06:39 PM

---

## Summary

Pinned all publish workflow actions to immutable commit SHAs, validated and mapped package/dry_run inputs before shell use, added regression tests, and passed release/actionlint/Node22 validation.

**Approach:** Standard approach

---

## Key Decisions

### Pinned every publish-workflow action to a full commit SHA and retained major-version comments
- **Chose:** Pinned every publish-workflow action to a full commit SHA and retained major-version comments
- **Reasoning:** The credential-bearing release workflow must not resolve mutable action tags at runtime.

### Validated package and dry_run inputs once, exported safe outputs, and consumed only mapped values downstream
- **Chose:** Validated package and dry_run inputs once, exported safe outputs, and consumed only mapped values downstream
- **Reasoning:** Choice/boolean dispatch inputs must not be interpolated into shell source or paths.

---

## Chapters

### 1. Work
*Agent: default*

- Pinned every publish-workflow action to a full commit SHA and retained major-version comments: Pinned every publish-workflow action to a full commit SHA and retained major-version comments
- Validated package and dry_run inputs once, exported safe outputs, and consumed only mapped values downstream: Validated package and dry_run inputs once, exported safe outputs, and consumed only mapped values downstream
- Release hardening is implemented; action refs and dispatch input paths are immutable or validated, and automated validation is green.

---

## Artifacts

**Commits:** 3ffc97ee
**Files changed:** 3
