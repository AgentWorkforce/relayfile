# Trajectory: Add CLI launch command for Relayfile observer

> **Status:** ✅ Completed
> **Confidence:** 93%
> **Started:** April 17, 2026 at 03:36 PM
> **Completed:** April 17, 2026 at 03:43 PM

---

## Summary

Added relayfile observer CLI launcher, runtime URL-fragment configuration for the hosted file observer, tests, docs, and ignored generated observer output.

**Approach:** Standard approach

---

## Key Decisions

### Launch hosted observer with URL fragment config
- **Chose:** Launch hosted observer with URL fragment config
- **Reasoning:** The hosted observer cannot rely on per-user build-time NEXT_PUBLIC values. The CLI can pass server, workspace, and token at launch time in the URL fragment so credentials are available to browser code without being sent to the observer host as query parameters.

---

## Chapters

### 1. Work
*Agent: default*

- Launch hosted observer with URL fragment config: Launch hosted observer with URL fragment config
