# SDK Improvements Plan

> **Status (2026-03-14):** TS SDK has full API coverage (24/25 endpoints; 1 internal HMAC-only endpoint excluded by design). NangoHelpers implemented and tested. Python SDK has types + errors defined; client and nango modules need implementation.

## 1. OpenAPI vs TypeScript SDK Gap Analysis

### Endpoints in OpenAPI spec

| # | Endpoint | Method | operationId | TS SDK Method | Status |
|---|----------|--------|-------------|---------------|--------|
| 1 | `/v1/workspaces/{id}/fs/tree` | GET | listTree | `listTree()` | Covered |
| 2 | `/v1/workspaces/{id}/fs/file` | GET | readFile | `readFile()` | Covered |
| 3 | `/v1/workspaces/{id}/fs/file` | PUT | writeFile | `writeFile()` | Covered |
| 4 | `/v1/workspaces/{id}/fs/file` | DELETE | deleteFile | `deleteFile()` | Covered |
| 5 | `/v1/workspaces/{id}/fs/query` | GET | queryFiles | `queryFiles()` | Covered |
| 6 | `/v1/workspaces/{id}/fs/events` | GET | getEvents | `getEvents()` | Covered |
| 7 | `/v1/workspaces/{id}/ops/{opId}` | GET | getOp | `getOp()` | Covered |
| 8 | `/v1/workspaces/{id}/ops` | GET | listOps | `listOps()` | Covered |
| 9 | `/v1/workspaces/{id}/ops/{opId}/replay` | POST | replayOp | `replayOp()` | Covered |
| 10 | `/v1/workspaces/{id}/sync/status` | GET | getSyncStatus | `getSyncStatus()` | Covered |
| 11 | `/v1/workspaces/{id}/sync/ingress` | GET | getSyncIngressStatus | `getSyncIngressStatus()` | Covered |
| 12 | `/v1/workspaces/{id}/sync/dead-letter` | GET | listSyncDeadLetters | `getSyncDeadLetters()` | Covered |
| 13 | `/v1/workspaces/{id}/sync/dead-letter/{id}` | GET | getSyncDeadLetter | `getSyncDeadLetter()` | Covered |
| 14 | `/v1/workspaces/{id}/sync/dead-letter/{id}/replay` | POST | replaySyncDeadLetter | `replaySyncDeadLetter()` | Covered |
| 15 | `/v1/workspaces/{id}/sync/dead-letter/{id}/ack` | POST | ackSyncDeadLetter | `ackSyncDeadLetter()` | Covered |
| 16 | `/v1/workspaces/{id}/sync/refresh` | POST | triggerSyncRefresh | `triggerSyncRefresh()` | Covered |
| 17 | `/v1/internal/webhook-envelopes` | POST | ingestWebhookEnvelope | — | **Missing** (internal, HMAC-only) |
| 18 | `/v1/admin/backends` | GET | getBackendStatus | `getBackendStatus()` | Covered |
| 19 | `/v1/admin/ingress` | GET | getAdminIngressStatus | `getAdminIngressStatus()` | Covered |
| 20 | `/v1/admin/sync` | GET | getAdminSyncStatus | `getAdminSyncStatus()` | Covered |
| 21 | `/v1/admin/replay/envelope/{id}` | POST | replayAdminEnvelope | `replayAdminEnvelope()` | Covered |
| 22 | `/v1/admin/replay/op/{id}` | POST | replayAdminOp | `replayAdminOp()` | Covered |
| 23 | `/v1/workspaces/{id}/webhooks/ingest` | POST | ingestGenericWebhook | `ingestWebhook()` | Covered |
| 24 | `/v1/workspaces/{id}/writeback/pending` | GET | listPendingWritebacks | `listPendingWritebacks()` | Covered |
| 25 | `/v1/workspaces/{id}/writeback/{itemId}/ack` | POST | acknowledgeWriteback | `ackWriteback()` | Covered |

### Previously missing SDK methods (now implemented)

The following 3 public endpoints were added to `client.ts` along with their types in `types.ts`:

- `ingestWebhook(input: IngestWebhookInput)` — generic webhook ingestion (POST `/webhooks/ingest`)
- `listPendingWritebacks(workspaceId, correlationId?, signal?)` — list pending writeback items (GET `/writeback/pending`)
- `ackWriteback(input: AckWritebackInput)` — acknowledge a writeback result (POST `/writeback/{itemId}/ack`)

All 3 methods have test coverage in `client.test.ts`.

### Note on internal endpoint

`POST /v1/internal/webhook-envelopes` uses HMAC auth (not Bearer JWT) and is intended only for relay-cloud. It should NOT be added to the public SDK. If needed, a separate `InternalRelayFileClient` class could expose it.

