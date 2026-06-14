# Trajectory: Design simple agent workspace connect flow

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** May 1, 2026 at 04:58 PM
> **Completed:** May 1, 2026 at 05:06 PM

---

## Summary

Aligned setup client with the spec waitForConnection contract and SDK version header, then added the one-handle agent workspace path: connectNotion, waitForNotion, mountEnv, and agentInvite, with unit and packaged E2E coverage.

**Approach:** Standard approach

---

## Key Decisions

### Expose one-handle agent workspace helpers
- **Chose:** Expose one-handle agent workspace helpers
- **Reasoning:** The existing WorkspaceHandle already holds relayfile token, workspace metadata, and relaycast API key, so connectNotion/waitForNotion/mountEnv/agentInvite keep the easy path on the object agents already receive without introducing a second setup abstraction.

---

## Chapters

### 1. Work
*Agent: default*

- Expose one-handle agent workspace helpers: Expose one-handle agent workspace helpers
