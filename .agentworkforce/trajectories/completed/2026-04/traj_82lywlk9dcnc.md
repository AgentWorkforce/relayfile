# Trajectory: Write SDK setup client workflow from spec

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** April 30, 2026 at 09:43 AM
> **Completed:** April 30, 2026 at 09:51 AM

---

## Summary

Added workflow 062-sdk-setup-client with explicit 80-to-100 validation gates for SDK setup client and cloud API contract; corrected docs/sdk-setup-client.md to use https://agentrelay.com/cloud based on ../cloud source of truth; validated workflow syntax with tsc.

**Approach:** Standard approach

---

## Key Decisions

### Use https://agentrelay.com/cloud as SDK setup default
- **Chose:** Use https://agentrelay.com/cloud as SDK setup default
- **Reasoning:** ../cloud/infra/web-routing.ts exports appUrl with /cloud and ../cloud/packages/cli/src/cli/constants.ts defaults CLOUD API to https://agentrelay.com/cloud; docs/sdk-setup-client.md currently points at obsolete app.agentrelay.com.

---

## Chapters

### 1. Work
*Agent: default*

- Use https://agentrelay.com/cloud as SDK setup default: Use https://agentrelay.com/cloud as SDK setup default
