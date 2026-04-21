# Trajectory: Replace chokidar with @parcel/watcher in local-mount

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** April 20, 2026 at 04:35 PM
> **Completed:** April 20, 2026 at 04:58 PM

---

## Summary

Replaced chokidar with @parcel/watcher in packages/local-mount. All 22 tests pass. Exit-hang should be gone because unsubscribe() on native-backed watchers resolves promptly instead of draining stability-poll timers.

**Approach:** Standard approach

---

## Key Decisions

### Switched watcher from chokidar v4 to @parcel/watcher 2.5.6
- **Chose:** Switched watcher from chokidar v4 to @parcel/watcher 2.5.6
- **Reasoning:** Chokidar v4's awaitWriteFinish polling + fs.watch teardown caused noticeable hangs on exit. @parcel/watcher uses native FSEvents/inotify/ReadDirectoryChangesW with clean async unsubscribe. Replaced awaitWriteFinish with a small per-path setTimeout debounce; reconcile loop + mtime/content check absorb the looser write semantics.

---

## Chapters

### 1. Work
*Agent: default*

- Switched watcher from chokidar v4 to @parcel/watcher 2.5.6: Switched watcher from chokidar v4 to @parcel/watcher 2.5.6
