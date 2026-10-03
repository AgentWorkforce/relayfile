# Trajectory: Use retained GitHub source archive for verified mount bootstrap

> **Status:** ✅ Completed
> **Confidence:** 92%
> **Started:** October 3, 2026 at 08:42 AM
> **Completed:** October 3, 2026 at 09:08 AM

---

## Summary

Added retained-source-archive bootstrap support: request the new export variant, honor its explicit one-component strip contract, verify all paths/types/modes/content hashes against the authoritative tree in bounded-memory staging, preserve concurrent local writebacks, and publish only after complete verification. Added HTTP, corruption atomicity, and 5,884-file gzip coverage plus OpenAPI contract updates.

**Approach:** Kept the existing manifest/tree snapshot as the authority and used the retained GitHub tar only as a faster body transport. Maintained compatibility with older or unavailable exports through the existing generated-tar and resumable per-file fallbacks.

---

## Key Decisions

### Request the retained GitHub source archive and fully verify it in staging before publication
- **Chose:** Request the retained GitHub source archive and fully verify it in staging before publication
- **Rejected:** Continue bounded-prefetch generated tar, trust the GitHub archive without tree verification
- **Reasoning:** The immutable headSha-bound archive reduces the bootstrap body from thousands of R2 reads and 175 MB of regenerated tar to one roughly 20 MB stream, while the authoritative Relayfile tree still supplies exact paths, types, modes, and SHA-256 hashes. Staging prevents a corrupt or incomplete archive from becoming visible.

---

## Chapters

### 1. Work
*Agent: default*

- Request the retained GitHub source archive and fully verify it in staging before publication: Request the retained GitHub source archive and fully verify it in staging before publication
