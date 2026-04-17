# Trajectory: Reduce relayfile mount polling noise

> **Status:** ✅ Completed
> **Confidence:** 92%
> **Started:** April 17, 2026 at 03:28 PM
> **Completed:** April 17, 2026 at 03:28 PM

---

## Summary

Reduced mount-loop noise and local scan overhead by wiring fsnotify into the CLI mount command and skipping websocket no-op cycles in both mount entrypoints.

**Approach:** Standard approach

---

## Key Decisions

### Use file watching for CLI mount local writes
- **Chose:** Use file watching for CLI mount local writes
- **Reasoning:** The CLI mount entrypoint was polling and scanning the local mirror every two seconds even though mountsync already has an fsnotify watcher. Wiring the watcher lets websocket mounts skip no-op cycles while still pushing local edits immediately.

---

## Chapters

### 1. Work
*Agent: default*

- Use file watching for CLI mount local writes: Use file watching for CLI mount local writes
