# Trajectory: Add .relayfile.acl enforcement to Go HTTP file handlers

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** March 27, 2026 at 10:02 AM
> **Completed:** March 27, 2026 at 10:03 AM

---

## Summary

Added ACL and scope matching tests in internal/httpapi

**Approach:** Standard approach

---

## Key Decisions

### Use relayauth-style scope matching for relayfile auth checks
- **Chose:** Use relayauth-style scope matching for relayfile auth checks
- **Reasoning:** Bearer tokens may grant relayfile capabilities as plane/resource/action/path scopes with wildcards or manage semantics, while handlers still request legacy resource:action scopes

---

## Chapters

### 1. Work
*Agent: default*

- Use relayauth-style scope matching for relayfile auth checks: Use relayauth-style scope matching for relayfile auth checks
