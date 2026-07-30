# Trajectory: Complete Unit B derived safety gate for scoped multi-path mounts

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 90%
> **Started:** July 30, 2026 at 05:02 AM
> **Completed:** July 30, 2026 at 05:58 AM

---

## Summary

Completed Unit B scoped multi-path CLI runtime with a derived safety gate: total input normalization, persisted topology, restart and lifecycle refusals, pre-mutation startup serialization, truthful background startup, infrastructure/content policy boundaries, TypeScript capability refusal, and focused plus full validation. Independent review found only the two deliberately deferred Unit C aggregate surfaces, confirming the stack merge constraint.

**Approach:** Standard approach

---

## Key Decisions

### Derived infrastructure exclusions, collision checks, and mount summaries from one policy table
- **Chose:** Derived infrastructure exclusions, collision checks, and mount summaries from one policy table
- **Reasoning:** Infrastructure completeness is load-bearing; one inventory prevents planner, runtime, and observability drift

### Kept portable folded identity for planning but used actual filesystem identity for runtime exclusions
- **Chose:** Kept portable folded identity for planning but used actual filesystem identity for runtime exclusions
- **Reasoning:** Portable configs must not collide on insensitive hosts, while case-distinct user content remains valid on sensitive filesystems

### Serialized CLI mount initialization and waited for detached child registration
- **Chose:** Serialized CLI mount initialization and waited for detached child registration
- **Reasoning:** Daemon discovery alone has a start race between read-only preflights and topology mutation

### Changed generated-content refusal remedies to move content or choose a new directory
- **Chose:** Changed generated-content refusal remedies to move content or choose a new directory
- **Reasoning:** --rehome is needed only when the registered mirror changes; requiring it for local content cleanup sends operators to an unnecessary migration

### Validated every resolved private state path before catalog persistence
- **Chose:** Validated every resolved private state path before catalog persistence
- **Reasoning:** A rejected state override must not poison the workspace record after the command reports failure

### Enforce incidental infrastructure exclusion in both sync directions and honor configured background PID files
- **Chose:** Enforce incidental infrastructure exclusion in both sync directions and honor configured background PID files
- **Reasoning:** Local-only exclusion still allowed remote hydration/delete to overwrite repository metadata; the background registration barrier must observe the same PID-file path the child writes or a valid daemon is killed.

### Keep rehome as a real escape and make startup locking independent of state storage
- **Chose:** Keep rehome as a real escape and make startup locking independent of state storage
- **Reasoning:** Malformed persisted scope is unsafe only at the same filesystem root; a fresh root must remain recoverable. Coordination must bind the mount-root identity across setup and mount without inheriting HOME or state override writability.

### Canonicalize existing path prefixes and isolate temp locks by user
- **Chose:** Canonicalize existing path prefixes and isolate temp locks by user
- **Reasoning:** A serialization boundary must use filesystem identity rather than spelling, including symlink aliases and not-yet-created descendants. Shared /tmp needs a per-user namespace so one operator cannot block all others.

### Use filesystem-aware infrastructure identity for runtime hydration and a validated trusted runtime/cache parent for mount-start locks
- **Chose:** Use filesystem-aware infrastructure identity for runtime hydration and a validated trusted runtime/cache parent for mount-start locks
- **Reasoning:** Portable folded identity is correct for planning but diverges from local scanning on case-sensitive filesystems; shared /tmp permits predictable namespace precreation. Runtime hydration now follows actual filesystem case behavior, while coordination rejects symlink, foreign-owned, or group/world-writable parents and falls back to a trusted per-user directory.

### Adjudicate final local review findings as Unit C dependencies, not Unit B regressions
- **Chose:** Adjudicate final local review findings as Unit C dependencies, not Unit B regressions
- **Reasoning:** The review identified aggregate writeback listing and catalog-root runtime status, the two operator surfaces already assigned to Unit C. This independently confirms that Unit B must not merge without C ready; Unit B does not absorb them because the approved stack exists to keep those aggregate consumers reviewable as their own unit.

---

## Chapters

### 1. Work
*Agent: default*

- Derived infrastructure exclusions, collision checks, and mount summaries from one policy table: Derived infrastructure exclusions, collision checks, and mount summaries from one policy table
- Kept portable folded identity for planning but used actual filesystem identity for runtime exclusions: Kept portable folded identity for planning but used actual filesystem identity for runtime exclusions
- Serialized CLI mount initialization and waited for detached child registration: Serialized CLI mount initialization and waited for detached child registration
- Changed generated-content refusal remedies to move content or choose a new directory: Changed generated-content refusal remedies to move content or choose a new directory
- Validated every resolved private state path before catalog persistence: Validated every resolved private state path before catalog persistence
- Enforce incidental infrastructure exclusion in both sync directions and honor configured background PID files: Enforce incidental infrastructure exclusion in both sync directions and honor configured background PID files
- Keep rehome as a real escape and make startup locking independent of state storage: Keep rehome as a real escape and make startup locking independent of state storage
- Canonicalize existing path prefixes and isolate temp locks by user: Canonicalize existing path prefixes and isolate temp locks by user
- Five local review rounds converged on boundary mismatches rather than feature logic: bidirectional infrastructure exclusion, configured PID observation, recoverable rehome, state-independent serialized startup, filesystem identity, and live-child readiness. C remains untouched and B remains held.
- Use filesystem-aware infrastructure identity for runtime hydration and a validated trusted runtime/cache parent for mount-start locks: Use filesystem-aware infrastructure identity for runtime hydration and a validated trusted runtime/cache parent for mount-start locks
- Adjudicate final local review findings as Unit C dependencies, not Unit B regressions: Adjudicate final local review findings as Unit C dependencies, not Unit B regressions
