# Trajectory: Add conformance suite to CI for relayfile and relayfile-cloud

> **Status:** ✅ Completed
> **Confidence:** 86%
> **Started:** March 26, 2026 at 12:46 PM
> **Completed:** March 26, 2026 at 12:52 PM

---

## Summary

Patched relayfile and relayfile-cloud CI to run the shared conformance suite, validated the OSS binary handoff and cloud clone/install path, and reported the current 4-test Go conformance failure caveat.

**Approach:** Standard approach

---

## Key Decisions

### Reuse the go-build artifact in OSS CI by letting scripts/conformance.ts start a prebuilt relayfile binary via RELAYFILE_CONFORMANCE_BINARY
- **Chose:** Reuse the go-build artifact in OSS CI by letting scripts/conformance.ts start a prebuilt relayfile binary via RELAYFILE_CONFORMANCE_BINARY
- **Reasoning:** This keeps server lifecycle inside the harness while avoiding a redundant rebuild in the conformance job and honoring the go-build dependency.

---

## Chapters

### 1. Work
*Agent: default*

- Reuse the go-build artifact in OSS CI by letting scripts/conformance.ts start a prebuilt relayfile binary via RELAYFILE_CONFORMANCE_BINARY: Reuse the go-build artifact in OSS CI by letting scripts/conformance.ts start a prebuilt relayfile binary via RELAYFILE_CONFORMANCE_BINARY
- Both CI workflows are patched. Local validation proved the OSS job can run the suite from the prebuilt binary, but the current Go server still fails 4 conformance checks, so the new gate will be red until those behaviors are fixed.
