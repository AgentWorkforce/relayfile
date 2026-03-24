# Bulk Seed, Export, WebSocket & Binary Support — Design Doc

**Status:** Draft
**Date:** 2026-03-24

---

## Overview

This document specifies four new capabilities for the RelayFile HTTP API:

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

**Scope:** `fs:write`

### Purpose

Populate or update many files in a single atomic request. Useful for initial workspace seeding from a provider sync, migration tooling, or batch imports.

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
      "encoding": "utf-8",
      "semantics": {
        "properties": { "stage": "active" },
        "relations": [],
        "permissions": [],
        "comments": []
      }
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `files` | array | yes | Array of file objects to write |
| `files[].path` | string | yes | Virtual filesystem path (must start with `/`) |
| `files[].contentType` | string | no | MIME type (default: `text/markdown`) |
| `files[].content` | string | yes | File content (plain text or base64-encoded) |
| `files[].encoding` | string | no | `"utf-8"` (default) or `"base64"` |
| `files[].semantics` | object | no | Optional semantic metadata (properties, relations, permissions, comments) |

**Limits:**

- Maximum 500 files per request
- Total request body capped at 50 MB
- Individual file content capped at 10 MB

### Request — Multipart tar.gz

```
Content-Type: multipart/form-data
```

| Part | Type | Description |
|------|------|-------------|
| `archive` | file (application/gzip) | A `.tar.gz` containing files rooted at `/` |
| `metadata` | application/json (optional) | JSON object mapping paths to semantic metadata |

The tar archive is extracted with paths mapped directly to the virtual filesystem. Directory entries are created implicitly. The optional `metadata` part allows attaching semantics:

```json
{
  "/docs/readme.md": {
    "contentType": "text/markdown",
    "semantics": {
      "properties": { "stage": "active" }
    }
  }
}
```

### Atomicity

- All files are written within a single store transaction.
- If any file fails validation (invalid path, exceeds size), the entire batch is rejected.
- Each successfully written file produces one filesystem event (`file.created` or `file.updated`).
- A single `If-Match: *` semantic is used — bulk seed always overwrites existing content (create-or-update).

### Response

**Success (200):**

```json
{
  "imported": 12,
  "skipped": 0,
  "errors": [],
  "correlationId": "corr-abc123"
}
```

**Partial failure (207 Multi-Status):**

Returned only if the `allowPartial=true` query parameter is set. Without it, any error rejects the entire batch.

```json
{
  "imported": 10,
  "skipped": 0,
  "errors": [
    { "path": "/bad/path", "error": "invalid path: must start with /" },
    { "path": "/too/large.bin", "error": "content exceeds 10 MB limit" }
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
    Semantics   FileSemantics
}

type BulkWriteResult struct {
    Imported int
    Skipped  int
    Errors   []BulkWriteError
}

type BulkWriteError struct {
    Path  string
    Error string
}

func (s *Store) BulkWrite(workspaceID string, files []BulkWriteFile, allowPartial bool, correlationID string) (BulkWriteResult, error)
```

Implementation notes:
- Acquires store lock once for the entire batch.
- Validates all files before writing any (unless `allowPartial`).
- Generates a revision per file, emits one event per file.
- Queues writeback operations per file if the workspace has provider bindings.

### OpenAPI Addition

```yaml
/v1/workspaces/{workspaceId}/fs/bulk:
  post:
    tags: [Filesystem]
    operationId: bulkSeedFiles
    summary: Atomically write multiple files in a single request
    security:
      - BearerAuth: [fs:write]
    parameters:
      - $ref: '#/components/parameters/WorkspaceId'
      - $ref: '#/components/parameters/CorrelationId'
      - name: allowPartial
        in: query
        required: false
        schema:
          type: boolean
          default: false
    requestBody:
      required: true
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/BulkSeedRequest'
        multipart/form-data:
          schema:
            type: object
            properties:
              archive:
                type: string
                format: binary
              metadata:
                type: string
                format: json
    responses:
      '200':
        description: All files imported successfully
      '207':
        description: Partial success (allowPartial=true)
      '400':
        $ref: '#/components/responses/BadRequest'
      '401':
        $ref: '#/components/responses/Unauthorized'
      '403':
        $ref: '#/components/responses/Forbidden'
      '413':
        $ref: '#/components/responses/PayloadTooLarge'
      '429':
        $ref: '#/components/responses/RateLimited'
```

---

## 2. Workspace Export

### Endpoint

```
GET /v1/workspaces/{workspaceId}/fs/export
```

