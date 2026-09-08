# Trajectory: Independent exact-head release review for Relayfile PR #479

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** September 8, 2026 at 10:53 PM
> **Completed:** September 9, 2026 at 01:26 AM

---

## Summary

Closed PR #479 feedback at exact head: shared optional digest validators reject present malformed shasums before publish/reconciliation, with pack, registry, and post-publish regressions; restored trajectory records removed without compaction; qualified stale/self-reported historical evidence.

**Approach:** Standard approach

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
*Agent: default*

- Reject present non-null malformed npm shasums before reconciliation or attestation: Reject present non-null malformed npm shasums before reconciliation or attestation
- Restore the two removed release trajectory records instead of compacting: Restore the two removed release trajectory records instead of compacting
- Exact-head release regressions now reject empty and malformed shasums in pack, registry, post-publish, attestation, and baseline paths; trajectory provenance repair remains pending final validation.

---

## Artifacts

**Commits:** 601d162c, 1e5b2642, b0293d36, a78139cb, 18b1470f, aa6e89bc, be78ac61, 4a3000d2, b70f3528
**Files changed:** 27
