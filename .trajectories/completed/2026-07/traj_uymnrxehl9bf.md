# Trajectory: Address Unit B exact GitHub review findings

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 02:22 AM
> **Completed:** July 30, 2026 at 02:34 AM

---

## Summary

Closed Unit B exact-head findings with filesystem-identity transition guards, comprehensive legacy private-state detection, one-shot competing-daemon refusal, a shared Unicode-aware reserved-root boundary, and honest scoped digest absence; full contract, vet, and Go tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Compare mount transition roots by filesystem identity
- **Chose:** Compare mount transition roots by filesystem identity
- **Reasoning:** Path spelling is not identity on symlinked, case-insensitive, or normalization-insensitive filesystems; refusing only exact spellings lets in-place scope and layout transitions bypass the rehome boundary.

### Derive local reserved-root checks from one mountscope owner
- **Chose:** Derive local reserved-root checks from one mountscope owner
- **Reasoning:** Mount planning, filesystem watching, and writeback scans were three handwritten copies of a security boundary. Shared constants plus one Unicode case-folded NFC identity predicate keep runtime directories and ignored local trees aligned.

### Do not auto-pull workspace-global digests across a scoped allowlist
- **Chose:** Do not auto-pull workspace-global digests across a scoped allowlist
- **Reasoning:** Canonical digests can contain events from providers outside the requested roots. A common-root digest sync would make a scoped mount disclose out-of-allowlist summaries; honest absence or explicit opt-in is safer than hidden widening.

---

## Chapters

### 1. Work
*Agent: default*

- Compare mount transition roots by filesystem identity: Compare mount transition roots by filesystem identity
- Derive local reserved-root checks from one mountscope owner: Derive local reserved-root checks from one mountscope owner
- Do not auto-pull workspace-global digests across a scoped allowlist: Do not auto-pull workspace-global digests across a scoped allowlist
- The exact-head review produced a bounded five-part correction: topology changes now compare filesystem identity, one-shot mounts cannot race a daemon, legacy private state cannot be missed when its remote root is unknowable, reserved local roots have one Unicode-aware owner, and scoped topology withdraws the false digest-currency promise. Full Go validation is green.
