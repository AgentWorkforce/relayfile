# Trajectory: PR #507 round-3 bugbot: supervisor install detach flags + go-run build path races

> **Status:** ✅ Completed
> **Task:** PR-507
> **Confidence:** 85%
> **Started:** September 18, 2026 at 06:58 AM
> **Completed:** September 18, 2026 at 07:07 AM

---

## Summary

Split relayfile listen's spec into filters vs process-model flags so supervisor install cannot embed --background/--daemonized in a unit (plus a runtime refusal), and made the go-run source fallback build to a private sibling published by atomic rename, with a Windows fallback and an age-bounded sweep.

**Approach:** Standard approach

---

## Key Decisions

### Split listenOptions into filters vs process-model; supervisor install advertises filters only and rejects the rest at runtime
- **Chose:** Split listenOptions into filters vs process-model; supervisor install advertises filters only and rejects the rest at runtime
- **Reasoning:** runListen acts on --background/--daemonized before connecting, so they parse fine but break any unit that embeds them; the surface and the binary both have to say no

### buildGoRunBinary stages to a private sibling and publishes by rename, with a returned-fallback and an age-bounded sweep
- **Chose:** buildGoRunBinary stages to a private sibling and publishes by rename, with a returned-fallback and an age-bounded sweep
- **Reasoning:** Shared path keyed by checkout alone is held open by long-running listen/mount; rename is atomic on POSIX and the fallback covers Windows, where replacing a running exe is impossible

---

## Chapters

### 1. Work
*Agent: default*

- Split listenOptions into filters vs process-model; supervisor install advertises filters only and rejects the rest at runtime: Split listenOptions into filters vs process-model; supervisor install advertises filters only and rejects the rest at runtime
- buildGoRunBinary stages to a private sibling and publishes by rename, with a returned-fallback and an age-bounded sweep: buildGoRunBinary stages to a private sibling and publishes by rename, with a returned-fallback and an age-bounded sweep

---

## Artifacts

**Commits:** 95bbfc3f
**Files changed:** 9
