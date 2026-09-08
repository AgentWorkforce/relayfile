# Trajectory: Repair release attestation digest and provenance binding

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** September 8, 2026 at 09:48 PM
> **Completed:** September 8, 2026 at 09:48 PM

---

## Summary

Decision record only: chose strict SHA-512 SRI and SHA-1 validation and binding artifacts to annotated tag source, tree, run, and attempt metadata. This trajectory captured no command, commit, changed-file, or test evidence and is non-gating.

**Approach:** Standard approach

---

## Key Decisions

### Bind validated artifacts to annotated tag run metadata
- **Chose:** Bind validated artifacts to annotated tag run metadata
- **Reasoning:** A signed artifact must match the tag's source, tree, workflow run, and attempt before it can become a baseline or authorize same-run recovery.

### Validate exact npm digest formats in both attestation builder and baseline validator
- **Chose:** Validate exact npm digest formats in both attestation builder and baseline validator
- **Reasoning:** Equal arbitrary strings are not evidence of package identity; require canonical SHA-512 SRI or lowercase SHA-1.

---

## Chapters

### 1. Work
*Agent: default*

- Bind validated artifacts to annotated tag run metadata
- Validate exact npm digest formats in both attestation builder and baseline validator
- Self-reported, non-gating session note: the agent stated that focused and full release suites passed. This trajectory captured no command, commit, changed-file, or test evidence.
