# Trajectory: Historical PR #479 repair decision record through 601d162c

> **Status:** ⚠️ Completed historical record; non-gating
> **Confidence:** 20% (file ranges captured, but no command or output evidence)
> **Started:** September 8, 2026 at 10:53 PM
> **Completed:** September 9, 2026 at 01:26 AM

---

## Summary

Historical decision record through stored endRef `601d162c`: the session reported optional-digest validation repairs, regressions, restored uncompacted trajectories, and provenance qualifications. Its stored start/end refs resolve locally but describe historical workspace states rather than the current PR head; the startRef is not an ancestor of this branch. The record captured file ranges but no command or output evidence, so its implementation and validation claims are self-reported, non-authoritative, and non-gating. It does not validate `3a1594a8` or any later repair commit.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Reject present non-null malformed npm shasums before reconciliation or attestation

- **Chose:** Reject present non-null malformed npm shasums before reconciliation or attestation
- **Reasoning:** The normalizers previously treated empty or non-string shasums as absent when integrity was valid, leaving a post-publish attestation failure window; shared null-or-valid validators now close all release paths.

### Restore the two removed release trajectory records instead of compacting

- **Chose:** Restore the two removed release trajectory records instead of compacting
- **Reasoning:** Git history shows b70f3528 removed their index entries without a deliberate compaction, and preserved JSON/Markdown originals are available locally.

---

## Chapters

### 1. Work

_Agent: default_

- Reject present non-null malformed npm shasums before reconciliation or attestation: Reject present non-null malformed npm shasums before reconciliation or attestation
- Restore the two removed release trajectory records instead of compacting: Restore the two removed release trajectory records instead of compacting
- Self-reported, non-gating session note: the agent stated that digest regressions and trajectory provenance repairs were in place through its historical workspace state. The record captured no command or output evidence and does not validate `3a1594a8` or any later PR head.

---

## Historical metadata (non-authoritative)

**Commits:** 601d162c, 1e5b2642, b0293d36, a78139cb, 18b1470f, aa6e89bc, be78ac61, 4a3000d2, b70f3528
**Files changed:** 27
