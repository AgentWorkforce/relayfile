# Trajectory: relayfile login: fall back to cloud browser flow when no --token

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** May 9, 2026 at 08:25 PM
> **Completed:** May 9, 2026 at 08:27 PM

---

## Summary

relayfile login now defaults to the cloud browser flow when --token is absent; legacy API-key prompt is preserved behind --api-key. Adds --cloud-api-url/--cloud-token/--no-open/--login-timeout flags mirroring 'setup', plus 3 unit tests.

**Approach:** Standard approach

---

## Key Decisions

### When no --token is provided, runLogin will trigger ensureCloudCredentials (browser flow) instead of prompting for API key
- **Chose:** When no --token is provided, runLogin will trigger ensureCloudCredentials (browser flow) instead of prompting for API key
- **Reasoning:** Matches what 'relayfile setup' already does. Old API-key prompt path stays available behind --api-key flag for self-hosted users who don't have cloud.

---

## Chapters

### 1. Work
*Agent: default*

- When no --token is provided, runLogin will trigger ensureCloudCredentials (browser flow) instead of prompting for API key: When no --token is provided, runLogin will trigger ensureCloudCredentials (browser flow) instead of prompting for API key
