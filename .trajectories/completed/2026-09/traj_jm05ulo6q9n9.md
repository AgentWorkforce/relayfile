# Trajectory: Repair Relayfile release baseline with cryptographic provenance attestation

> **Status:** ✅ Completed
> **Task:** PR #479 candidate 925f617ff2038fabf871e26416738f13083b5450
> **Confidence:** 92%
> **Started:** September 8, 2026 at 08:34 PM
> **Completed:** September 8, 2026 at 08:35 PM

---

## Summary

Added cryptographically verified GitHub artifact attestations for release baseline tags, exact source/tag-tree binding, same-workflow rerun artifact recovery, and regressions for forged exact-shape siblings and mismatched attestations. Node 22 release suite passed twice.

**Approach:** Standard approach

---

## Key Decisions

### Require GitHub artifact attestation before accepting any release-tag baseline
- **Chose:** Require GitHub artifact attestation before accepting any release-tag baseline
- **Reasoning:** Git topology, subject, allowlisted paths, and package versions can be forged by an annotated sibling tag; gh attestation verify constrained to the repository/workflow and source digest supplies provenance.

### Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata
- **Chose:** Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata
- **Reasoning:** A release asset may not exist before a rerun, but arbitrary sibling tags must never gain an artifact fallback.

---

## Chapters

### 1. Work
*Agent: default*

- Require GitHub artifact attestation before accepting any release-tag baseline: Require GitHub artifact attestation before accepting any release-tag baseline
- Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata: Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata
- The forged v99.99.99 exact-shape sibling is rejected without a valid signed attestation; valid stale releases advance and exact same-run reruns can recover from the immutable workflow artifact.

---

## Artifacts

**Commits:** 73840800
**Files changed:** 7
