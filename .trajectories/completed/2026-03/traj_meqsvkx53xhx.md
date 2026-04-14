# Trajectory: FUSE mount implementation for relayfile

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** March 26, 2026 at 11:07 PM
> **Completed:** March 26, 2026 at 11:07 PM

---

## Summary

Created internal/mountfuse/ with 8 files: fs.go (root+mount+cache+inode+error mapping), dir.go (readdir/lookup/create/unlink), file.go (open/read/write/flush with If-Match), client.go (FuseClient wrapper), cache.go (LRU+TTL), wsinvalidate.go (WS cache invalidation), cache_test.go (7 tests), fuse_test.go (10 tests). Wired into cmd/relayfile-mount via fuse_mount.go build tag. 27 tests passing.

**Approach:** Standard approach

---

## Key Decisions

### Used hanwen/go-fuse/v2 with fsState internal cache instead of standalone Cache for FUSE nodes
- **Chose:** Used hanwen/go-fuse/v2 with fsState internal cache instead of standalone Cache for FUSE nodes
- **Reasoning:** fsState provides tighter integration with FUSE lifecycle (dir/file caches, inode map) vs generic LRU

---

## Chapters

### 1. Work
*Agent: default*

- Used hanwen/go-fuse/v2 with fsState internal cache instead of standalone Cache for FUSE nodes: Used hanwen/go-fuse/v2 with fsState internal cache instead of standalone Cache for FUSE nodes
