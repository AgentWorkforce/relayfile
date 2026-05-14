# Trajectory: Support -h help flag for every relayfile command

> **Status:** ✅ Completed
> **Confidence:** 92%
> **Started:** May 14, 2026 at 10:26 AM
> **Completed:** May 14, 2026 at 10:29 AM

---

## Summary

Added centralized -h/--help handling for relayfile root commands, command groups, subcommands, and aliases, with regression coverage for every CLI help path.

**Approach:** Standard approach

---

## Key Decisions

### Intercept help flags at the top-level dispatcher
- **Chose:** Intercept help flags at the top-level dispatcher
- **Reasoning:** This lets -h/--help work before subcommand validation, credential loading, or network calls, and keeps aliases consistent.

---

## Chapters

### 1. Work
*Agent: default*

- Intercept help flags at the top-level dispatcher: Intercept help flags at the top-level dispatcher
- Help handling is centralized and focused CLI/manual checks pass; broad Go tests are running before commit.
