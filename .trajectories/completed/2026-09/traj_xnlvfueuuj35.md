# Trajectory: Repair release attestation digest and package-set validation

> **Status:** ✅ Completed
> **Task:** relayfile-release-snapshot-fix-479
> **Confidence:** 95%
> **Started:** September 8, 2026 at 08:56 PM
> **Completed:** September 8, 2026 at 08:56 PM

---

## Summary

Require common exact package digests, reject unexpected package records, and add missing-package plus post-publish retry/conflict regressions.

**Approach:** Standard approach

---

## Key Decisions

### Require a shared exact digest and allowlisted package set in release attestations
- **Chose:** Require a shared exact digest and allowlisted package set in release attestations
- **Reasoning:** Without a common digest, integrity and shasum-only records are incomparable; without an allowlist, extra package records can be attested.

---

## Chapters

### 1. Work
*Agent: default*

- Require a shared exact digest and allowlisted package set in release attestations: Require a shared exact digest and allowlisted package set in release attestations

---

## Artifacts

**Commits:** 508be165, b5be0df1
**Files changed:** 4
