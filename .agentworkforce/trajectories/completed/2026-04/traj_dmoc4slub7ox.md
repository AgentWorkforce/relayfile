# Trajectory: Fix SDK setup workflow evidence path

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** April 30, 2026 at 10:33 AM
> **Completed:** April 30, 2026 at 10:34 AM

---

## Summary

Fixed capture-final-evidence in workflows/062-sdk-setup-client.ts by using an absolute evidence file path and git -C instead of changing directories inside the redirected block; replayed the evidence command and typechecked the workflow.

**Approach:** Standard approach

---

## Key Decisions

### Use absolute evidence file path
- **Chose:** Use absolute evidence file path
- **Reasoning:** capture-final-evidence changed into ../cloud inside its output block, so the final grep resolved docs/evidence relative to the cloud repo. The fix writes and checks the relayfile evidence path absolutely and uses git -C for both repos.

---

## Chapters

### 1. Work
*Agent: default*

- Use absolute evidence file path: Use absolute evidence file path
