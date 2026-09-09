# Trajectory: Historical decisions on action pinning and dispatch input handling

> **Status:** ⚠️ Completed historical record; non-gating
> **Task:** #478
> **Confidence:** 20% (historical commit/file metadata, but no captured command or output evidence)
> **Started:** September 8, 2026 at 06:39 PM
> **Completed:** September 8, 2026 at 06:39 PM

---

## Summary

Historical decision record at refs `9e32618a` through `3ffc97ee`: the session reported immutable action pins, mapped package/dry_run inputs, and regressions. Those refs resolve to earlier PR ancestors and the stored commit/file list is historical implementation metadata only. No command or output evidence establishes tests, actionlint, or Node 22 results; validation claims are self-reported, non-authoritative, and non-gating.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Chose full-SHA action pinning with major-version comments

- **Chose:** Use full commit SHA pins and retain major-version comments
- **Reasoning:** The credential-bearing release workflow must not resolve mutable action tags at runtime.

### Chose one-time package/dry_run validation with mapped downstream outputs

- **Chose:** Validate once, export safe outputs, and consume only mapped values downstream
- **Reasoning:** Choice/boolean dispatch inputs must not be interpolated into shell source or paths.

---

## Chapters

### 1. Work

_Agent: default_

- Chose full-SHA action pinning with major-version comments
- Chose one-time package/dry_run validation with mapped downstream outputs
- Self-reported, non-gating session note: the agent stated that action refs and dispatch input paths were hardened. Historical commit/file metadata exists, but the record captured no command or output evidence and does not establish test, actionlint, or Node 22 results.

---

## Historical metadata (non-authoritative)

**Commits:** 3ffc97ee
**Files changed:** 3
