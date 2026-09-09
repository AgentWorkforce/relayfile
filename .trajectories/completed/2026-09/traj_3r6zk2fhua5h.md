# Trajectory: Historical decision record for optional npm digest validation

> **Status:** ⚠️ Completed historical record; non-gating
> **Confidence:** 20% (no captured command, output, commit, changed-file, or test evidence)
> **Started:** September 8, 2026 at 10:37 PM
> **Completed:** September 8, 2026 at 10:39 PM

---

## Summary

Historical decision record only: chose to accept an optional npm digest only when the local tarball and registry share one canonical matching value, while refusing malformed or incomparable digests. The stored `f811fd48` trace is an earlier PR ancestor, not the current head. This trajectory captured no command, output, commit, changed-file, or test evidence; all implementation and validation statements are self-reported, non-authoritative, and non-gating.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Chose the optional digest validation contract

- **Chose:** Require one canonical local/registry digest match and reject malformed or incomparable values
- **Reasoning:** Reconciliation safely accepts either canonical SHA-512 SRI or SHA-1 when local and registry share one; the later attestation gate incorrectly demanded both after publication.

---

## Chapters

### 1. Work

_Agent: default_

- Chose the optional digest validation contract
- Self-reported, non-gating session note: the agent stated that it aligned attestation and baseline validation with the reconciliation contract and observed the release suite passing. This trajectory captured no command, commit, or changed-file evidence.