---

## 2. Python SDK Design

Mirror the TypeScript SDK with Pythonic conventions.

### Installation

```bash
pip install relayfile
```

### Client interface

```python
from relayfile import RelayFileClient, WriteFileInput

client = RelayFileClient(
    base_url="https://relayfile.agent-relay.com",
    token="eyJ...",  # or callable: lambda: get_token()
    retry={"max_retries": 3, "base_delay": 0.1},
)

# Filesystem
tree = await client.list_tree("ws_acme", path="/zendesk", depth=2)
file = await client.read_file("ws_acme", "/zendesk/tickets/48291.json")
result = await client.write_file(WriteFileInput(
    workspace_id="ws_acme",
    path="/zendesk/tickets/48291.json",
    base_revision=file.revision,
    content='{"status": "solved"}',
    content_type="application/json",
))
result = await client.delete_file(DeleteFileInput(
    workspace_id="ws_acme",
    path="/old/file.json",
    base_revision="rev_abc",
))
files = await client.query_files("ws_acme", provider="zendesk", properties={"provider.status": "open"})

# Events
events = await client.get_events("ws_acme", provider="zendesk", limit=50)

# Operations
op = await client.get_op("ws_acme", "op_123")
ops = await client.list_ops("ws_acme", status="failed")
await client.replay_op("ws_acme", "op_123")

# Sync
status = await client.get_sync_status("ws_acme", provider="zendesk")
ingress = await client.get_sync_ingress_status("ws_acme")
dead_letters = await client.get_sync_dead_letters("ws_acme")
dl = await client.get_sync_dead_letter("ws_acme", "env_456")
await client.replay_sync_dead_letter("ws_acme", "env_456")
await client.ack_sync_dead_letter("ws_acme", "env_456")
await client.trigger_sync_refresh("ws_acme", provider="zendesk", reason="manual")

# Webhook ingest (new)
await client.ingest_webhook("ws_acme", IngestWebhookInput(
    provider="zendesk",
    event_type="file.updated",
    path="/zendesk/tickets/48291.json",
    data={"content": "..."},
))

# Writeback (new)
pending = await client.list_pending_writebacks("ws_acme")
await client.ack_writeback("ws_acme", "wb_789", success=True)

# Admin
backends = await client.get_backend_status()
admin_ingress = await client.get_admin_ingress_status(workspace_id="ws_acme")
admin_sync = await client.get_admin_sync_status()
await client.replay_admin_envelope("env_456")
await client.replay_admin_op("op_123")
```

### Error hierarchy

```python
class RelayFileApiError(Exception):
    status: int
    code: str
    message: str
    correlation_id: str
    details: dict | None

class RevisionConflictError(RelayFileApiError):
    expected_revision: str
    current_revision: str
    current_content_preview: str | None

class InvalidStateError(RelayFileApiError): ...
class QueueFullError(RelayFileApiError):
    retry_after_seconds: int | None

class PayloadTooLargeError(RelayFileApiError): ...
```

### Implementation status