**Scope:** `fs:read`

### Purpose

Download the entire workspace filesystem as a single artifact. Useful for backups, migrations, offline analysis, and audit.

### Query Parameters

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `format` | string | `json` | One of: `tar`, `json`, `patch` |
| `path` | string | `/` | Subtree to export (prefix filter) |
| `includeSemantics` | boolean | `true` | Include semantic metadata in export |

### Format: `tar`

**Response:** `Content-Type: application/gzip`

Returns a gzip-compressed tar archive. File paths in the archive mirror the virtual filesystem paths. Binary files are stored as raw bytes. A `.relayfile-manifest.json` file is included at the root containing metadata:

```json
{
  "workspaceId": "ws-123",
  "exportedAt": "2026-03-24T12:00:00Z",
  "fileCount": 42,
  "files": {
    "/docs/readme.md": {
      "revision": "rev-abc",
      "contentType": "text/markdown",
      "semantics": { "properties": { "stage": "active" } }
    }
  }
}
```

### Format: `json`

**Response:** `Content-Type: application/json`

```json
{
  "workspaceId": "ws-123",
  "exportedAt": "2026-03-24T12:00:00Z",
  "files": [
    {
      "path": "/docs/readme.md",
      "revision": "rev-abc",
      "contentType": "text/markdown",
      "content": "# Hello",
      "encoding": "utf-8",
      "semantics": {
        "properties": { "stage": "active" },
        "relations": [],
        "permissions": [],
        "comments": []
      }
    }
  ]
}
```

Binary files are included with `"encoding": "base64"` and base64-encoded content.

### Format: `patch`

**Response:** `Content-Type: text/plain`

Returns a unified diff of all files against an empty tree, similar to `git diff --no-index /dev/null`. Each file produces a diff hunk:

```diff
--- /dev/null
+++ a/docs/readme.md
@@ -0,0 +1,1 @@
+# Hello
```

Binary files are represented as:

```
Binary file /images/logo.png added (4.2 KB, base64)
```

### Permission Filtering

The export respects the caller's effective permissions. Files the caller cannot read (per ACL rules) are silently excluded. The response includes only visible files.

### Store Method

```go
func (s *Store) ExportWorkspace(workspaceID string, pathPrefix string) ([]File, error)
```

Returns all files under the given path prefix. The HTTP handler is responsible for format conversion (tar assembly, patch generation).

### Streaming

For large workspaces, the tar and patch formats stream directly to the response writer without buffering the entire archive in memory. The JSON format buffers because it requires a valid JSON array.

For workspaces exceeding 10,000 files or 500 MB total content, the server returns `413 Payload Too Large` with a suggestion to use path-scoped exports.

### OpenAPI Addition

```yaml
/v1/workspaces/{workspaceId}/fs/export:
  get:
    tags: [Filesystem]
    operationId: exportWorkspace
    summary: Export workspace files as tar, JSON, or patch
    security:
      - BearerAuth: [fs:read]
    parameters:
      - $ref: '#/components/parameters/WorkspaceId'
      - $ref: '#/components/parameters/CorrelationId'
      - name: format
        in: query
        required: false
        schema:
          type: string
          enum: [tar, json, patch]
          default: json
      - name: path
        in: query
        required: false
        schema:
          type: string
          default: /
      - name: includeSemantics
        in: query
        required: false
        schema:
          type: boolean
          default: true
    responses:
      '200':
        description: Workspace export
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/ExportJsonResponse'
          application/gzip:
            schema:
              type: string
              format: binary
          text/plain:
            schema:
              type: string
      '401':
        $ref: '#/components/responses/Unauthorized'
      '403':
        $ref: '#/components/responses/Forbidden'
      '413':
        $ref: '#/components/responses/PayloadTooLarge'
      '429':
        $ref: '#/components/responses/RateLimited'
```

---

## 3. WebSocket Events

### Endpoint

```
GET /v1/workspaces/{workspaceId}/fs/ws
```

**Scope:** `fs:read`

### Purpose

Real-time push of filesystem change events over a persistent WebSocket connection. Replaces polling the `/fs/events` endpoint for latency-sensitive consumers.

### Connection Lifecycle

1. Client sends HTTP GET with `Upgrade: websocket` header.
2. Server validates JWT bearer token (same as REST endpoints).
3. On successful upgrade, server sends a `connected` frame.
4. Client optionally sends `subscribe` messages to filter events.
5. Server pushes events as they occur.
6. Either side can close the connection.

### Server-to-Client Messages

**Connected:**

