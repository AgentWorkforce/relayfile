# Trajectory: Fix Cloud PR 511 Next build

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** May 9, 2026 at 10:18 PM
> **Completed:** May 9, 2026 at 10:20 PM

---

## Summary

Fixed Cloud PR 511 Next.js build failure by making the workflow schedule store lazy so route imports no longer initialize the DB/SST resources during next build page-data collection. Verified local web build, typecheck, relaycron tests, and workflow schedule request tests, then pushed 07b43082.

**Approach:** Standard approach
