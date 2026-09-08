# Trajectory: Repair fresh review findings for relayfile#478 release snapshot fix

> **Status:** ✅ Completed
> **Task:** relayfile#478-review
> **Confidence:** 90%
> **Started:** September 8, 2026 at 06:19 PM
> **Completed:** September 8, 2026 at 06:25 PM

---

## Summary

Repaired fresh review findings: custom version is env-only, attestation precedes tag push, existing tags require exact source parent and release tree equivalence, binary attestations enforce the exact four mount plus six CLI set, registry E404 classification is fail-closed, and permissions are job-scoped. Commit fb0361c1d2aab3b67ca2e69e4f15d3cae7940af6.

**Approach:** Standard approach

---

## Key Decisions

### Moved custom release input values into step env and scoped GitHub permissions per job
- **Chose:** Moved custom release input values into step env and scoped GitHub permissions per job
- **Reasoning:** Direct expression interpolation could turn a custom version into shell source, while workflow-wide write permissions exceeded build and publish needs.

### Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- **Chose:** Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- **Reasoning:** A retry can regenerate a different commit object for the same intended release; proving the immutable source parent and exact release tree allows safe reuse without tagging un-attested code.

---

## Chapters

### 1. Work
*Agent: default*

- Moved custom release input values into step env and scoped GitHub permissions per job
- Generate attestation before remote tag push and reuse tags by parent/tree equivalence
- Fresh review findings are repaired: release retries are tag-safe, binaries are an exact validated set, registry ambiguity fails closed, and permissions are least-privilege.

---

## Artifacts

**Commits:** fb0361c1
**Files changed:** 7
