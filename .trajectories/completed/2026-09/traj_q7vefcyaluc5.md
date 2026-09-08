# Trajectory: Fix pre-loop cancellation-vs-completion race in --once bootstrap resume; add CHANGELOG entry

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** September 8, 2026 at 04:20 PM
> **Completed:** September 8, 2026 at 04:21 PM

---

## Summary

Fixed the pre-loop cancellation-vs-completion race in finishInitialBootstrap by checking the on-disk checkpoint before rootCtx.Err()/lastCycleErr, matching the resume loop's existing precedence; added an adversarial test and a CHANGELOG entry for --once resuming after a timeout yield

**Approach:** Standard approach

---

## Key Decisions

### Reordered finishInitialBootstrap's pre-loop checks to check the on-disk checkpoint (!state.inProgress) before rootCtx.Err()/lastCycleErr, mirroring the resume loop's existing checkpoint-first precedence
- **Chose:** Reordered finishInitialBootstrap's pre-loop checks to check the on-disk checkpoint (!state.inProgress) before rootCtx.Err()/lastCycleErr, mirroring the resume loop's existing checkpoint-first precedence
- **Reasoning:** A SIGTERM/cancellation landing exactly as a fresh (not alreadyBootstrapped) bootstrap finished in its own first cycle was reported as an incomplete bootstrap even though the persisted checkpoint was genuinely complete, because rootCtx.Err() was checked before the checkpoint state. A real (non-yielded) cycle failure is still reported when the checkpoint reads not-in-progress, since that can mean the cycle failed before any bootstrap started

---

## Chapters

### 1. Work
*Agent: default*

- Reordered finishInitialBootstrap's pre-loop checks to check the on-disk checkpoint (!state.inProgress) before rootCtx.Err()/lastCycleErr, mirroring the resume loop's existing checkpoint-first precedence: Reordered finishInitialBootstrap's pre-loop checks to check the on-disk checkpoint (!state.inProgress) before rootCtx.Err()/lastCycleErr, mirroring the resume loop's existing checkpoint-first precedence
