# Trajectory: Align release attestation digest validation with optional npm digest contract

> **Status:** ✅ Completed
> **Confidence:** 92%
> **Started:** September 8, 2026 at 10:37 PM
> **Completed:** September 8, 2026 at 10:39 PM

---

## Summary

Decision record only: chose to accept an optional npm digest only when the local tarball and registry share one canonical matching value, while refusing malformed or incomparable digests. This trajectory captured no command, commit, or changed-file evidence and is non-gating.

**Approach:** Standard approach

---

## Key Decisions

### Accepted optional digest validation repair
- **Chose:** Accepted optional digest validation repair
- **Reasoning:** Reconciliation safely accepts either canonical SHA-512 SRI or SHA-1 when local and registry share one; the later attestation gate incorrectly demanded both after publication.

---

## Chapters

### 1. Work
*Agent: default*

- Accepted optional digest validation repair
- Self-reported, non-gating session note: the agent stated that it aligned attestation and baseline validation with the reconciliation contract and observed the release suite passing. This trajectory captured no command, commit, or changed-file evidence.
