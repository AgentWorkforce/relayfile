# Trajectory: Align release attestation digest validation with optional npm digest contract

> **Status:** ✅ Completed
> **Confidence:** 92%
> **Started:** September 8, 2026 at 10:37 PM
> **Completed:** September 8, 2026 at 10:39 PM

---

## Summary

Allowed canonical optional npm digests only when the local tarball and registry share one matching value; preserved malformed and incomparable digest refusal.

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

- Accepted optional digest validation repair: Accepted optional digest validation repair
- Aligned release attestation and baseline validation with the reconciliation contract; added integrity-only, shasum-only, and baseline regressions. Release suite is green.
