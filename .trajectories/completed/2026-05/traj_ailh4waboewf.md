# Trajectory: Design relayfile low-friction cloud login and integration mount flow

> **Status:** ✅ Completed
> **Confidence:** 86%
> **Started:** May 2, 2026 at 01:39 AM
> **Completed:** May 2, 2026 at 01:45 AM

---

## Summary

Added a guided relayfile setup path: bare relayfile now signs into Relayfile Cloud, creates and joins a workspace, opens a hosted integration connect session, waits for readiness, stores credentials/catalog state, and starts the existing mount sync loop. Documented the flow and covered it with CLI tests; go test ./... passes.

**Approach:** Standard approach

---

## Key Decisions

### Add guided setup as bare relayfile and relayfile setup while preserving explicit subcommands
- **Chose:** Add guided setup as bare relayfile and relayfile setup while preserving explicit subcommands
- **Reasoning:** The desired UX is low-friction for humans, but existing token-first subcommands are useful for CI and tests; a guided default layers on top without breaking scriptable surfaces.

---

## Chapters

### 1. Work
*Agent: default*

- Add guided setup as bare relayfile and relayfile setup while preserving explicit subcommands: Add guided setup as bare relayfile and relayfile setup while preserving explicit subcommands
- Guided CLI path now mirrors the SDK cloud setup sequence: browser/cloud token, workspace create/join, Nango connect session, then existing mount sync. Focused CLI tests and all Go tests pass.
