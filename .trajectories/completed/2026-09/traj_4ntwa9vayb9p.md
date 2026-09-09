# Trajectory: Repair PR #479 pre-publish integrity validation

> **Status:** ✅ Completed
> **Task:** PR-479
> **Confidence:** 20%
> **Started:** September 9, 2026 at 02:55 AM
> **Completed:** September 9, 2026 at 02:58 AM

---

## Summary

Historical self-report of the decision to require canonical SHA-512 npm pack integrity while retaining optional SHA-1 shasums. This trajectory captured no command, output, commit, changed-file, or test evidence, so all implementation and validation statements are non-authoritative and non-gating.

**Approach:** Historical decision record (non-gating)

---

## Key Decisions

### Require canonical SHA-512 integrity at the npm pack boundary

- **Chose:** Require canonical SHA-512 integrity at the npm pack boundary
- **Reasoning:** A SHA-1-only local record can reach npm publish when the registry version is absent, then fail only during post-publish comparison. Keeping shasum optional preserves npm 11 integrity-only output while ensuring publish identity is canonical before mutation.

---

## Chapters

### 1. Work

_Agent: default_

- Require canonical SHA-512 integrity at the npm pack boundary: Require canonical SHA-512 integrity at the npm pack boundary
