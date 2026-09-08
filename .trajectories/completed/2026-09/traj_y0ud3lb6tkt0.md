# Trajectory: Harden release CLI entrypoints and bounded registry propagation retries

> **Status:** ✅ Completed
> **Confidence:** 94%
> **Started:** September 8, 2026 at 10:45 PM
> **Completed:** September 8, 2026 at 10:50 PM

---

## Summary

Hardened release CLI entry recognition, propagation retry bounds, provenance fixture isolation, and portable trajectory metadata; release suite passed twice.

**Approach:** Standard approach

---

## Key Decisions

### Accepted entrypoint, retry-budget, fixture-isolation, and portable-trajectory repairs
- **Chose:** Accepted entrypoint, retry-budget, fixture-isolation, and portable-trajectory repairs
- **Reasoning:** Spaced paths can differ from Node's canonical module path; package retries were unbounded; shallow fixture overrides confounded negative provenance tests; and PR-added trajectory metadata contained checkout-specific paths.

---

## Chapters

### 1. Work
*Agent: default*

- Accepted entrypoint, retry-budget, fixture-isolation, and portable-trajectory repairs: Accepted entrypoint, retry-budget, fixture-isolation, and portable-trajectory repairs
- Added real spaced-path CLI tests, bounded registry retry delays to 30 seconds each and five minutes cumulative, deep-merged negative fixtures, and normalized PR trajectory metadata. The release suite passed twice.
