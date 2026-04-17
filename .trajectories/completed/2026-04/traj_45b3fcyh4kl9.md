# Trajectory: Fix observer default URL to live router path

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** April 17, 2026 at 03:50 PM
> **Completed:** April 17, 2026 at 03:53 PM

---

## Summary

Updated relayfile observer to default to https://agentrelay.com/observer/file, added a CLI regression test for the hosted router path, and removed stale files.relayfile.dev route references from observer config/templates.

**Approach:** Standard approach

---

## Key Decisions

### Default observer launch to agentrelay.com router path
- **Chose:** Default observer launch to agentrelay.com router path
- **Reasoning:** files.relayfile.dev is not live; cloud routes the file observer at /observer/file on agentrelay.com.

---

## Chapters

### 1. Work
*Agent: default*

- Default observer launch to agentrelay.com router path: Default observer launch to agentrelay.com router path
- Observer default now matches the live agentrelay.com router path; stale files.relayfile.dev config was removed from package and workflow templates.
