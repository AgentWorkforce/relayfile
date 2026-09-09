# Trajectory: Historical decisions on cryptographic release-baseline provenance

> **Status:** ⚠️ Completed historical record; non-gating
> **Task:** PR #479 candidate 925f617ff2038fabf871e26416738f13083b5450
> **Confidence:** 20% (historical commit/file metadata, but no captured command or output evidence)
> **Started:** September 8, 2026 at 08:34 PM
> **Completed:** September 8, 2026 at 08:35 PM

---

## Summary

Historical decision record: the session reported cryptographic artifact attestations, exact source/tag-tree binding, same-workflow recovery, and related regressions at historical refs `925f617f` through `73840800`. Those refs resolve to earlier PR ancestors, but the stored commit/file list is historical implementation metadata only. No command or output evidence substantiates the stated Node 22 runs, so all validation claims are self-reported, non-authoritative, and non-gating.

**Approach:** Historical decision record (non-gating)

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

_Agent: default_

- Require GitHub artifact attestation before accepting any release-tag baseline: Require GitHub artifact attestation before accepting any release-tag baseline
- Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata: Permit same-workflow rerun recovery only through the exact run artifact and tag source/tree/run metadata
- Self-reported, non-gating session note: the agent stated that forged sibling tags were rejected, valid stale releases advanced, and same-run reruns recovered from the workflow artifact. No command or output evidence was captured.

---

## Historical metadata (non-authoritative)

**Commits:** 73840800
**Files changed:** 7
