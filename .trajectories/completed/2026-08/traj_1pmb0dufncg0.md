# Trajectory: Replace delegated credential shell-out with direct SDK mint

> **Status:** ✅ Completed
> **Task:** relayfile-sdk-remint-0815
> **Confidence:** 93%
> **Started:** August 15, 2026 at 11:38 PM
> **Completed:** August 15, 2026 at 11:42 PM

---

## Summary

Pinned delegated credential recovery to direct Cloud behavior with broker-shaped AGENT_RELAY_BIN regression arms, corrected misleading delegated-expiry guidance, and verified live mint plus daemon status.

**Approach:** Standard approach

---

## Key Decisions

### Kept origin/main's direct canonical-session and Cloud API mint path; added boundary-level two-arm regression coverage instead of renaming any binary variable
- **Chose:** Kept origin/main's direct canonical-session and Cloud API mint path; added boundary-level two-arm regression coverage instead of renaming any binary variable
- **Reasoning:** Commit 92c495e already removed the delegated re-mint shell-out. The remaining measurable gap was that the refresh-to-remint regression did not poison AGENT_RELAY_BIN, and delegated-expiry text still prescribed cloud login even when the session was healthy.

---

## Chapters

### 1. Work
*Agent: default*

- Kept origin/main's direct canonical-session and Cloud API mint path; added boundary-level two-arm regression coverage instead of renaming any binary variable: Kept origin/main's direct canonical-session and Cloud API mint path; added boundary-level two-arm regression coverage instead of renaming any binary variable
