# Trajectory: Address PR 46 feedback and conflicts

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** April 17, 2026 at 05:19 PM
> **Completed:** April 17, 2026 at 05:22 PM

---

## Summary

Resolved PR 46 against main, accepted the mount polling changes from PR 45, fixed observer launch URLs to use a raw single-encoded fragment, and added browser-style fragment parsing assertions.

**Approach:** Standard approach

---

## Key Decisions

### Build observer URLs with a raw single-encoded fragment
- **Chose:** Build observer URLs with a raw single-encoded fragment
- **Reasoning:** Go re-encodes URL.Fragment when String() is called, so assigning fragment.Encode() there double-encodes browser parameters.

---

## Chapters

### 1. Work
*Agent: default*

- Build observer URLs with a raw single-encoded fragment: Build observer URLs with a raw single-encoded fragment