| Component | File | Status |
|-----------|------|--------|
| `pyproject.toml` | `sdk/relayfile-sdk-py/pyproject.toml` | Done — hatchling build, httpx dep, Python >=3.10 |
| Types | `sdk/relayfile-sdk-py/src/relayfile/types.py` | Done — all dataclasses mirror TS types |
| Errors | `sdk/relayfile-sdk-py/src/relayfile/errors.py` | Done — `RelayFileApiError` hierarchy |
| `__init__.py` | `sdk/relayfile-sdk-py/src/relayfile/__init__.py` | Done — exports defined (imports `client.py` and `nango.py` which don't exist yet) |
| `client.py` | `sdk/relayfile-sdk-py/src/relayfile/client.py` | **Not started** |
| `nango.py` | `sdk/relayfile-sdk-py/src/relayfile/nango.py` | **Not started** |
| Tests | `sdk/relayfile-sdk-py/tests/` | **Not started** |

### Implementation notes

- Use `httpx.AsyncClient` for HTTP with connection pooling
- Provide both sync and async clients (`RelayFileClient` and `AsyncRelayFileClient`)
- Use `dataclasses` for input/output types (not Pydantic — keep dependency-light)
- Retry logic mirrors TS SDK: exponential backoff with jitter, honor `Retry-After`
- Token provider accepts `str | Callable[[], str] | Callable[[], Awaitable[str]]`
- The sync `RelayFileClient` should wrap `AsyncRelayFileClient` using `asyncio.run()` or a dedicated event loop to avoid nested-loop issues

---

## 3. Nango Convenience Helpers

High-level helpers that wrap the low-level SDK for common Nango Bridge patterns. Available in both TS and Python SDKs.

### TypeScript

```typescript
import { RelayFileClient, NangoHelpers } from "@agentworkforce/relayfile-sdk";

const client = new RelayFileClient({ baseUrl, token });
const nango = new NangoHelpers(client);

// Ingest a Nango webhook payload directly
// Computes the canonical path, maps metadata to properties
await nango.ingestNangoWebhook("ws_acme", {
  connectionId: "conn_zendesk_acme",
  integrationId: "zendesk-support",
  providerConfigKey: "zendesk",
  model: "tickets",
  objectId: "48291",
  eventType: "updated",
  payload: { /* raw Nango webhook body */ },
});

// Get all files from a specific provider
const tickets = await nango.getProviderFiles("ws_acme", {
  provider: "zendesk",
  objectType: "tickets",     // optional — filters to /zendesk/tickets/
  status: "open",            // optional — filters by provider.status property
  limit: 100,
});

// Watch for new events from a provider (polling iterator)
for await (const event of nango.watchProviderEvents("ws_acme", {
  provider: "github",
  pollIntervalMs: 5000,
  signal: abortController.signal,
})) {
  console.log(`${event.type} at ${event.path}`);
}
```

### Python

```python
from relayfile import RelayFileClient
from relayfile.nango import NangoHelpers

client = RelayFileClient(base_url=base_url, token=token)
nango = NangoHelpers(client)

# Ingest
await nango.ingest_nango_webhook("ws_acme",
    connection_id="conn_zendesk_acme",
    integration_id="zendesk-support",
    provider_config_key="zendesk",
    model="tickets",
    object_id="48291",
    event_type="updated",
    payload={...},
)

# Query provider files
tickets = await nango.get_provider_files("ws_acme",
    provider="zendesk",
    object_type="tickets",
    status="open",
)

# Watch events (async generator)
async for event in nango.watch_provider_events("ws_acme", provider="github", poll_interval=5.0):
    print(f"{event.type} at {event.path}")
```

### Helper method specifications

#### `ingestNangoWebhook(workspaceId, options)`

1. Compute canonical path from `providerConfigKey`, `model`, and `objectId` using the path conventions from the Nango Bridge design
2. Build semantic properties from Nango connection metadata
3. Extract cross-references from the payload to populate `semantics.relations`
4. Call `client.ingestWebhook()` with the assembled envelope
5. Return the `QueuedResponse`

#### `getProviderFiles(workspaceId, options)`

1. Build the base path from `provider` and optional `objectType` (e.g., `/zendesk/tickets/`)
2. Map `status` to `properties: { "provider.status": status }`
3. Call `client.queryFiles()` with path prefix and property filters
4. Auto-paginate if `limit` exceeds page size
5. Return full list of `FileQueryItem[]`

#### `watchProviderEvents(workspaceId, options)`

1. Call `client.getEvents()` with provider filter
2. Yield each new event
3. Store cursor for next poll
4. Sleep for `pollIntervalMs` between polls
5. Respect `AbortSignal` / cancellation for clean shutdown
6. On error: log and continue (configurable via `onError` callback)

---

## 4. Remaining Work

### High Priority

1. **Python SDK `client.py`** — Implement `RelayFileClient` and `AsyncRelayFileClient` covering all 24 public endpoints. Mirror the TS SDK's retry logic, error mapping, and correlation ID generation.
2. **Python SDK `nango.py`** — Port `NangoHelpers` / `AsyncNangoHelpers` from TS. Include `ingest_nango_webhook`, `get_provider_files`, and `watch_provider_events` (async generator).
3. **Python SDK tests** — Use `pytest-asyncio` + `respx` (httpx mock). Cover all client methods, error cases, retry behavior, and NangoHelpers.

### Medium Priority

4. **TS SDK: `AccessTokenProvider` refresh hook** — Add optional `onTokenRefresh` callback so callers get notified when a token is re-resolved (useful for token caching/metrics).
5. **TS SDK: batch operations** — Consider `batchWriteFiles()` and `batchDeleteFiles()` if the API adds batch endpoints.
6. **Both SDKs: request/response interceptors** — Middleware hooks for logging, metrics, and custom header injection.

### Low Priority

7. **OpenAPI codegen validation** — CI step that diffs the OpenAPI spec against SDK method signatures to catch drift.
8. **SDK versioning alignment** — Ensure TS SDK (`package.json`) and Python SDK (`pyproject.toml`) versions track the API version.
9. **SDK documentation site** — Auto-generate API docs from TSDoc/docstrings.
