# Trajectory: Address PR 477 shutdown races from current reviews

> **Status:** ✅ Completed
> **Task:** relayfile#477
> **Confidence:** 93%
> **Started:** September 8, 2026 at 07:26 PM
> **Completed:** September 8, 2026 at 07:30 PM

---

## Summary

Prevented foreground --once readiness and retry from racing shutdown; corrected stale validation trajectory wording.

**Approach:** Standard approach

---

## Key Decisions

### Gate both foreground readiness acceptance and resumable restart on shutdown state
- **Chose:** Gate both foreground readiness acceptance and resumable restart on shutdown state
- **Reasoning:** stop() can begin while status() or retry backoff is pending; checking stopping at the side-effect boundaries prevents a stopped child from resolving ready or being respawned.

---

## Chapters

### 1. Work
*Agent: default*

- Gate both foreground readiness acceptance and resumable restart on shutdown state: Gate both foreground readiness acceptance and resumable restart on shutdown state
- Current reviewer races are fixed with deterministic stop-vs-probe and stop-vs-backoff regressions; focused 25/25 and full Node 22 SDK 293/293 pass.