```json
{
  "type": "connected",
  "workspaceId": "ws-123",
  "serverTime": "2026-03-24T12:00:00Z"
}
```

**Event:**

```json
{
  "type": "file.created",
  "path": "/docs/readme.md",
  "revision": "rev-abc",
  "provider": "notion",
  "origin": "provider_sync",
  "correlationId": "corr-xyz",
  "timestamp": "2026-03-24T12:00:01Z"
}
```

Event types mirror the existing `FilesystemEvent` schema:
- `file.created`
- `file.updated`
- `file.deleted`
- `dir.created`
- `dir.deleted`
- `sync.error`
- `sync.ignored`
- `sync.suppressed`
- `sync.stale`
- `writeback.failed`
- `writeback.succeeded`

**Ping:**

```json
{ "type": "ping", "ts": "2026-03-24T12:00:30Z" }
```

Server sends pings every 30 seconds. Client must respond with `pong` within 10 seconds or the connection is closed.

### Client-to-Server Messages

**Subscribe (filter):**

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

All filter fields are optional. If omitted, the client receives all events. Filters use simple glob matching for paths (`*` matches any segment, `**` matches recursively).

**Pong:**

```json
{ "type": "pong" }
```

**Unsubscribe (reset to all):**

```json
{ "type": "unsubscribe" }
```

### Cursor Resumption

On connect, clients can provide a `cursor` query parameter to resume from a known position:

```
GET /v1/workspaces/{workspaceId}/fs/ws?cursor=evt-456
```

The server replays missed events from the cursor position before switching to live push. If the cursor is too old (events have been pruned), the server sends an error frame and falls back to live-only:

```json
{
  "type": "error",
  "code": "cursor_expired",
  "message": "Requested cursor is no longer available. Receiving live events only."
}
```

### Polling Fallback

For environments that don't support WebSocket (some proxies, serverless), clients should fall back to the existing `GET /v1/workspaces/{workspaceId}/fs/events` endpoint with cursor-based polling. No server-side changes needed for this — it's a client implementation concern.

### Implementation Notes

- Use `gorilla/websocket` or `nhooyr.io/websocket` for the WebSocket upgrade.
- The store emits events to an internal channel; the WebSocket handler fans out to connected clients.
- Each workspace has an independent fan-out group. Connections are tracked in a `sync.Map`.
- Max connections per workspace: 50 (configurable).
- Max connections per agent per workspace: 5.
- Idle timeout: 5 minutes with no subscribe/pong activity.

### Store Integration

No new store method required. The WebSocket handler subscribes to the existing event emission path. A new internal interface is added:

```go
type EventSubscriber interface {
    Subscribe(workspaceID string, filter EventFilter) (<-chan FilesystemEvent, func())
}

type EventFilter struct {
    Paths      []string  // glob patterns
    EventTypes []string
    Providers  []string
}
```

The `Store` implements `EventSubscriber`. The returned channel receives events; the returned `func()` is called to unsubscribe.

### OpenAPI Addition

```yaml
/v1/workspaces/{workspaceId}/fs/ws:
  get:
    tags: [Events]
    operationId: websocketEvents
    summary: Real-time filesystem events over WebSocket
    description: |
      Upgrades to a WebSocket connection for real-time event push.
      Falls back to polling /fs/events for non-WebSocket environments.
    security:
      - BearerAuth: [fs:read]
    parameters:
      - $ref: '#/components/parameters/WorkspaceId'
      - name: cursor
        in: query
        required: false
        schema:
          type: string
    responses:
      '101':
        description: WebSocket upgrade successful
      '401':
        $ref: '#/components/responses/Unauthorized'
      '403':
        $ref: '#/components/responses/Forbidden'
      '429':
        $ref: '#/components/responses/RateLimited'
```

---

## 4. Binary File Support

### Purpose

Support storing and retrieving non-text files (images, PDFs, compiled assets) through the same filesystem API by allowing base64-encoded content.

### Write Path

The `encoding` field is added to file write requests:

```json
{
  "contentType": "image/png",
  "content": "iVBORw0KGgoAAAANSUhEUgAA...",
  "encoding": "base64"
}
```

| Field | Values | Default | Description |
|-------|--------|---------|-------------|
| `encoding` | `"utf-8"`, `"base64"` | `"utf-8"` | How `content` is encoded in the JSON payload |

When `encoding` is `"base64"`:
- The server validates that `content` is valid base64.
- Content is stored as the raw base64 string in the store.
- The `encoding` value is persisted in file metadata.
- Size limits apply to the decoded size, not the base64 representation.

