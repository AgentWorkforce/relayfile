# Bulk Seed, Export, WebSocket & Binary Support — Design Doc

**Status:** Implemented
**Last Updated:** 2026-03-25

---

## Overview

This document specifies four capabilities in the RelayFile HTTP API:

1. **Bulk Seed** — atomic multi-file write in a single request
2. **Workspace Export** — full workspace snapshot in tar, JSON, or patch format
3. **WebSocket Events** — real-time push of filesystem change events
4. **Binary File Support** — base64-encoded content for non-text files

All endpoints live under the existing `/v1/workspaces/{workspaceId}/fs/` namespace and follow the same auth, correlation-ID, and rate-limiting conventions as the current API.

---

## 1. Bulk Seed

### Endpoint

```
POST /v1/workspaces/{workspaceId}/fs/bulk
```

**Auth:** Bearer JWT with `fs:write` scope.

### Purpose

Populate or update many files in a single request. Used for initial workspace seeding from a provider sync, migration tooling, or batch imports.

### Request — JSON

```
Content-Type: application/json
```

```json
{
  "files": [
    {
      "path": "/docs/readme.md",
      "contentType": "text/markdown",
      "content": "# Hello",
      "encoding": "utf-8"
    },
    {
      "path": "/assets/logo.png",
      "contentType": "image/png",
      "content": "iVBORw0KGgoAAAANSUhEU...",
      "encoding": "base64"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `files` | array | yes | Array of file objects to write |
| `files[].path` | string | yes | Virtual filesystem path (must start with `/`) |
| `files[].contentType` | string | yes | MIME type of the file content |
| `files[].content` | string | yes | File content (plain text or base64-encoded) |
| `files[].encoding` | string | no | `"utf-8"` (default) or `"base64"` for binary |

**Request body limit:** Governed by `ServerConfig.MaxBodyBytes` (default: 1 MiB).

### Request — Multipart tar.gz (Future)

```
Content-Type: multipart/form-data
```

| Part | Type | Description |
|------|------|-------------|
| `archive` | file (application/gzip) | A `.tar.gz` containing files rooted at `/` |
| `metadata` | application/json (optional) | JSON mapping paths to content types and semantics |

This format is not yet implemented. The JSON path covers all current use cases. The multipart format would allow larger imports without base64 overhead.

### Behavior

1. Each file path is normalized via `normalizeRoutePath`.
2. Permission checks run per-file against the caller's JWT claims:
   - **Existing file:** Effective permissions (target + inherited `.relayfile.acl` markers) must allow access.
   - **New file:** Inherited directory permissions must allow access.
   - Files failing permission checks are reported in the `errors` array; permitted files proceed.
3. Permitted files are passed to `Store.BulkWrite(workspaceID, files)`.
4. `BulkWrite` writes each file, generating a new revision and emitting an event per file.
5. Writeback operations are enqueued for files that have a provider binding.

### Partial Failure

Bulk write always uses partial-failure semantics. Files that pass permission checks are written even if other files fail. The response reports both the count of successful writes and a list of errors.

### Response

**Status: 202 Accepted**

```json
{
  "written": 42,
  "errorCount": 1,
  "errors": [
    {
      "path": "/restricted/secret.md",
      "code": "forbidden",
      "message": "file access denied by permission policy"
    }
  ],
  "correlationId": "corr-abc123"
}
```

### Store Method

```go
type BulkWriteFile struct {
    Path        string
    ContentType string
    Content     string
    Encoding    string         // "utf-8" or "base64"
}

type BulkWriteError struct {
    Path    string
    Code    string
    Message string
}

