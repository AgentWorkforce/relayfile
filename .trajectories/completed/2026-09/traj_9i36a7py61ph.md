# Trajectory: Historical decisions on release tag and attestation safeguards

> **Status:** ⚠️ Completed historical record; non-gating
> **Task:** PR-479
> **Confidence:** 20% (no captured command, output, commit, changed-file, or test evidence)
> **Started:** September 8, 2026 at 10:03 PM
> **Completed:** September 8, 2026 at 10:13 PM

---

## Summary

Historical decision record only: chose to reject untrusted automatic tag collisions before package jobs, preserve immutable tag producer metadata across exact reruns, and validate complete child attestations before composition. The stored `8afcd036` trace is an earlier PR ancestor, not the current head. This trajectory captured no command, output, commit, changed-file, or test evidence; all implementation statements are self-reported, non-authoritative, and non-gating.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Preserve immutable tag producer metadata across exact same-workflow reruns

- **Chose:** Preserve immutable tag producer metadata across exact same-workflow reruns
- **Reasoning:** The tag records the original run and attempt; regenerated package and release attestations must use that identity so future baseline validation remains valid.

### Reject automatic version targets that already have an untrusted tag

- **Chose:** Reject automatic version targets that already have an untrusted tag
- **Reasoning:** Tag preparation occurs after publication, so collision rejection must happen in the build/version step before any package job.

### Validate complete package child attestations before composition

- **Chose:** Validate complete package child attestations before composition
- **Reasoning:** Top-level release provenance is only trustworthy when every package child has canonical digests, complete fields, and exact source/run identity.

---

## Chapters

### 1. Work

_Agent: default_

- Preserve immutable tag producer metadata across exact same-workflow reruns
- Reject automatic version targets that already have an untrusted tag
- Validate complete package child attestations before composition
