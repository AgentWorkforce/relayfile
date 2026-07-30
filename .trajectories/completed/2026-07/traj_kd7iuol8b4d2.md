# Trajectory: Reserve local Git metadata across scoped mount roots

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 94%
> **Started:** July 30, 2026 at 04:05 AM
> **Completed:** July 30, 2026 at 05:00 AM

---

## Summary

Closed scoped mount topology boundaries: explicit collision/runtime identities, immutable background allowlists, exact-vs-scoped artifact handling, provider-filter validation, setup artifact migration, and shared empty allowlist refusal

**Approach:** Standard approach

---

## Key Decisions

### Keep exact .git reserved at every Syncer topology boundary
- **Chose:** Keep exact .git reserved at every Syncer topology boundary
- **Reasoning:** Provider-specific catalog names become valid content below non-root scopes, but local .git objects, indexes, configs, and remote credentials must never enter sync or writeback; case-distinct .Git remains valid on case-sensitive filesystems.

### Carry scoped-child topology explicitly into Syncer and watcher
- **Chose:** Carry scoped-child topology explicitly into Syncer and watcher
- **Reasoning:** RemoteRoot cannot distinguish an exact non-root mount from a scoped child: exact mounts reserve generated catalog artifacts, while scoped children may contain the same names as provider data. An explicit topology bit makes the two states unambiguous.

### Reject heterogeneous multi-root mounts with one explicit provider filter
- **Chose:** Reject heterogeneous multi-root mounts with one explicit provider filter
- **Reasoning:** A blank filter lets each Syncer infer its provider; applying one explicit filter across other providers makes those scopes permanently stale, so refusal is safer than partial success.

### Clean only provably generated catalog artifacts before scoped mount
- **Chose:** Clean only provably generated catalog artifacts before scoped mount
- **Reasoning:** Setup can pre-create global digests and the activity-summary skill. Scoped mounts must not display them, but unknown or modified content cannot be deleted safely; validate the full trees first, remove only the exact generated skill and empty directories, otherwise refuse with --rehome guidance.

### Reject workspace root with scoped layout
- **Chose:** Reject workspace root with scoped layout
- **Reasoning:** The workspace root has no isolated child directory; treating it as ScopedChild removes catalog artifacts and relaxes exact-root reservations at the same filesystem root. Exact layout is the only honest topology for /.

### Give CLI and standalone daemon one explicit-empty paths-file guard
- **Chose:** Give CLI and standalone daemon one explicit-empty paths-file guard
- **Reasoning:** Both binaries normalize an absent allowlist to /, so a configured file with zero usable roots must be distinguished before fallback. Centralizing the guard in mountscope prevents either entry point from silently widening.

---

## Chapters

### 1. Work
*Agent: default*

- Keep exact .git reserved at every Syncer topology boundary: Keep exact .git reserved at every Syncer topology boundary
- Carry scoped-child topology explicitly into Syncer and watcher: Carry scoped-child topology explicitly into Syncer and watcher
- Reject heterogeneous multi-root mounts with one explicit provider filter: Reject heterogeneous multi-root mounts with one explicit provider filter
- Clean only provably generated catalog artifacts before scoped mount: Clean only provably generated catalog artifacts before scoped mount
- Reject workspace root with scoped layout: Reject workspace root with scoped layout
- Give CLI and standalone daemon one explicit-empty paths-file guard: Give CLI and standalone daemon one explicit-empty paths-file guard
