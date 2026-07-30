# Trajectory: Ship scoped multi-path CLI runtime with explicit refusal boundaries

> **Status:** ✅ Completed
> **Task:** relayfile#379-unit-b
> **Confidence:** 90%
> **Started:** July 30, 2026 at 12:44 AM
> **Completed:** July 30, 2026 at 01:00 AM

---

## Summary

Implemented Unit B scoped multi-path CLI runtime with write-ahead topology persistence and three explicit refusal boundaries: existing exact state cannot become scoped in place, scoped clobber reset refuses until transactional recovery exists, and the singular TypeScript launcher rejects multi-path env before side effects. Added lifecycle, restart, no-side-effect, and TS coverage plus migration docs.

**Approach:** Standard approach

---

## Key Decisions

### Legacy blank layout continues only as exact; scoped migration refuses in place
- **Chose:** Legacy blank layout continues only as exact; scoped migration refuses in place
- **Reasoning:** Pre-layout records were created when exact was the only supported topology, so exact restarts remain compatible. Blank cannot establish scoped topology; enabling scoped at the same LOCAL_DIR refuses and requires a new LOCAL_DIR with --rehome.

### Reject TypeScript paths-file configuration before launcher side effects
- **Chose:** Reject TypeScript paths-file configuration before launcher side effects
- **Reasoning:** The exported low-level env accepts RELAYFILE_MOUNT_PATHS_FILE, but the TS handle models and observes one remotePath. Refusal prevents a multi-root process from appearing singly observable.

### Persist resolved mount topology before mirror initialization
- **Chose:** Persist resolved mount topology before mirror initialization
- **Reasoning:** No catalog epoch marker exists. Writing the nonblank layout and allowlist after non-mutating preflight but before mkdir/spawn makes it impossible for a current scoped writer to leave child roots behind a blank record; blank remains safe to interpret as historical exact-only or current pre-mount state.

### Refuse legacy scoped migration only when local mount state exists
- **Chose:** Refuse legacy scoped migration only when local mount state exists
- **Reasoning:** Blank catalog records also come from current setup before the first mount. The safety fact is persisted exact mount state that scoping would orphan, not blankness itself; unmounted blank records may start scoped, while blank records with public or legacy state must rehome.

---

## Chapters

### 1. Work
*Agent: default*

- Legacy blank layout continues only as exact; scoped migration refuses in place: Legacy blank layout continues only as exact; scoped migration refuses in place
- Reject TypeScript paths-file configuration before launcher side effects: Reject TypeScript paths-file configuration before launcher side effects
- Unit B now has a finite refusal boundary: persisted multi-path runtime, legacy blank-to-scoped migration refusal, transactional scoped-reset refusal, and TypeScript low-level multi-path refusal. Focused Go and TS coverage pass; full validation is running.
- Persist resolved mount topology before mirror initialization: Persist resolved mount topology before mirror initialization
- Refuse legacy scoped migration only when local mount state exists: Refuse legacy scoped migration only when local mount state exists
