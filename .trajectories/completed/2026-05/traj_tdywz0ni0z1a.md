# Trajectory: Fix issue 132

> **Status:** ✅ Completed
> **Task:** #132
> **Confidence:** 82%
> **Started:** May 11, 2026 at 02:12 PM
> **Completed:** May 11, 2026 at 02:14 PM

---

## Summary

Fixed issue 132 by using Node's non-forcing COPYFILE_FICLONE flag for createMount initial project-to-mount copies, added a mocked copy-mode regression test, and documented same-filesystem reflink fallback behavior.

**Approach:** Standard approach

---

## Key Decisions

### Use COPYFILE_FICLONE only for initial mount copies
- **Chose:** Use COPYFILE_FICLONE only for initial mount copies
- **Reasoning:** Issue 132 targets createMount's project-to-mount mirror; sync-back and autosync behavior should remain unchanged unless separately scoped.

---

## Chapters

### 1. Work
*Agent: default*

- Use COPYFILE_FICLONE only for initial mount copies: Use COPYFILE_FICLONE only for initial mount copies
- Implemented COPYFILE_FICLONE for initial local-mount copies; focused tests and build pass, full suite is limited by local FSEvents failures and a pre-existing abort timing race.
