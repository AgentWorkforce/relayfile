# Trajectory: Prove bootstrap-complete full re-pull preserves snapshot files after agent edit

> **Status:** ✅ Completed
> **Task:** Agent37 GA mount no-deletion proof
> **Confidence:** 95%
> **Started:** October 4, 2026 at 08:50 AM
> **Completed:** October 4, 2026 at 09:07 AM

---

## Summary

Added the exact Agent37 post-bootstrap snapshot regression: an agent edit followed by one ExportFiles JSON full re-pull preserves all untouched files and creates no delete tombstones; a two-observation incomplete-export control proves the deletion consequence.

**Approach:** Standard approach

---

## Chapters

### 1. Work
*Agent: default*

- Use an ExportFiles-only fake for the snapshot-backed mount so the regression reaches the post-bootstrap format=json branch instead of the GitHub tar-seed interface; pair the complete export assertion with two advancing incomplete snapshots to prove the tombstone delete consequence.: Use an ExportFiles-only fake for the snapshot-backed mount so the regression reaches the post-bootstrap format=json branch instead of the GitHub tar-seed interface; pair the complete export assertion with two advancing incomplete snapshots to prove the tombstone delete consequence.
