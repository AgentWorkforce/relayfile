# Trajectory: Historical decisions on release digest and provenance binding

> **Status:** ⚠️ Completed historical record; non-gating
> **Confidence:** 20% (no captured command, output, commit, changed-file, or test evidence)
> **Started:** September 8, 2026 at 09:48 PM
> **Completed:** September 8, 2026 at 09:48 PM

---

## Summary

Historical decision record only: chose strict SHA-512 SRI and SHA-1 validation and binding artifacts to annotated tag source, tree, run, and attempt metadata. The stored `1873c3cf` trace is an earlier PR ancestor, not the current head. This trajectory captured no command, output, commit, changed-file, or test evidence; all implementation and validation statements are self-reported, non-authoritative, and non-gating.

**Approach:** Historical decision record (non-gating)

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

_Agent: default_

- Bind validated artifacts to annotated tag run metadata
- Validate exact npm digest formats in both attestation builder and baseline validator
- Self-reported, non-gating session note: the agent stated that focused and full release suites passed. This trajectory captured no command, commit, changed-file, or test evidence.
