# Trajectory: Fix writeback pending endpoint to exclude acknowledged items

> **Status:** ✅ Completed
> **Confidence:** 93%
> **Started:** March 26, 2026 at 11:49 AM
> **Completed:** March 26, 2026 at 11:51 AM

---

## Summary

Fixed pending writeback filtering so acknowledged/succeeded ops are excluded from /writeback/pending, and added an HTTP regression test covering ack plus post-ack pending visibility.

**Approach:** Standard approach

---

## Key Decisions

### Filter pending writebacks by operation status
- **Chose:** Filter pending writebacks by operation status
- **Reasoning:** The queue snapshot retains acknowledged items, so the pending endpoint must cross-check workspace op state and only expose writebacks still actionable by external consumers (pending/running).

---

## Chapters

### 1. Work
*Agent: default*

- Filter pending writebacks by operation status: Filter pending writebacks by operation status
