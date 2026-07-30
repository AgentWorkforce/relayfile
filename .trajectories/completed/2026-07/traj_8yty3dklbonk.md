# Trajectory: Classify TS-only multi-path launcher refusal

> **Status:** ✅ Completed
> **Task:** relayfile#386-ci
> **Confidence:** 98%
> **Started:** July 30, 2026 at 01:15 AM
> **Completed:** July 30, 2026 at 01:15 AM

---

## Summary

Declared the public TypeScript-only launcher refusal in SDK parity with the capability reason; contract, build, typecheck, and 235 SDK tests pass.

**Approach:** Standard approach

---

## Key Decisions

### Classify MountMultiPathUnsupportedError as TS-only within setup errors
- **Chose:** Classify MountMultiPathUnsupportedError as TS-only within setup errors
- **Reasoning:** Python exposes no mount launcher or env bag and its mount placeholders always raise ts_only_sdk_feature; no Go or Swift SDK launcher exists in this repo. Only the TypeScript launcher can pass RELAYFILE_MOUNT_PATHS_FILE to the daemon.

---

## Chapters

### 1. Work
*Agent: default*

- Classify MountMultiPathUnsupportedError as TS-only within setup errors: Classify MountMultiPathUnsupportedError as TS-only within setup errors
