# Trajectory: Harden release workflow action pinning and dispatch input handling

> **Status:** ✅ Completed
> **Task:** #478
> **Confidence:** 95%
> **Started:** September 8, 2026 at 06:39 PM
> **Completed:** September 8, 2026 at 06:39 PM

---

## Summary

Pinned all publish workflow actions to immutable commit SHAs, validated and mapped package/dry_run inputs before shell use, and added regression tests. Any validation statement in this record is self-reported and non-gating: it captured no command output or test/actionlint/Node22 evidence; its commit and file metadata support implementation statements only.

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

- Pinned every publish-workflow action to a full commit SHA and retained major-version comments
- Validated package and dry_run inputs once, exported safe outputs, and consumed only mapped values downstream
- Release hardening is implemented; action refs and dispatch input paths are immutable or validated. This record does not establish test, actionlint, or Node 22 validation results.

---

## Artifacts

**Commits:** 3ffc97ee
**Files changed:** 3