### Read Path

The read response includes the `encoding` field:

```json
{
  "path": "/images/logo.png",
  "revision": "rev-abc",
  "contentType": "image/png",
  "content": "iVBORw0KGgoAAAANSUhEUgAA...",
  "encoding": "base64",
  "semantics": {}
}
```

Detection logic:
- If the file was written with `encoding: "base64"`, it is returned with `encoding: "base64"`.
- If no encoding was specified at write time, it defaults to `"utf-8"`.
- The `contentType` field is informational and does not affect encoding detection.

### Store Changes

The `File` struct gains an `Encoding` field:

```go
type File struct {
    Path            string
    Revision        string
    ContentType     string
    Content         string
    Encoding        string        // "utf-8" or "base64"
    Provider        string
    ProviderObjectID string
    LastEditedAt    string
    Semantics       FileSemantics
}
```

The `WriteRequest` struct gains a matching field:

```go
type WriteRequest struct {
    // ... existing fields ...
    Encoding      string  // "utf-8" or "base64"
}
```

Storage is unchanged — content is always stored as a Go `string`. For base64 files, the string contains the base64 representation. No decoding happens at the store layer.

### Validation

- On write with `encoding: "base64"`: validate that content is valid standard base64 (RFC 4648). Reject with `400 bad_request` if invalid.
- Decoded size must not exceed the per-file content limit (10 MB decoded).
- Base64 content size in the JSON payload is approximately 4/3 of the decoded size.

### Content Type Detection

The server does not auto-detect content types. The caller must provide the correct `contentType`. Common binary types:

- `image/png`, `image/jpeg`, `image/gif`, `image/svg+xml`
- `application/pdf`
- `application/octet-stream` (generic binary)

### Impact on Other Endpoints

| Endpoint | Change |
|----------|--------|
| `PUT /fs/file` | Accepts `encoding` field |
| `GET /fs/file` | Returns `encoding` field |
| `GET /fs/query` | No change (doesn't return content) |
| `GET /fs/tree` | No change |
| `GET /fs/export` | Includes `encoding` per file; tar format stores decoded bytes |
| `POST /fs/bulk` | Accepts `encoding` per file |
| WebSocket events | No change (events don't include content) |

### OpenAPI Changes

Add `encoding` to `FileWriteRequest`:

```yaml
FileWriteRequest:
  type: object
  properties:
    # ... existing ...
    encoding:
      type: string
      enum: [utf-8, base64]
      default: utf-8
      description: Content encoding. Use base64 for binary files.
```

Add `encoding` to `FileReadResponse`:

```yaml
FileReadResponse:
  type: object
  properties:
    # ... existing ...
    encoding:
      type: string
      enum: [utf-8, base64]
      default: utf-8
```

---

## HTTP Handler Routing

New routes added to `ServeHTTP`:

```go
case len(parts) == 5 && parts[3] == "fs" && parts[4] == "bulk" && r.Method == http.MethodPost:
    requiredScope = "fs:write"
    route = "bulk_write"

case len(parts) == 5 && parts[3] == "fs" && parts[4] == "export" && r.Method == http.MethodGet:
    requiredScope = "fs:read"
    route = "export"

case len(parts) == 5 && parts[3] == "fs" && parts[4] == "ws" && r.Method == http.MethodGet:
    requiredScope = "fs:read"
    route = "ws_events"
```

---

## Migration & Compatibility

- All changes are additive — no existing endpoints are modified.
- The `encoding` field defaults to `"utf-8"`, so existing clients see no change.
- Files written before binary support have no `encoding` metadata and default to `"utf-8"` on read.
- The WebSocket endpoint is independent of the polling events endpoint; both remain available.

---

## Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `bulk_too_large` | 413 | Bulk request exceeds file count or size limit |
| `invalid_base64` | 400 | Content is not valid base64 when encoding=base64 |
| `export_too_large` | 413 | Workspace exceeds export size threshold |
| `ws_limit_reached` | 429 | WebSocket connection limit per workspace exceeded |
| `cursor_expired` | N/A (WS frame) | Resume cursor no longer available |

---

## Security Considerations

- **Bulk seed** uses the same permission checks as single-file writes. Each file in the batch is validated against effective ACL rules.
- **Export** filters files by caller permissions — no information leakage.
- **WebSocket** validates JWT on upgrade. Token expiry is checked periodically (every 60s); expired tokens trigger graceful close.
- **Binary content** is stored as base64 strings, not raw bytes, avoiding encoding issues in JSON transport. No server-side execution of binary content.