func (s *Store) BulkWrite(workspaceID string, files []BulkWriteFile) (int, []BulkWriteError)
```

Implementation:
- Acquires store lock once for the entire batch.
- Generates a revision per file (UUID-based).
- Emits one event per file (`file.created` or `file.updated`).
- Queues writeback operations per file if a provider is bound.
- Returns count of written files and a slice of per-path errors.

### Error Codes

| HTTP Status | Code | Condition |
|-------------|------|-----------|
| 400 | `bad_request` | Missing `files` array or empty |
| 401 | `unauthorized` | Invalid or expired JWT |
| 403 | `forbidden` | Missing `fs:write` scope |
| 413 | `payload_too_large` | Body exceeds `MaxBodyBytes` |
| 429 | `rate_limited` | Per-agent rate limit exceeded |

---

## 2. Workspace Export

### Endpoint

```
GET /v1/workspaces/{workspaceId}/fs/export?format={json|tar|patch}&path={path}
```

**Auth:** Bearer JWT with `fs:read` scope.

### Purpose

Download all visible workspace files as a single artifact. Used for backups, migrations, offline analysis, and audit.

### Query Parameters

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `format` | string | `json` | One of: `json`, `tar`, `patch` |
| `path` | string | `/` | Restrict the export to files under this VFS path |

### Permission Filtering

The export respects the caller's effective permissions. Each file is checked against inherited `.relayfile.acl` markers and target `semantics.permissions`. Files the caller cannot read are silently excluded.

### Format: `json`

**Response:** `Content-Type: application/json`

Returns a JSON array of file objects with full content and metadata:

```json
[
  {
    "path": "/docs/readme.md",
    "revision": "rev-abc",
    "contentType": "text/markdown",
    "content": "# Hello",
    "encoding": "utf-8",
    "provider": "confluence",
    "providerObjectId": "page-123",
    "lastEditedAt": "2026-03-25T10:00:00Z",
    "semantics": {
      "properties": { "stage": "active" },
      "relations": ["depends-on:/docs/api.md"],
      "permissions": ["scope:fs:read"],
      "comments": ["Reviewed 2026-03-20"]
    }
  }
]
```

Binary files are included with `"encoding": "base64"` and base64-encoded content.

### Format: `tar`

**Response:** `Content-Type: application/gzip`
**Content-Disposition:** `attachment; filename="workspace-export.tar.gz"`

Returns a gzip-compressed tar archive. Each file becomes a tar entry:

| Tar Header Field | Value |
|-----------------|-------|
| **Name** | File path with leading `/` stripped (e.g., `docs/readme.md`) |
| **Mode** | `0644` |
| **Size** | Byte length of content |
| **ModTime** | Parsed from `lastEditedAt` (falls back to Unix epoch) |

Binary files (encoding=base64) are **decoded** to raw bytes in the tar entry, so extracted files are immediately usable.

**Two-phase implementation to avoid partial responses:**

1. **Prepare phase** (`prepareTarExport`): Decodes all content, validates base64 encoding, and assembles tar entry metadata. Errors at this stage produce a proper HTTP error response (e.g., `500 export_error`).

2. **Stream phase** (`streamTarExport`): Sets HTTP headers, writes `200 OK`, and streams the gzip+tar data. Once streaming begins, HTTP status is committed — errors can only be logged, not sent to the client.

### Format: `patch`

**Response:** `Content-Type: text/x-diff; charset=utf-8`

Returns a unified diff of all files against an empty tree:

```diff
--- /dev/null
+++ b/docs/readme.md
@@ -0,0 +1,3 @@
+# Hello
+
+World
--- /dev/null
+++ b/assets/logo.png
@@ -0,0 +1 @@
+[binary content omitted; encoding=base64]
```

Binary files emit a placeholder line instead of base64 content.

### Store Method

```go
func (s *Store) ExportWorkspace(workspaceID string) ([]File, error)
```

- Returns all files in the workspace regardless of permissions (filtering is done at the HTTP layer).
- Returns `ErrInvalidInput` if the workspace ID is empty.

### Error Codes

| HTTP Status | Code | Condition |
|-------------|------|-----------|
| 400 | `bad_request` | Invalid format parameter or empty workspace ID |
| 401 | `unauthorized` | Invalid or expired JWT |
| 403 | `forbidden` | Missing `fs:read` scope |
| 429 | `rate_limited` | Per-agent rate limit exceeded |
| 500 | `export_error` | Tar preparation failed (e.g., invalid base64) |

---

## 3. WebSocket Events

### Endpoint

```
GET /v1/workspaces/{workspaceId}/fs/ws?token={jwt}
```

**Auth:** JWT passed as `token` query parameter (not Bearer header, because the WebSocket upgrade handshake does not support custom headers in browser clients).

**Required scope:** `fs:read`

### Purpose

Real-time streaming of filesystem change events over a persistent WebSocket connection. Replaces polling `/fs/events` for latency-sensitive consumers.

### Library

Uses `nhooyr.io/websocket` with `wsjson` for JSON message framing.

### Connection Lifecycle

```
Client                                    Server
  |                                         |
  |  GET /v1/workspaces/ws1/fs/ws?token=... |
  | --------- HTTP Upgrade ---------------→ |
  |                                         | validate JWT (query param)
  |                                         | check rate limit
  | ←-------- 101 Switching Protocols ----- |
  |                                         |
  |                                         | subscribe to store event channel
  |                                         | fetch recent 100 events (catch-up)
  | ←-------- catch-up events ------------- |
  | ←-------- live events (ongoing) ------- |
  |                                         |
  | --------- {"type":"ping"} -----------→  |
  | ←-------- {"type":"pong","ts":"..."} -- |
  |                                         |
  | --------- close ----------------------→ |
  | ←-------- close ack ------------------- |
