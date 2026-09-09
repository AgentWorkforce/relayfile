# Trajectory: Historical decisions from an earlier relayfile#478 repair branch

> **Status:** ⚠️ Completed historical record; non-gating
> **Task:** relayfile#478-review
> **Confidence:** 20% (historical commit/file metadata, but no captured command or output evidence)
> **Started:** September 8, 2026 at 06:19 PM
> **Completed:** September 8, 2026 at 06:25 PM

---

## Summary

Historical decision record only: the stored refs resolve locally, but endRef `fb0361c1` is from an earlier divergent branch state and is not an ancestor of the current PR head. The reported commit/file list is non-authoritative historical metadata. The record captured no command or output evidence, so all implementation claims are self-reported, non-gating, and not validation or release approval.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Chose step-environment release inputs and per-job GitHub permissions

- **Chose:** Pass release inputs through step env and scope permissions per job
- **Reasoning:** Direct expression interpolation could turn a custom version into shell source, while workflow-wide write permissions exceeded build and publish needs.

### Chose attestation-before-tag and parent/tree equivalence for retries

- **Chose:** Generate attestation before remote tag push and require parent/tree equivalence for reuse
- **Reasoning:** A retry can regenerate a different commit object for the same intended release; proving the immutable source parent and exact release tree allows safe reuse without tagging un-attested code.

---

## Chapters

### 1. Work

_Agent: default_

- Chose step-environment release inputs and per-job GitHub permissions
- Chose attestation-before-tag and parent/tree equivalence for retries
- Self-reported and non-gating: the session stated that tag retries, binary selection, registry ambiguity, and permissions were repaired, but captured no command/output evidence and describes an earlier divergent branch state.

---

## Historical metadata (non-authoritative)

**Commits:** fb0361c1
**Files changed:** 7
