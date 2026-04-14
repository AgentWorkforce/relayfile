# ChromaFS-Inspired Read-Path Optimizations for RelayFile

## 1. Document Status

- Status: Proposal
- Date: 2026-04-05
- Scope: Read-path performance optimizations borrowing from ChromaFS patterns
- Audience: relayfile core, SDK, and mount implementers

## 2. Context

[ChromaFS](https://www.trychroma.com/blog/chromafs) is a virtual filesystem that
intercepts UNIX commands and translates them into queries against a Chroma vector
database. It powers a documentation assistant serving 30k+ conversations/day with
sub-100ms session creation by replacing container-based sandboxes with a
query-backed filesystem illusion.

RelayFile and ChromaFS solve fundamentally different problems — relayfile is a
bidirectional read-write bridge to live external services with multi-agent
coordination, while ChromaFS is a read-only projection of static docs. However,
three ChromaFS patterns are directly applicable to relayfile's read path:

1. **Cached path-tree** — instant `ls`/`find`/tree traversal without scanning files
2. **Two-phase content search** — coarse DB filter + fine in-memory filter for grep
3. **Lazy file resolution** — defer fetching content until `cat` time

This spec proposes adopting these patterns within relayfile's existing
`StorageAdapter` and query interfaces.

## 3. Goals

1. Make `listTree` O(tree-structure) instead of O(all-files) by caching the
   directory tree separately from file content.
2. Add content-search capability (`grep` over workspace files) that avoids
   full-scan reads via a two-phase filter.
3. Support lazy/deferred file content for large or external-origin files so
   tree listings and queries don't pay the cost of loading content they won't
   return.
4. All changes are backward-compatible with existing `StorageAdapter`
   implementations.

## 4. Non-Goals

- Embedding / vector search over file content (not needed — relayfile files are
  structured, not chunked prose).
- Replacing the storage backend with Chroma or any vector DB.
- Changing write-path semantics, writeback, or event machinery.
- FUSE mount performance (separate concern, benefits indirectly).

## 5. Design

### 5.1 Cached Path-Tree Index

**Problem.** `listTree()` and `queryFiles()` both call `storage.listFiles()`
which returns every `FileRow` including full content. For workspaces with
hundreds of provider-synced files, this is wasteful when the caller only needs
paths and metadata.

**ChromaFS pattern.** On init, ChromaFS decompresses a `__path_tree__` JSON
document into `Set<string>` (files) and `Map<string, string[]>` (dir → children).
All `ls`/`find`/`cd` resolve from memory with zero network calls.

**Proposed change.**

Add an optional `PathIndex` cache layer that sits between core logic and the
storage adapter:

```typescript
interface PathEntry {
  path: string;
  revision: string;
  provider: string;
  contentType: string;
  size: number;
  lastEditedAt: string;
  semantics: FileSemantics;
}

interface PathIndex {
  /** All known file paths with lightweight metadata (no content). */
  entries(): PathEntry[];

  /** Children of a directory path, pre-computed. */
  children(dirPath: string): PathEntry[];

  /** Check existence without loading content. */
  has(path: string): boolean;

  /** Invalidate on write/delete/webhook-apply. */
  invalidate(path: string): void;

  /** Full rebuild from storage. */
  rebuild(storage: StorageAdapter): void;
}
```

Add a lightweight listing method to `StorageAdapter`:

```typescript
interface StorageAdapter {
  // ... existing methods ...

  /**
   * List file metadata without content. Implementations SHOULD return
   * lightweight rows (content omitted or empty). Falls back to listFiles()
   * if not implemented.
   */
  listFileMeta?(): PathEntry[];
}
```

**Impact on `listTree()` and `queryFiles()`:** Both functions switch from
`storage.listFiles()` to `pathIndex.entries()` (or `pathIndex.children()` for
tree), only calling `storage.getFile()` when content is actually needed (e.g.,
`encodedSize` can be pre-computed in `PathEntry.size`).

**Invalidation:** Every `putFile`, `deleteFile`, and webhook-apply call
`pathIndex.invalidate(path)`. The index rebuilds lazily or incrementally
depending on the storage backend.

**Cache warming:** For Postgres backends, a single
`SELECT path, revision, provider, content_type, length(content), last_edited_at, semantics FROM relayfile_state`
query populates the index. For in-memory backends, the index IS the storage —
no extra work.

### 5.2 Two-Phase Content Search

**Problem.** RelayFile has no content-search API. Agents that want to grep
across workspace files must `listTree` → `getFile` each file → search locally.
This is N+1 reads.

**ChromaFS pattern.** Intercept `grep`, parse flags, translate the pattern into
a Chroma `$contains`/`$regex` filter (coarse), bulk-prefetch matching chunks,
then run the real regex in-memory (fine filter). Result: millisecond grep over
thousands of docs.

**Proposed change.**

Add a `searchContent` function to core and a corresponding REST endpoint:

```
GET /v1/workspaces/{ws}/fs/search?q=<pattern>&path=<prefix>&limit=50
```

```typescript
interface ContentSearchOptions {
  /** Regex or literal string to match. */
  pattern: string;
  /** Treat pattern as literal string (default: false = regex). */
  literal?: boolean;
  /** Case insensitive (default: false). */
  ignoreCase?: boolean;
  /** Restrict to path prefix. */
  path?: string;
  /** Max results. */
  limit?: number;
}

interface ContentSearchHit {
  path: string;
  revision: string;
  /** Matched line(s) with surrounding context. */
  matches: Array<{
    line: number;
    text: string;
  }>;
}

function searchContent(
  storage: StorageAdapter,
  index: PathIndex,
  options: ContentSearchOptions,
  claims: TokenClaims | null,
): ContentSearchHit[];
```

**Two-phase execution:**

1. **Coarse filter (storage-level).** If the storage backend supports it
   (Postgres `LIKE`/`~`, SQLite FTS), push the pattern down:
   ```typescript
   interface StorageAdapter {
     // ... existing ...
     /** Optional: return paths whose content matches pattern. */
     searchFileContent?(pattern: string, options: {
       literal?: boolean;
       ignoreCase?: boolean;
       pathPrefix?: string;
       limit?: number;
     }): string[];
   }
   ```
   If not implemented, fall back to `listFileMeta()` filtered by `path` prefix
   to get candidate paths.

2. **Fine filter (in-memory).** For each candidate path, `getFile()` and run
   the full regex against content. Build match context (line number + text).
   Respect ACL via existing `filePermissionAllows`.

**Why two phases matter:** For Postgres backends with 1000+ files, pushing
`WHERE content ~* 'pattern'` to the DB avoids transferring all file content to
the application. For in-memory backends, both phases collapse into one since
everything is already local.

### 5.3 Lazy File Resolution

**Problem.** Some files mapped into relayfile are large (OpenAPI specs,
generated schemas, binary assets) or expensive to fetch (provider API call
required). Eagerly loading content on every `listFiles()` or webhook-apply
wastes resources.

**ChromaFS pattern.** Register "lazy file pointers" — the file appears in the
directory tree, but content only fetches when the agent runs `cat`. Large
OpenAPI specs stored in S3 resolve on demand.

**Proposed change.**

Extend `FileRow` to support deferred content:

```typescript
interface FileRow {
  // ... existing fields ...

  /**
   * When set, `content` is empty and the actual body is resolved on read
   * via `storage.loadFileContent(file)`. The storage adapter or an external
   * resolver fetches the real content.
   */
  contentRef?: string;
}
```

The existing optional `loadFileContent` method on `StorageAdapter` already
provides the hook:

```typescript
interface StorageAdapter {
  /**
   * Resolve deferred content for a file with a contentRef.
   * Called by readFile() when file.contentRef is set and file.content is empty.
   * Implementations may fetch from S3, provider API, etc.
   */
  loadFileContent?(file: FileRow): { content: string; encoding?: string } | string;
}
```

**Behavior:**
- `listTree()` and `queryFiles()` never trigger lazy resolution — they use
  `PathEntry` metadata which already has `size` pre-computed.
- `readFile()` calls `loadFileContent()` when `contentRef` is set and `content`
  is empty, then caches the result in-memory for the duration of the request
  (or with a short TTL).
- `searchContent()` coarse phase skips lazy files unless the storage backend's
  `searchFileContent` can query them. Fine phase resolves lazily as needed.

**Provider integration:** When a webhook-apply creates a file with a large body
(e.g., a full GitHub file > 100KB), the adapter can store a `contentRef`
pointing to the provider's content API instead of inlining the body. This keeps
`putFile` fast and the storage footprint small.

## 6. Storage Adapter Changes Summary

All new methods are **optional** with sensible fallbacks, so existing adapters
(in-memory, durable-local, Postgres) continue to work without changes.

| Method | Required | Fallback |
|---|---|---|
| `listFileMeta()` | No | Derived from `listFiles()` (strip content) |
| `searchFileContent()` | No | Full scan via `listFileMeta()` + `getFile()` |
| `loadFileContent()` | No | Already exists, no-op if `contentRef` unused |

## 7. SDK Surface

```typescript
class RelayFileClient {
  // ... existing ...

  /** Search file content across a workspace. */
  async searchContent(
    workspaceId: string,
    options: ContentSearchOptions,
  ): Promise<ContentSearchHit[]>;
}
```

## 8. REST API

```
GET /v1/workspaces/{ws}/fs/search
  ?q=pattern          (required)
  &literal=true       (optional, default false)
  &ignoreCase=true    (optional, default false)
  &path=/github       (optional, prefix filter)
  &limit=50           (optional, default 50, max 200)

Response 200:
{
  "hits": [
    {
      "path": "/github/repos/acme/api/pulls/42/diff.patch",
      "revision": "r_47",
      "matches": [
        { "line": 12, "text": "+  const token = await refreshOAuth(...);" }
      ]
    }
  ]
}
```

## 9. Implementation Order

| Phase | Work | Complexity |
|---|---|---|
| **1 - Path Index** | `PathIndex` cache, `listFileMeta()` adapter method, refactor `listTree()`/`queryFiles()` to use it | Low — mostly refactoring existing code |
| **2 - Content Search** | `searchContent()` core function, REST endpoint, SDK method | Medium — new capability, Postgres `searchFileContent` impl |
| **3 - Lazy Resolution** | `contentRef` on `FileRow`, wire `loadFileContent()` into `readFile()`, adapter support | Medium — touches write path for ref storage |

Phases are independent and can ship separately. Phase 1 has the highest
bang-for-buck since it speeds up every tree/query operation.

## 10. What We Explicitly Don't Borrow

- **Embedding-based search.** ChromaFS uses vector similarity as a fallback.
  Relayfile files are structured data with known schemas — regex/literal search
  is sufficient and deterministic.
- **Read-only enforcement.** ChromaFS throws EROFS on writes. Relayfile's
  write path is its core value prop — no change.
- **Single-collection architecture.** ChromaFS maps everything to one Chroma
  collection. Relayfile's multi-workspace, multi-provider model needs the
  existing storage adapter abstraction.
- **Session-less design.** ChromaFS is stateless because it's read-only.
  Relayfile needs revision tracking, event feeds, and writeback queues.