```

### Catch-Up Pattern

To prevent missing events between subscribe and the first live event:

1. Subscribe to the store's event channel first.
2. Fetch the most recent 100 events via `Store.GetRecentEvents`.
3. Send catch-up events to the client.
4. Track sent event IDs in a map to deduplicate.
5. Forward live events, skipping any already sent during catch-up.

### Server → Client Messages

**File event:**

```json
{
  "type": "file.updated",
  "path": "/docs/readme.md",
  "revision": "rev-002",
  "timestamp": "2026-03-25T10:05:00Z"
}
```

Event types: `file.created`, `file.updated`, `file.deleted`, `dir.created`, `dir.deleted`, `sync.error`, `sync.ignored`, `sync.suppressed`, `sync.stale`, `writeback.failed`, `writeback.succeeded`.

**Pong (response to client ping):**

```json
{
  "type": "pong",
  "ts": "2026-03-25T10:05:00Z"
}
```

### Client → Server Messages

**Ping (keepalive):**

```json
{
  "type": "ping"
}
```

The server responds with a pong including the current server timestamp.

### Concurrency Model

- A background goroutine (`readWebSocketMessages`) handles incoming client messages and routes pings/errors through channels.
- The main goroutine reads from the store subscription channel and writes events to the WebSocket.
- Context cancellation closes the WebSocket with `StatusNormalClosure`.

### Store Integration

```go
func (s *Store) Subscribe(workspaceID string, ch chan<- Event) func()
```

The store's `Subscribe` method registers a channel to receive events for a workspace. The returned function unsubscribes and is called on connection close.

```go
func (s *Store) GetRecentEvents(workspaceID string, limit int) ([]Event, error)
```

Used for the catch-up phase on initial connection.

### Polling Fallback

For environments where WebSocket connections are unavailable (some proxies, serverless):

- Use `GET /v1/workspaces/{workspaceId}/fs/events?cursor={lastCursor}&limit=200`
- Poll on a 1–5 second interval.
- The `nextCursor` field in the response provides the resume point.

### Future: Client-Side Filtering

A `subscribe` message with path and event type filters is a natural extension:

```json
{
  "type": "subscribe",
  "filter": {
    "paths": ["/docs/*"],
    "eventTypes": ["file.created", "file.updated"],
    "providers": ["notion"]
  }
}
```

This is not yet implemented. Currently all workspace events are streamed to every connected client.

---

## 4. Binary File Support

### Purpose

Support storing and retrieving non-text files (images, PDFs, compiled assets) through the same filesystem API by allowing base64-encoded content.

### Encoding Field

All write endpoints (`PUT /fs/file`, `POST /fs/bulk`) accept an optional `encoding` field:

| Value | Behavior |
|-------|----------|
| `utf-8` | Default. Content is plain text. |
| `base64` | Content is base64-encoded binary data. |

### Write Path

```json
PUT /v1/workspaces/ws1/fs/file?path=/assets/logo.png
If-Match: *

{
  "contentType": "image/png",
  "content": "iVBORw0KGgoAAAANSUhEU...",
  "encoding": "base64"
}
```

The store persists the `encoding` field alongside the content. The content string is stored as-is (the base64 string, not decoded bytes). This keeps the storage layer simple and avoids binary blob handling.

### Read Path

```json
GET /v1/workspaces/ws1/fs/file?path=/assets/logo.png

