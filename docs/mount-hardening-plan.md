# Mount Client Hardening Plan

This document captures the research and proposed implementation strategy for the three hardening areas identified in the README: **true FUSE integration**, **inode cache semantics**, and **conflict translation**.

---

## 1. Current State

The mount client (`cmd/relayfile-mount` + `internal/mountsync`) uses a **polling-based sync loop**:

- Every N seconds (default 2s, with jitter), it runs `SyncOnce()`.
- **Push phase**: walks the local directory, detects hash changes, and PUTs modified files with `If-Match` optimistic concurrency.
- **Pull phase**: either fetches the full remote tree or uses an incremental event cursor to download changed/deleted files.
- Internal tracked state is persisted to `.relayfile-mount-state.json`, while the
  user-facing synced-mirror status surface is written to `.relay/state.json`
  with explicit `ready` / `stale` / `offline` / `conflict` /
  `writeback-pending` states.
- Conflict on write is detected via HTTP 409 and the file is marked `Dirty` for retry on the next cycle.

### Limitations

| Area | Current Behavior | Problem |
|------|-----------------|---------|
| Filesystem interface | Polling + `os.ReadFile` / `os.WriteFile` | Agent writes are invisible until the next poll; no instant notification; race window between poll cycles |
| Cache coherence | SHA-256 hash comparison per file per cycle | No expiration, no TTL; stale reads possible between polls; full-tree walk is O(n) for large workspaces |
| Conflict handling | HTTP 409 -> local body is preserved in `.relay/conflicts/…`, remote is refreshed in place, status flips to `conflict` | User-visible and machine-readable, but still requires manual resolution before retry |

---

## 2. True FUSE Integration

### 2.1 Goal

Replace the polling-based local mirror with a true FUSE (Filesystem in Userspace) filesystem so that:

- Agent reads hit the remote API on-demand (or from cache) without waiting for a sync cycle.
- Agent writes are captured immediately by the kernel VFS layer, not discovered on the next poll.
- The local directory behaves like a real mounted filesystem (`ls`, `cat`, `vim`, etc. all work).

### 2.2 Recommended Library

