# Trajectory: Review and fix PR #243

> **Status:** ✅ Completed
> **Confidence:** 90%
> **Started:** June 6, 2026 at 12:15 AM
> **Completed:** June 8, 2026 at 11:32 AM

---

## Summary

Reviewed PR #259, fixed validated read-not-ready cleanup findings in mountsync, and verified focused/package Go tests locally.

**Approach:** Standard approach

---

## Key Decisions

### Validated PR bot comments before editing
- **Chose:** Validated PR bot comments before editing
- **Reasoning:** Fetched PR #255 discussion via GitHub connector; available bot comments had no actionable findings against current checkout.

### Fixed narrowed grant empty-path match
- **Chose:** Fixed narrowed grant empty-path match
- **Reasoning:** Current scopeMatchesPath returned true for any applicable narrowed scope when filePath was empty; only wildcard path scopes should bypass path matching.

### Extended path-scoped fs enforcement to tree/events/websocket
- **Chose:** Extended path-scoped fs enforcement to tree/events/websocket
- **Reasoning:** Tracing fs:read callers showed tree and websocket already accept path filters but authorized with an empty required path, and events has no path filter; narrowed mount grants should not authorize whole-workspace reads.

### Patch ingestWebhook to pass provider into normalizeEnvelopePath
- **Chose:** Patch ingestWebhook to pass provider into normalizeEnvelopePath
- **Reasoning:** Current checkout canonicalizes Slack provider-relative paths in normalizeEnvelope, but ingestWebhook re-normalizes payload.path without provider and rejects /channels/... as invalid_input before queueing.

### Validated read-not-ready state persistence through sync error save path
- **Chose:** Validated read-not-ready state persistence through sync error save path
- **Reasoning:** Reconcile delegates to sync, and sync marks errors then saves state before returning pullRemote errors, so IncrementalReadNotReadySince is persisted on retry errors.

### Centralized read-not-ready cleanup in remote apply helpers
- **Chose:** Centralized read-not-ready cleanup in remote apply helpers
- **Reasoning:** WebSocket and full-pull paths call applyRemoteFile/applyRemoteDelete outside applyIncrementalChanges, so helper-level cleanup prevents stale TTL timestamps from affecting future recreates.

---

## Chapters

### 1. Work
*Agent: default*

- Validated PR bot comments before editing: Validated PR bot comments before editing
- PR #255 review found no actionable bot findings; focused auth package tests pass after installing Go.
- Fixed narrowed grant empty-path match: Fixed narrowed grant empty-path match
- Extended path-scoped fs enforcement to tree/events/websocket: Extended path-scoped fs enforcement to tree/events/websocket
- Validated bot finding and caller trace; first websocket test run exposed a no-path slice guard issue now fixed.
- PR #255 review addressed CodeRabbit's empty-path finding, added fs:write coverage, and closed related path-scoped fs read gaps for tree, events, and websocket subscriptions. Verified with internal/httpapi tests, full Go tests, Go builds, go vet, and contract surface check.
- Patch ingestWebhook to pass provider into normalizeEnvelopePath: Patch ingestWebhook to pass provider into normalizeEnvelopePath
- Validated read-not-ready state persistence through sync error save path: Validated read-not-ready state persistence through sync error save path
- Centralized read-not-ready cleanup in remote apply helpers: Centralized read-not-ready cleanup in remote apply helpers
- PR #259 reviewed; bot findings validated and fixed in mountsync helpers; focused and package tests pass
