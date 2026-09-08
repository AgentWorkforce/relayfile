# Trajectory: Repair release snapshot review findings: tag collision, rerun attestation metadata, package attestation schema binding

> **Status:** ✅ Completed
> **Task:** PR-479
> **Confidence:** 94%
> **Started:** September 8, 2026 at 10:03 PM
> **Completed:** September 8, 2026 at 10:13 PM

---

## Summary

Repaired release snapshot provenance: automatic tag collisions fail before package jobs, exact reruns preserve immutable tag run metadata through child/top-level attestations, and package attestations require complete canonical fields and exact source/run identity. Added adversarial regressions; 68 release tests and static gates pass.

**Approach:** Standard approach

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
*Agent: default*

- Preserve immutable tag producer metadata across exact same-workflow reruns: Preserve immutable tag producer metadata across exact same-workflow reruns
- Reject automatic version targets that already have an untrusted tag: Reject automatic version targets that already have an untrusted tag
- Validate complete package child attestations before composition: Validate complete package child attestations before composition
