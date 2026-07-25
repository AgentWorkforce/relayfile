# Trajectory: Implement Phase 2 rebase fork HTTP and TypeScript SDK surfaces

> **Status:** ✅ Completed
> **Confidence:** 88%
> **Started:** July 25, 2026 at 01:05 PM
> **Completed:** July 25, 2026 at 01:07 PM

---

## Summary

Added the fork rebase HTTP endpoint and OpenAPI schemas, TypeScript SDK types/client method, typed ParentMovedError recovery, HTTP and SDK tests, and SDK parity classification; build, vet, contract, focused SDK tests, and typecheck pass, with listener/process tests explicitly blocked by sandbox policy

**Approach:** Standard approach

---

## Key Decisions

### Return RebaseFork conflicts as successful response data
- **Chose:** Return RebaseFork conflicts as successful response data
- **Reasoning:** Store semantics intentionally leave genuinely diverged overlay entries untouched; callers resolve by citing each reported liveRevision in a fork write or delete before commit

---

## Chapters

### 1. Work
*Agent: default*

- Return RebaseFork conflicts as successful response data: Return RebaseFork conflicts as successful response data
- HTTP, OpenAPI, SDK, typed parent_moved recovery, and tests are implemented; build, vet, contract, client tests, and typecheck pass, while full Go and SDK suites are limited only by sandbox loopback and process-inspection restrictions
