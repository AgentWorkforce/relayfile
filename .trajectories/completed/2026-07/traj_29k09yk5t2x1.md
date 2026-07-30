# Trajectory: Close reserved-root and inherited-env mount boundaries

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 95%
> **Started:** July 30, 2026 at 01:50 AM
> **Completed:** July 30, 2026 at 01:52 AM

---

## Summary

Rejected scoped roots overlapping reserved local namespaces and made the TypeScript launcher validate its effective merged environment before side effects; contract, build, 236 SDK tests, vet, and full Go suite pass.

**Approach:** Standard approach

---

## Key Decisions

### Reject scoped roots that occupy reserved local namespaces
- **Chose:** Reject scoped roots that occupy reserved local namespaces
- **Reasoning:** Scoped mapping strips the remote prefix and places the child beneath the same local path used for runtime, digest, skill, VCS, or dependency artifacts. Rejecting in the shared planner makes the collision unrepresentable for both CLI and standalone daemon.

### Use one effective environment for launcher validation and spawn
- **Chose:** Use one effective environment for launcher validation and spawn
- **Reasoning:** Validating input.env while spawning a process.env merge creates two interpretations of the same launch. The launcher now derives topology and validates multi-path refusal from the exact env passed to the child.

---

## Chapters

### 1. Work
*Agent: default*

- Reject scoped roots that occupy reserved local namespaces: Reject scoped roots that occupy reserved local namespaces
- Use one effective environment for launcher validation and spawn: Use one effective environment for launcher validation and spawn
