# Trajectory: Migrate relayfile e2e and conformance scripts to RS256 local JWKS

> **Status:** ✅ Completed
> **Confidence:** 95%
> **Started:** April 24, 2026 at 11:06 AM
> **Completed:** April 24, 2026 at 11:10 AM

---

## Summary

Migrated relayfile e2e and conformance scripts from shared-secret JWTs to RS256 tokens backed by a startup-local JWKS server; both local script harnesses now pass RELAYAUTH_JWKS_URL to the spawned Go server and cleanly close the JWKS listener during teardown.

**Approach:** Standard approach

---

## Key Decisions

### Extracted shared RS256 local JWKS signer for script tests
- **Chose:** Extracted shared RS256 local JWKS signer for script tests
- **Reasoning:** Both e2e.ts and conformance.ts minted duplicated HS256 tokens; a small shared helper keeps RSA key generation, JWKS serving, kid calculation, and signing consistent without adding dependencies.

---

## Chapters

### 1. Work
*Agent: default*

- Extracted shared RS256 local JWKS signer for script tests: Extracted shared RS256 local JWKS signer for script tests
