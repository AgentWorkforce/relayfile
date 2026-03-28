/**
 * relayfile-bulk-and-export.ts
 *
 * Adds bulk seeding and workspace export to relayfile.
 * These are prerequisites for cloud workflow integration:
 *   - Bulk seed: upload 200+ project files in a single API call (tarball or batch)
 *   - Export: download the entire workspace as a tar or git patch
 *
 * Also adds WebSocket push for real-time events (upgrade from polling)
 * and binary file support.
 *
 * Run: agent-relay run workflows/relayfile-bulk-and-export.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = process.env.RELAYFILE_PATH || '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';

async function main() {
const result = await workflow('relayfile-bulk-and-export')
  .description('Add bulk seed, workspace export, WebSocket events, and binary file support to relayfile')
  .pattern('dag')
  .channel('wf-relayfile-bulk')
  .maxConcurrency(3)
  .timeout(3_600_000)

  .agent('architect', {
    cli: 'claude',
    preset: 'lead',
    role: 'Design API endpoints, review implementation',
    cwd: RELAYFILE,
  })
  .agent('go-backend', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement Go server endpoints and store methods',
    cwd: RELAYFILE,
  })
  .agent('go-websocket', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement WebSocket event streaming',
    cwd: RELAYFILE,
  })
  .agent('sdk-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Update TypeScript SDK with new methods',
    cwd: RELAYFILE,
  })
  .agent('test-writer', {
    cli: 'codex',
    preset: 'worker',
    role: 'Write tests for new functionality',
    cwd: RELAYFILE,
  })

  // ── Phase 1: Read existing code ────────────────────────────────────

  .step('read-store', {
    type: 'deterministic',
    command: `grep -n "func.*Store\\b\\|func.*Write\\|func.*Read\\|func.*Delete\\|func.*List\\|func.*Event" ${RELAYFILE}/internal/relayfile/store.go | head -40`,
    captureOutput: true,
  })

  .step('read-http-server', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/httpapi/server.go`,
    captureOutput: true,
  })

  .step('read-openapi', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/openapi/relayfile-v1.openapi.yaml`,
    captureOutput: true,
  })

  .step('read-ts-sdk', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/packages/sdk/typescript/src/client.ts`,
    captureOutput: true,
  })

  .step('read-ts-types', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/packages/sdk/typescript/src/types.ts`,
    captureOutput: true,
  })

  .step('read-syncer', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/mountsync/syncer.go`,
    captureOutput: true,
  })

  .step('read-store-full', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/relayfile/store.go`,
    captureOutput: true,
  })

  // ── Phase 2: Architect designs the new endpoints ───────────────────

  .step('design-endpoints', {
    agent: 'architect',
    dependsOn: ['read-store', 'read-http-server', 'read-openapi'],
    task: `Design the bulk seed, export, WebSocket, and binary support endpoints.

Current store methods:
{{steps.read-store.output}}

Current HTTP server:
{{steps.read-http-server.output}}

Current OpenAPI spec:
{{steps.read-openapi.output}}

Write a design doc at ${RELAYFILE}/docs/bulk-export-design.md:

1. **Bulk Seed** — POST /v1/workspaces/{workspaceId}/fs/bulk
   - Accept: application/json with array of files:
     { files: [{ path: string, contentType: string, content: string }] }
   - Also accept: multipart/form-data with a tar.gz file
   - Atomic — all files written in one operation, single event per file
   - Returns: { imported: number, errors: [{path, error}] }
   - Store method: BulkWrite(workspaceId string, files []BulkWriteFile) (int, []error)

2. **Workspace Export** — GET /v1/workspaces/{workspaceId}/fs/export
   - Query params: format=tar|json|patch
   - format=tar: returns application/gzip tarball of all files
   - format=json: returns JSON array of all files with content
   - format=patch: returns a unified diff against an empty tree (like git diff)
   - Store method: ExportWorkspace(workspaceId string) ([]File, error)

3. **WebSocket Events** — GET /v1/workspaces/{workspaceId}/fs/ws
   - Upgrade to WebSocket
   - Server pushes JSON events: { type: file.created"|"file.updated"|"file.deleted", path, revision, timestamp }
   - Client can send: { type: "subscribe", filter?: { paths?: string[] } }
   - Falls back to polling for environments without WebSocket support

4. **Binary File Support**
   - Content field supports base64 encoding for binary files
   - New field on write: encoding?: "utf-8" | "base64" (default: "utf-8")
   - On read: detect content type, return encoding field
   - Store: content stored as-is (string), encoding tracked in metadata

Write the complete design.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 3: Parallel implementation ──────────────────────────────

  .step('implement-bulk-and-export', {
    agent: 'go-backend',
    dependsOn: ['design-endpoints', 'read-store-full', 'read-http-server'],
    task: `Implement bulk seed, export, and binary file support in the Go server.

Design:
{{steps.design-endpoints.output}}

Current store:
{{steps.read-store-full.output}}

Current HTTP server:
{{steps.read-http-server.output}}

Changes:

1. Edit ${RELAYFILE}/internal/relayfile/store.go — add methods:
   - BulkWrite(workspaceID string, files []BulkWriteFile) (int, []BulkWriteError)
     BulkWriteFile: { Path, ContentType, Content, Encoding string }
     Writes all files, emitting events for each. No If-Match check (seed overwrites).
   - ExportWorkspace(workspaceID string) ([]File, error)
     Returns all files in the workspace with content.

2. Edit ${RELAYFILE}/internal/httpapi/server.go — add route handlers:
   - POST /v1/workspaces/{workspaceId}/fs/bulk → handleBulkWrite
     Parse JSON body { files: [...] }, call store.BulkWrite, return summary.
   - GET /v1/workspaces/{workspaceId}/fs/export → handleExport
     Query param format=tar|json|patch.
     For tar: use archive/tar + compress/gzip to stream a tarball.
     For json: return JSON array of files.
     For patch: generate unified diff format.
   - Update handleWriteFile and handleReadFile for binary encoding support:
     Accept encoding" field in write request body.
     Include "encoding" field in read response when base64.

3. Add encoding field to File struct in store.go:
   Encoding string \`json:"encoding,omitempty"\` // "utf-8" (default) or "base64"

Write all changes to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-websocket', {
    agent: 'go-websocket',
    dependsOn: ['design-endpoints', 'read-store-full', 'read-http-server'],
    task: `Implement WebSocket event streaming in the Go server.

Design:
{{steps.design-endpoints.output}}

Current store (for event subscription):
{{steps.read-store-full.output}}

Current HTTP server:
{{steps.read-http-server.output}}

Changes:

1. Add dependency: go get nhooyr.io/websocket (lightweight WebSocket library for Go)
   Or use golang.org/x/net/websocket. Pick whichever is simpler.

2. Edit ${RELAYFILE}/internal/httpapi/server.go — add WebSocket handler:
   - Route: /v1/workspaces/{workspaceId}/fs/ws
   - Upgrade HTTP to WebSocket
   - Auth: validate JWT from query param ?token= (WebSocket can't send headers)
   - On connect: send all recent events (last 100) as catch-up
   - Subscribe to store events for this workspace
   - Push events as JSON messages: { type, path, revision, timestamp }
   - Handle client messages: { type: ping" } → respond with { type: "pong" }
   - Clean disconnect on context cancellation

3. Add event subscription to store:
   - Add Subscribe(workspaceID string, ch chan<- Event) func()
     Returns an unsubscribe function.
   - On each Write/Delete, fan out to subscribers.
   - Use sync.Map or mutex-protected map of workspace -> []chan.

4. Create ${RELAYFILE}/internal/httpapi/websocket.go for the handler to keep server.go clean.

Write all changes to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-sdk-updates', {
    agent: 'sdk-dev',
    dependsOn: ['design-endpoints', 'read-ts-sdk', 'read-ts-types'],
    task: `Update the TypeScript SDK with new methods for bulk seed, export, and WebSocket.

Design:
{{steps.design-endpoints.output}}

Current SDK client:
{{steps.read-ts-sdk.output}}

Current SDK types:
{{steps.read-ts-types.output}}

Changes:

1. Edit ${RELAYFILE}/packages/sdk/typescript/src/types.ts — add:
   - BulkWriteFile: { path: string; contentType?: string; content: string; encoding?: utf-8" | "base64" }
   - BulkWriteInput: { workspaceId: string; files: BulkWriteFile[]; correlationId?: string; signal?: AbortSignal }
   - BulkWriteResponse: { imported: number; errors: { path: string; error: string }[] }
   - ExportFormat: "tar" | "json" | "patch"
   - ExportOptions: { workspaceId: string; format?: ExportFormat; correlationId?: string; signal?: AbortSignal }
   - ExportJsonResponse: { files: FileReadResponse[] }
   - FilesystemEvent (already exists? add if missing): { eventId, type, path, revision, timestamp }
   - Add encoding?: "utf-8" | "base64" to WriteFileInput and FileReadResponse

2. Edit ${RELAYFILE}/packages/sdk/typescript/src/client.ts — add methods:
   - bulkWrite(input: BulkWriteInput): Promise<BulkWriteResponse>
     POST /v1/workspaces/{workspaceId}/fs/bulk with JSON body
   - exportWorkspace(options: ExportOptions): Promise<ExportJsonResponse | Blob>
     GET /v1/workspaces/{workspaceId}/fs/export?format=...
     For json: parse as JSON. For tar/patch: return raw response.
   - connectWebSocket(workspaceId: string, options?: { token?: string, onEvent?: (event: FilesystemEvent) => void }): WebSocketConnection
     Connect to ws:// or wss:// endpoint
     Return object with { close(), on(event, handler) }

3. Re-export new types from ${RELAYFILE}/packages/sdk/typescript/src/index.ts

Write all changes to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 4: Update mount syncer for WebSocket ─────────────────────

  .step('update-syncer-websocket', {
    agent: 'go-backend',
    dependsOn: ['implement-websocket', 'read-syncer'],
    task: `Update the mount syncer to use WebSocket events when available, falling back to polling.

Current syncer:
{{steps.read-syncer.output}}

Edit ${RELAYFILE}/internal/mountsync/syncer.go:

1. Add a WebSocket mode to Syncer:
   - New field: wsConn (WebSocket connection, nil if not connected)
   - On SyncOnce(): if wsConn is nil, try to connect to ws:// endpoint
   - If WebSocket is available: listen for events and apply them immediately (no polling)
   - If WebSocket fails or disconnects: fall back to existing polling behavior
   - This makes the daemon opportunistically real-time when the server supports it

2. Add private method connectWebSocket(ctx) error:
   - Dial ws://{baseURL}/v1/workspaces/{workspace}/fs/ws?token={token}
   - Start goroutine to read events and apply via applyRemoteFile/applyRemoteDelete
   - On disconnect: set wsConn = nil, fall back to polling

3. Update the relayfile-mount CLI (cmd/relayfile-mount/main.go):
   - Add --websocket flag (default: true) to enable/disable WebSocket mode
   - When WebSocket mode is on and polling interval is set, use WebSocket for
     real-time events and polling as a safety net (reconciliation every N intervals)

Write all changes to disk. Keep the polling fallback working — WebSocket is an optimization, not a requirement.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 5: Tests ─────────────────────────────────────────────────

  .step('write-tests', {
    agent: 'test-writer',
    dependsOn: ['implement-bulk-and-export', 'implement-websocket', 'implement-sdk-updates'],
    task: `Write Go tests for the new functionality.

Create/update these test files:

1. ${RELAYFILE}/internal/relayfile/store_test.go — add tests:
   - TestBulkWrite: seed 10 files, verify all present in tree, verify events emitted
   - TestBulkWriteOverwrite: bulk write same paths twice, verify latest content
   - TestExportWorkspace: seed files, export, verify all returned with content
   - TestBinaryEncoding: write base64 content, read back, verify encoding field

2. ${RELAYFILE}/internal/httpapi/server_test.go — add HTTP tests:
   - TestBulkWriteEndpoint: POST /fs/bulk with 5 files, verify 200 + imported count
   - TestExportJSON: seed files, GET /fs/export?format=json, verify response
   - TestExportTar: seed files, GET /fs/export?format=tar, verify gzip response
   - TestWebSocketEvents: connect WebSocket, write a file via API, verify event received

3. ${RELAYFILE}/internal/mountsync/syncer_test.go — add sync test:
   - TestBulkSeedThenSync: use bulk API to seed, run SyncOnce, verify local files

Follow the existing test patterns in each file.
Use the standard Go testing package + testify if already a dependency, otherwise stdlib.

Write all test files to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 6: Verify ───────────────────────────────────────────────

  .step('verify-files', {
    type: 'deterministic',
    dependsOn: ['write-tests', 'update-syncer-websocket'],
    command: `cd ${RELAYFILE} && echo "=== New/modified files ===" && \
grep -rl "BulkWrite\|ExportWorkspace\|handleBulkWrite\|handleExport\|websocket\|WebSocket" internal/ --include="*.go" | sort && \
echo "" && echo "=== SDK updates ===" && \
grep -c "bulkWrite\|exportWorkspace\|connectWebSocket" packages/sdk/typescript/src/client.ts && \
echo "" && echo "=== Build check ===" && \
go build ./... 2>&1 | tail -10; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('run-tests', {
    type: 'deterministic',
    dependsOn: ['verify-files'],
    command: `cd ${RELAYFILE} && go test ./... -count=1 2>&1 | tail -30; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('fix-issues', {
    agent: 'architect',
    dependsOn: ['run-tests'],
    task: `Fix any build or test failures.

Test output:
{{steps.run-tests.output}}

Build output:
{{steps.verify-files.output}}

If EXIT: 0 for both, summarize what was added.
Otherwise read the failing files and fix them.
Run go test ./... again to verify.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 7: Update OpenAPI spec ──────────────────────────────────

  .step('update-openapi', {
    agent: 'architect',
    dependsOn: ['fix-issues'],
    task: `Update the OpenAPI spec to include the new endpoints.

Read the current spec: cat ${RELAYFILE}/openapi/relayfile-v1.openapi.yaml

Add these endpoints to the spec:

1. POST /v1/workspaces/{workspaceId}/fs/bulk
   - requestBody: { files: [{ path, contentType, content, encoding? }] }
   - responses: 200 { imported, errors }

2. GET /v1/workspaces/{workspaceId}/fs/export
   - parameters: format (tar|json|patch)
   - responses: 200 (varies by format)

3. GET /v1/workspaces/{workspaceId}/fs/ws
   - description: WebSocket upgrade for real-time events

4. Update PUT /fs/file to include encoding field
5. Update GET /fs/file response to include encoding field

Write the updated spec to disk.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nBulk + Export workflow: ${result.status}`);
}

main().catch(console.error);