{
  "path": "/assets/logo.png",
  "revision": "rev-003",
  "contentType": "image/png",
  "content": "iVBORw0KGgoAAAANSUhEU...",
  "encoding": "base64",
  "lastEditedAt": "2026-03-25T10:00:00Z"
}
```

The `encoding` field tells the client how to interpret the `content` string.

### Bulk Write

Binary files in bulk write use the same `encoding` field per file entry:

```json
{
  "files": [
    { "path": "/logo.png", "contentType": "image/png", "content": "iVBOR...", "encoding": "base64" },
    { "path": "/readme.md", "contentType": "text/markdown", "content": "# Hi" }
  ]
}
```

### Export Handling

| Format | Binary behavior |
|--------|-----------------|
| `json` | Content returned as base64 string with `encoding: "base64"` |
| `tar` | Base64 content is **decoded** to raw bytes in the tar entry |
| `patch` | Placeholder line: `+[binary content omitted; encoding=base64]` |

The tar format decodes base64 so that extracted files are immediately usable. Both standard and raw base64 (no padding) are accepted during decode:

```go
func decodeBase64String(value string) ([]byte, error) {
    if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
        return decoded, nil
    }
    return base64.RawStdEncoding.DecodeString(value)
}
```

### Content Size

Binary content is subject to the same `MaxBodyBytes` limit as text content (default: 1 MiB). The base64 encoding adds ~33% overhead, so effective binary file size limit is ~750 KiB with default settings.

### Impact on Endpoints

| Endpoint | Change |
|----------|--------|
| `PUT /fs/file` | Accepts `encoding` field |
| `GET /fs/file` | Returns `encoding` field |
| `POST /fs/bulk` | Accepts `encoding` per file |
| `GET /fs/export` | Includes `encoding` per file; tar decodes base64 |
| `GET /fs/query` | No change (doesn't return content) |
| `GET /fs/tree` | No change |
| WebSocket events | No change (events don't include content) |

---

## HTTP Handler Routing

Routes in `ServeHTTP`:

```go
case len(parts) == 5 && parts[3] == "fs" && parts[4] == "ws" && r.Method == http.MethodGet:
    requiredScope = "fs:read"
    route = "fs_ws"

case len(parts) == 5 && parts[3] == "fs" && parts[4] == "bulk" && r.Method == http.MethodPost:
    requiredScope = "fs:write"
    route = "bulk_write"

case len(parts) == 5 && parts[3] == "fs" && parts[4] == "export" && r.Method == http.MethodGet:
    requiredScope = "fs:read"
    route = "export"
```

The WebSocket route (`fs_ws`) bypasses the standard Bearer header auth and correlation ID requirement — it handles auth via the `token` query parameter and is dispatched before the main auth middleware.

---

## Cross-Cutting Concerns

### Authentication & Authorization

| Endpoint | Auth Method | Required Scope |
|----------|-------------|----------------|
| `POST /fs/bulk` | Bearer JWT (header) | `fs:write` |
| `GET /fs/export` | Bearer JWT (header) | `fs:read` |
| `GET /fs/ws` | JWT (query param) | `fs:read` |

All workspace-scoped endpoints validate that the JWT's `workspace_id` claim matches the URL workspace.

### Correlation ID

All REST endpoints require `X-Correlation-Id` header. The WebSocket endpoint is the exception (correlation IDs are embedded in event payloads).

### Rate Limiting

Rate limiting is keyed on `{workspaceId}|{agentName}` with a configurable window and max count. Applied to all authenticated endpoints including WebSocket connection establishment.

### Error Format

All errors use the standard envelope:

```json
{
  "code": "bad_request",
  "message": "missing files",
  "correlationId": "corr-abc123"
}
```

---

## Migration & Compatibility

- All changes are additive — no existing endpoints are modified.
- The `encoding` field defaults to `"utf-8"`, so existing clients see no change.
- Files written before binary support have no `encoding` metadata and default to `"utf-8"` on read.
- The WebSocket endpoint is independent of the polling events endpoint; both remain available.
