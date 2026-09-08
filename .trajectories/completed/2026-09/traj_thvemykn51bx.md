# Trajectory: Repair release attestation digest and provenance binding

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** September 8, 2026 at 09:48 PM
> **Completed:** September 8, 2026 at 09:48 PM

---

## Summary

Strictly validated SHA-512 SRI/SHA-1 digests in release attestation builder and baseline validator; bound artifacts to annotated tag source/tree/run/attempt metadata; added regressions and passed 64-test Node22 release suite plus actionlint/Prettier/diff checks.

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

- Bind validated artifacts to annotated tag run metadata: Bind validated artifacts to annotated tag run metadata
- Validate exact npm digest formats in both attestation builder and baseline validator: Validate exact npm digest formats in both attestation builder and baseline validator
- Focused and full release suites pass with metadata and malformed-digest regressions; final gates remain before commit.
