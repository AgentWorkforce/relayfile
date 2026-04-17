# Trajectory: Make relayfile mount bootstrap fast for sandbox agents

> **Status:** ✅ Completed
> **Confidence:** 93%
> **Started:** April 17, 2026 at 03:06 PM
> **Completed:** April 17, 2026 at 03:12 PM

---

## Summary

Changed mirror mount bootstrap to use the Relayfile export JSON snapshot with server-side path filtering, preserved tree/read fallback for older servers, and documented the export path parameter.

**Approach:** Standard approach

---

## Key Decisions

### Bootstrap mirror mounts from the export snapshot API
- **Chose:** Bootstrap mirror mounts from the export snapshot API
- **Reasoning:** Mount startup was doing one tree request plus one file read per visible file and then walking events. The implemented export endpoint already provides a full JSON snapshot with revisions, so using it makes sandbox mounts one request while preserving a tree/read fallback for older servers.

---

## Chapters

### 1. Work
*Agent: default*

- Bootstrap mirror mounts from the export snapshot API: Bootstrap mirror mounts from the export snapshot API
