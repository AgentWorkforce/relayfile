# Trajectory: Repair fresh review findings for relayfile#478 release snapshot fix

> **Status:** ✅ Completed
> **Task:** relayfile#478-review
> **Confidence:** 75%
> **Started:** September 8, 2026 at 06:19 PM
> **Completed:** September 8, 2026 at 06:25 PM

---

## Summary

Historical record only: this session ended at fb0361c1d2aab3b67ca2e69e4f15d3cae7940af6 and predates later exact-head PR #479 fixes through 601d162ccd2264d187982239dd383f4c637fe742. Self-reported and non-gating: the recorded implementation claims have no captured command/output evidence and are not validation or release approval.

**Approach:** Standard approach

---

## Key Decisions

### Moved custom release input values into step env and scoped GitHub permissions per job
- **Chose:** Moved custom release input values into step env and scoped GitHub permissions per job
- **Reasoning:** Direct expression interpolation could turn a custom version into shell source, while workflow-wide write permissions exceeded build and publish needs.

### Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- **Chose:** Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- **Reasoning:** A retry can regenerate a different commit object for the same intended release; proving the immutable source parent and exact release tree allows safe reuse without tagging un-attested code.

---

## Chapters

### 1. Work
*Agent: default*

- Moved custom release input values into step env and scoped GitHub permissions per job
- Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- Self-reported and non-gating: the session recorded the fresh review findings as repaired (tag-safe retries, exact binary set, fail-closed registry ambiguity, least-privilege permissions), but captured no command/output evidence and predates later exact-head fixes.

---

## Artifacts

**Commits:** fb0361c1
**Files changed:** 7
