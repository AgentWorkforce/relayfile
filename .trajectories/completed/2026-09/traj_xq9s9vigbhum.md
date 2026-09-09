# Trajectory: Fix relayfile SDK package version resolution in compiled Bun binaries

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** September 10, 2026 at 12:35 AM
> **Completed:** September 10, 2026 at 12:38 AM

---

## Summary

Replaced runtime createRequire package lookup in @relayfile/sdk workspace-mount with a bundled package.json import, added a compiled Bun regression, and verified baseline failure versus fixed execution.

**Approach:** Standard approach

---

## Key Decisions

### Bundle package metadata through a JSON import
- **Chose:** Bundle package metadata through a JSON import
- **Reasoning:** createRequire('../package.json') resolves relative to Bun's virtual compiled module path and fails; a static JSON import is bundled into the executable while preserving the package manifest version as the release URL source.

---

## Chapters

### 1. Work
*Agent: default*

- Bundle package metadata through a JSON import: Bundle package metadata through a JSON import
