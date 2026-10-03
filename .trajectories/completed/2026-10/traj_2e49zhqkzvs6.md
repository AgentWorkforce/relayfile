# Trajectory: Measure and accelerate Relayfile mount bulk bootstrap

> **Status:** ✅ Completed
> **Task:** relayfile mount read-side bootstrap performance
> **Confidence:** 96%
> **Started:** October 2, 2026 at 11:25 PM
> **Completed:** October 3, 2026 at 12:25 AM

---

## Summary

Made GitHub clone bootstrap cursor resolution newest-first and revision-safe. Self-reported, non-gating DEV measurements observed a reduction from 328 event requests to one; regression coverage and repository CI are the gating evidence.

**Approach:** Measured released 0.10.70 on DEV, isolated the event-feed scan, implemented an optional descending-event extension with legacy fallback, matched the sentinel revision when available, then measured a cold and warm run with the built client.

---

## Key Decisions

### Resolve the GitHub clone sentinel cursor newest-first and match its exact file revision when available
- **Chose:** Resolve the GitHub clone sentinel cursor newest-first and match its exact file revision when available
- **Rejected:** Increase the watchdog, Keep the oldest-first scan, Use the latest event without revision matching
- **Reasoning:** The released mount scanned 328 retained event pages oldest-first and hit the 90-second bootstrap watchdog before tar export. Matching clone.json by revision prevents a concurrent newer clone from advancing the checkpoint beyond the manifest being materialized.

---

## Chapters

### 1. Work
*Agent: default*

- Resolve the GitHub clone sentinel cursor newest-first and match its exact file revision when available: Resolve the GitHub clone sentinel cursor newest-first and match its exact file revision when available
- DEV measurement separated the two bottlenecks: the cursor resolver failed before tar in 93.506 seconds; the fixed resolver uses one event request, while the unchanged serial server tar still consumes about 760 seconds. The client change is intentionally limited to cursor lookup and preserves compatibility with older/custom clients.
