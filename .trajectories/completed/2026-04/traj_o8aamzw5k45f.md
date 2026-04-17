# Trajectory: Fix CLI default relayfile server and token status behavior

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** April 17, 2026 at 02:55 PM
> **Completed:** April 17, 2026 at 02:57 PM

---

## Summary

Updated the CLI built-in default server to https://api.relayfile.dev, refreshed stale docs/workflow references, and added a regression test for default server resolution.

**Approach:** Standard approach

---

## Key Decisions

### Use api.relayfile.dev as the CLI built-in default server
- **Chose:** Use api.relayfile.dev as the CLI built-in default server
- **Reasoning:** The hosted observer and SDK already use api.relayfile.dev, and the user expects bare relayfile commands to target that host when no server override or saved server is present.

---

## Chapters

### 1. Work
*Agent: default*

- Use api.relayfile.dev as the CLI built-in default server: Use api.relayfile.dev as the CLI built-in default server