**[`bazil.org/fuse`](https://github.com/bazil/fuse)** (pure Go, well-maintained, macOS + Linux).

Alternative: [`hanwen/go-fuse`](https://github.com/hanwen/go-fuse) (higher performance pathfs/nodefs API, used by rclone). If macOS support is critical (agents primarily run on macOS), `bazil.org/fuse` has broader macOS compatibility. For Linux-only production, `hanwen/go-fuse` is more performant.

Platform dependency: macOS requires [macFUSE](https://osxfuse.github.io/); Linux requires `libfuse` / `fuse3`. Docker containers can use `--device /dev/fuse --cap-add SYS_ADMIN`.

### 2.3 Architecture

```
Agent process (read/write files)
       |
       v
  kernel VFS  <-->  FUSE kernel module
       |
       v
  relayfile-fuse (userspace daemon)
       |
       +-- Lookup/GetAttr/ReadDir  -->  cached tree metadata
       +-- Read                    -->  content cache or remote ReadFile
       +-- Write/Create            -->  buffer + WriteFile with If-Match
       +-- Unlink/Rename           -->  DeleteFile / WriteFile
       +-- Fsync                   -->  flush pending writes to remote
```

### 2.4 Implementation Phases

**Phase 1: Read-only FUSE mount**

1. Add `bazil.org/fuse` dependency.
2. Implement a `fuseFS` struct satisfying `fuse.FS` / `fuse.Node` / `fuse.Handle` interfaces.
3. `Lookup` and `ReadDirAll` call `ListTree` (cached, see Section 3).
4. `Read` calls `ReadFile` (cached).
5. `GetAttr` returns synthetic inode numbers, sizes derived from content length, and timestamps from event feed.
6. Mount with `fuse.Mount(mountpoint, fuseFS, ...)`.
7. Retain the existing `mountsync` polling path as a fallback (`--mode=poll|fuse`, default `fuse`).

**Phase 2: Read-write FUSE mount**

1. `Write` / `Create` buffer content in memory (or a local spool directory).
2. `Fsync` or `Release` triggers `WriteFile` with `If-Match` against the cached revision.
3. `Unlink` triggers `DeleteFile`.
4. `Rename` implemented as read-source + write-dest + delete-source (the REST API has no rename primitive).
5. On conflict (409), apply conflict translation (Section 4).

**Phase 3: Graceful degradation**

1. If FUSE is unavailable (no macFUSE, no /dev/fuse), auto-fall back to polling mode with a log warning.
2. `--fuse-fallback=poll` flag to control behavior.
3. Docker Compose updated: add `--device /dev/fuse` and `SYS_ADMIN` capability to the mountsync container.

### 2.5 File Layout

```
cmd/relayfile-mount/main.go          (add --mode=fuse|poll flag)
internal/mountfuse/fs.go             (fuse.FS, root node)
internal/mountfuse/dir.go            (directory node)
internal/mountfuse/file.go           (file node, read/write handles)
internal/mountfuse/cache.go          (inode + content cache, see Section 3)
internal/mountfuse/conflict.go       (conflict translation, see Section 4)
internal/mountfuse/fs_test.go        (unit tests with mock RemoteClient)
```

---

## 3. Inode Cache Semantics

### 3.1 Goal

Provide a coherent, performant cache layer so FUSE operations do not hit the remote API on every `stat`, `readdir`, or `read` call, while bounding staleness.

### 3.2 Cache Design

#### 3.2.1 Metadata Cache (Tree / Attr)

| Property | Value |
|----------|-------|
| Key | remote path |
| Value | `{inode, mode, size, revision, contentType, mtime, children}` |
| TTL | configurable, default 5s (`RELAYFILE_CACHE_METADATA_TTL`) |
| Invalidation | event feed poll (background goroutine), explicit `Fsync`, write-through on local mutations |
| Eviction | LRU with max entries (default 10,000; `RELAYFILE_CACHE_MAX_ENTRIES`) |

#### 3.2.2 Content Cache

| Property | Value |
|----------|-------|
| Key | remote path + revision |
| Value | file content bytes |
| TTL | revision-pinned (immutable per revision); evict on LRU pressure |
| Max size | configurable, default 256 MB (`RELAYFILE_CACHE_MAX_BYTES`) |
| Storage | in-memory for small files; optional disk spool for files > 1 MB |

#### 3.2.3 Inode Allocation

FUSE requires stable inode numbers. Strategy:

- Root directory = inode 1.
- Hash remote path to uint64 (FNV-1a) for deterministic, collision-resistant inode assignment.
- Maintain a `map[string]uint64` (path -> inode) persisted across mount sessions in the state file.
- On collision (extremely rare with FNV-1a over path strings), linear probe to next available inode.

#### 3.2.4 Background Refresh

A background goroutine polls the event feed (same cursor mechanism as current `pullRemoteIncremental`) and:

1. Invalidates metadata cache entries for changed paths.
2. Pre-fetches updated content for hot files (files accessed in the last TTL window).
3. Updates inode map for newly created / deleted paths.

Poll interval: `RELAYFILE_CACHE_REFRESH_INTERVAL` (default 1s, separate from the legacy sync interval).

#### 3.2.5 Kernel Cache Hints

- Set `fuse.Attr.Valid` to metadata TTL so the kernel caches `stat` results.
- Use `fuse.OpenDirectIO` for files where instant coherence matters (opt-in via `.relayfile.acl` property).
- Use `fuse.OpenKeepCache` when revision has not changed since last open.

### 3.3 Cache Coherence Contract

| Operation | Guarantee |
|-----------|-----------|
| `stat` / `getattr` | Fresh within metadata TTL |
| `readdir` | Fresh within metadata TTL |
| `read` | Correct for the revision seen at `open` time (revision-pinned) |
| `write` | Write-through; cache updated immediately on successful PUT |
| `fsync` | All pending writes flushed; cache refreshed from remote |

---

## 4. Conflict Translation

### 4.1 Goal

When a local write conflicts with a concurrent remote change (HTTP 409), produce a visible, recoverable artifact instead of silently retrying.

### 4.2 Current Behavior

On 409, the syncer:
1. Logs "conflict writing <path>".
2. Marks the tracked file as `Dirty: true`.
3. On the next cycle, retries the write (which will likely conflict again if the remote changed).

This creates a silent infinite retry loop with no user visibility.

### 4.3 Proposed Conflict Resolution Strategy

#### 4.3.1 Conflict File Generation

When a write returns 409:

1. Fetch the current remote file (content + revision).
2. Save the local version as `<filename>.LOCAL.<ext>` (e.g., `guide.LOCAL.md`).
3. Save the remote version as `<filename>.REMOTE.<ext>` (e.g., `guide.REMOTE.md`).
4. Overwrite the original file with the remote version (so the canonical path always reflects server truth).
5. Create a `.relayfile-conflicts.json` manifest in the directory:

```json
{
  "conflicts": [
    {
      "path": "/docs/guide.md",
      "localFile": "guide.LOCAL.md",
      "remoteFile": "guide.REMOTE.md",
      "localRevision": "rev_abc",
      "remoteRevision": "rev_xyz",
      "detectedAt": "2026-03-11T21:55:00Z"
    }
  ]
}
```

6. Log a prominent warning: `CONFLICT: /docs/guide.md -- local and remote versions saved as guide.LOCAL.md / guide.REMOTE.md`.

#### 4.3.2 Conflict Resolution by Agent / User

An agent or user resolves the conflict by:

1. Editing the canonical file (`guide.md`) to the desired content.
2. Deleting `guide.LOCAL.md` and `guide.REMOTE.md`.
3. On the next sync cycle, the syncer detects:
   - Canonical file changed -> push to remote.
   - Conflict files deleted -> remove from manifest.
   - Manifest empty -> delete `.relayfile-conflicts.json`.

#### 4.3.3 Conflict Policies (Configurable)

| Policy | Behavior | Flag |
|--------|----------|------|
| `conflict-files` (default) | Generate LOCAL/REMOTE conflict files as described above |  `--conflict-strategy=conflict-files` |
| `remote-wins` | Silently overwrite local with remote; no conflict files | `--conflict-strategy=remote-wins` |
| `local-wins` | Force-push local content (retry with latest remote revision as base) | `--conflict-strategy=local-wins` |
| `fail` | Stop sync cycle and exit with error code | `--conflict-strategy=fail` |

Environment variable: `RELAYFILE_CONFLICT_STRATEGY`.

#### 4.3.4 FUSE-Specific Conflict Behavior

In FUSE mode, conflicts are detected at `Fsync` / `Release` time:

1. The `Write` syscall buffers content locally (always succeeds from the agent's perspective).
2. `Fsync` or `Release` attempts the PUT with `If-Match`.
3. On 409, the conflict strategy is applied:
   - `conflict-files`: conflict artifacts materialized; `Fsync` returns `syscall.EBUSY` or `0` (configurable).
   - `remote-wins`: local buffer discarded; `Fsync` returns `0`.
   - `local-wins`: retry with fetched revision; `Fsync` returns `0`.
   - `fail`: `Fsync` returns `syscall.EBUSY`.

---

## 5. Implementation Roadmap

| Phase | Scope | Estimated Effort | Dependencies |
|-------|-------|-----------------|--------------|
| **P0** | Conflict translation in current polling syncer | 1-2 days | None |
| **P1** | Inode cache + background event refresh | 2-3 days | None |
| **P2** | Read-only FUSE mount | 3-5 days | P1, bazil.org/fuse |
| **P3** | Read-write FUSE mount with conflict translation | 3-5 days | P0, P2 |
| **P4** | Docker/CI integration, macFUSE detection, fallback | 1-2 days | P2 |

Total estimated effort: 10-17 days.

### P0 can be done independently and immediately improves the polling mount.

---

## 6. Configuration Summary

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAYFILE_MOUNT_MODE` | `fuse` | `fuse` or `poll` |
| `RELAYFILE_FUSE_FALLBACK` | `poll` | fallback when FUSE unavailable |
| `RELAYFILE_CONFLICT_STRATEGY` | `conflict-files` | `conflict-files`, `remote-wins`, `local-wins`, `fail` |
| `RELAYFILE_CACHE_METADATA_TTL` | `5s` | metadata cache TTL |
| `RELAYFILE_CACHE_MAX_ENTRIES` | `10000` | max cached metadata entries |
| `RELAYFILE_CACHE_MAX_BYTES` | `268435456` (256 MB) | max content cache size |
| `RELAYFILE_CACHE_REFRESH_INTERVAL` | `1s` | background event poll interval |

---

## 7. Testing Strategy

- **Unit tests**: mock `RemoteClient` for cache hit/miss/eviction, conflict file generation, inode allocation stability.
- **Integration tests**: spin up `relayfile` in-memory, mount via FUSE in a temp directory, perform read/write/conflict scenarios.
- **E2E tests**: extend `scripts/live-e2e.sh` to exercise FUSE mount mode with Docker `--device /dev/fuse`.
- **Benchmarks**: measure FUSE latency vs. polling latency for read-heavy and write-heavy workloads.

---

## 8. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| macFUSE unavailable on agent machines | FUSE mode unusable | Auto-fallback to polling; clear error messages |
| FUSE kernel deadlocks on buggy operations | Mount hangs | Timeouts on all remote calls; `SIGTERM` handler unmounts cleanly |
| High-frequency writes overwhelm API | 429 / degraded performance | Write coalescing in FUSE buffer; respect Retry-After; backpressure via `Fsync` latency |
| Inode number collisions | Kernel confusion | FNV-1a + linear probe; collision rate is negligible for path-based keys |
| Content cache memory pressure | OOM | Bounded LRU with configurable max; disk spool for large files |
