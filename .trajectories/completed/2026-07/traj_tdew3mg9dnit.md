# Trajectory: Reset explicit mount state identity on rehome

> **Status:** ✅ Completed
> **Task:** relayfile#379
> **Confidence:** 94%
> **Started:** July 30, 2026 at 01:30 AM
> **Completed:** July 30, 2026 at 01:32 AM

---

## Summary

Made rehome select fresh state instead of inheriting an explicit prior state file; regression uses invalid old state to prove it is not read; full vet and Go suite pass.

**Approach:** Standard approach

---

## Key Decisions

### Do not inherit an explicit state file during rehome
- **Chose:** Do not inherit an explicit state file during rehome
- **Reasoning:** An explicit state file is a fixed identity outside the local-root hash. Reusing it at a new root can falsely claim bootstrap completion for files that exist only in the old mirror. StateDir remains reusable because its state ID includes the new local root.

---

## Chapters

### 1. Work
*Agent: default*

- Do not inherit an explicit state file during rehome: Do not inherit an explicit state file during rehome
