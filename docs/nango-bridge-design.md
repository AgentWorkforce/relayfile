# Nango Bridge Design

How Nango webhooks flow into Relayfile so AI agents can access SaaS data as files.

> **Status (2026-03-14):** Core ingest + writeback endpoints are live in the Go HTTP API (`internal/httpapi/server.go`). The TS SDK exposes `ingestWebhook()`, `listPendingWritebacks()`, and `ackWriteback()`. High-level `NangoHelpers` class is implemented in `sdk/relayfile-sdk/src/nango.ts` with `ingestNangoWebhook`, `getProviderFiles`, and `watchProviderEvents`. Python SDK types are defined; client + nango modules are in progress.

## Overview

Nango is a SaaS integration platform that normalizes OAuth, token refresh, and webhook delivery for 250+ providers. The Nango Bridge is a lightweight service that receives Nango webhook events and translates them into Relayfile ingestion calls.

```
Provider (Zendesk, Shopify, …)
  │  webhook
  ▼
Nango  ──── normalizes auth, delivers webhook ────►  Nango Bridge
                                                        │
                                            POST /v1/workspaces/{id}/webhooks/ingest
                                                        │
                                                        ▼
                                                   Relayfile
                                                  (queue → store)
                                                        │
                                              AI agent reads files
```

## Webhook Envelope Format

Nango delivers a webhook to the bridge with connection metadata. The bridge maps it to the existing generic ingest endpoint:

```
POST /v1/workspaces/{workspaceId}/webhooks/ingest
Authorization: Bearer <workspace-scoped JWT with sync:read>
X-Correlation-Id: nango_{connectionId}_{deliveryId}
```

```json
{
  "provider": "zendesk",
  "event_type": "file.updated",
  "path": "/zendesk/tickets/48291.json",
  "delivery_id": "nango_evt_abc123",
  "timestamp": "2026-03-14T10:30:00Z",
  "data": {
    "content": "{ ... provider object ... }",
    "title": "Ticket #48291"
  },
  "headers": {
    "X-Nango-Connection-Id": "conn_xyz",
    "X-Nango-Integration-Id": "zendesk-support"
  }
}
```

The bridge is responsible for:
1. Receiving the raw Nango webhook payload
2. Extracting the provider name, object type, and object ID
3. Computing the canonical file path (see conventions below)
4. Serializing the provider object as the file content
5. Mapping Nango connection metadata to Relayfile semantic properties
6. POSTing the envelope to the ingest endpoint

## File Path Conventions

Each provider gets a top-level directory. Object types become subdirectories. Object IDs become filenames.

| Provider | Object Type | Path Pattern | Example |
|----------|-------------|--------------|---------|
| Zendesk | Tickets | `/zendesk/tickets/{id}.json` | `/zendesk/tickets/48291.json` |
| Zendesk | Users | `/zendesk/users/{id}.json` | `/zendesk/users/901.json` |
| Shopify | Orders | `/shopify/orders/{id}.json` | `/shopify/orders/5001.json` |
| Shopify | Products | `/shopify/products/{id}.json` | `/shopify/products/789.json` |
| Shopify | Customers | `/shopify/customers/{id}.json` | `/shopify/customers/42.json` |
| GitHub | Issues | `/github/repos/{owner}/{repo}/issues/{num}.json` | `/github/repos/acme/api/issues/137.json` |
| GitHub | Pull Requests | `/github/repos/{owner}/{repo}/pulls/{num}.json` | `/github/repos/acme/api/pulls/42.json` |
| GitHub | Commits | `/github/repos/{owner}/{repo}/commits/{sha}.json` | `/github/repos/acme/api/commits/a1b2c3.json` |
| Stripe | Payments | `/stripe/payments/{id}.json` | `/stripe/payments/pi_3Ox.json` |
| Stripe | Customers | `/stripe/customers/{id}.json` | `/stripe/customers/cus_N1.json` |
| Stripe | Subscriptions | `/stripe/subscriptions/{id}.json` | `/stripe/subscriptions/sub_1P.json` |
| Stripe | Invoices | `/stripe/invoices/{id}.json` | `/stripe/invoices/in_1Q.json` |
| Slack | Messages | `/slack/channels/{channel}/messages/{ts}.json` | `/slack/channels/C01ABC/messages/1710400200.json` |
| Linear | Issues | `/linear/issues/{id}.json` | `/linear/issues/ENG-142.json` |
| Jira | Issues | `/jira/projects/{key}/issues/{key}.json` | `/jira/projects/PROJ/issues/PROJ-99.json` |
| HubSpot | Contacts | `/hubspot/contacts/{id}.json` | `/hubspot/contacts/501.json` |
| HubSpot | Deals | `/hubspot/deals/{id}.json` | `/hubspot/deals/301.json` |
| Salesforce | Records | `/salesforce/{sobject}/{id}.json` | `/salesforce/Account/001xx.json` |

### Path rules
- All paths are lowercase except provider-native casing (GitHub owner/repo, Salesforce SObject names)
- All files use `.json` extension — content is always `application/json`
- Nested resources use nested directories (e.g., GitHub repos → owner → repo → issues)
- Deleted objects: the bridge sends `event_type: "file.deleted"` with the same path

## Semantic Properties Mapping

Nango connection metadata maps to Relayfile `semantics.properties` on each file:

```json
{
  "semantics": {
    "properties": {
      "nango.connection_id": "conn_xyz",
      "nango.integration_id": "zendesk-support",
      "nango.provider_config_key": "zendesk",
      "provider": "zendesk",
      "provider.object_type": "ticket",
      "provider.object_id": "48291",
      "provider.status": "open",
      "provider.updated_at": "2026-03-14T10:30:00Z"
    },
    "relations": [
      "/zendesk/users/901.json"
    ]
  }
}
```

### Standard property keys

| Key | Source | Description |
|-----|--------|-------------|
| `nango.connection_id` | Nango webhook header | Links back to Nango connection |
| `nango.integration_id` | Nango webhook header | Which Nango integration fired |
| `nango.provider_config_key` | Nango webhook header | Provider config in Nango |
| `provider` | Derived | Canonical provider name |
| `provider.object_type` | Derived | Object type (ticket, order, issue, etc.) |
| `provider.object_id` | Derived | Provider-native ID |
| `provider.status` | Provider payload | Object status if applicable |
| `provider.updated_at` | Provider payload | Last modification timestamp |
| `provider.created_at` | Provider payload | Creation timestamp |

### Relations

Cross-references between objects are stored as `semantics.relations`:
- A Zendesk ticket assigned to user 901 → relation to `/zendesk/users/901.json`
- A GitHub PR referencing issue 137 → relation to `/github/repos/acme/api/issues/137.json`
- A Stripe invoice linked to customer cus_N1 → relation to `/stripe/customers/cus_N1.json`

Agents can use `GET /v1/workspaces/{id}/fs/query?relation=/zendesk/users/901.json` to find all files related to a user.

## Writeback Flow

When an AI agent edits a file (e.g., updates a Zendesk ticket status), the change must propagate back to the source provider.

```
Agent writes file (PUT /v1/workspaces/{id}/fs/file?path=/zendesk/tickets/48291.json)
  │
  ▼
Relayfile queues writeback op (status: pending)
  │
  ▼
Nango Bridge polls GET /v1/workspaces/{id}/writeback/pending
  │
  ▼
Bridge calls Nango Action API to apply change to provider
  │  (e.g., PATCH zendesk ticket via Nango proxy)
  │
  ▼
Bridge acknowledges: POST /v1/workspaces/{id}/writeback/{itemId}/ack
  { "success": true }  or  { "success": false, "error": "..." }
  │
  ▼
Relayfile marks op as succeeded/failed
Agent sees updated op status via GET /v1/workspaces/{id}/ops/{opId}
```

### Writeback bridge worker

The bridge runs a polling loop:
1. `GET /v1/workspaces/{id}/writeback/pending` — fetch pending items
2. For each item, extract the provider and path to determine the Nango action
3. Diff the file content against the previous revision to compute the minimal change
4. Call Nango's proxy API: `PATCH /proxy/{integration}/{endpoint}` with the provider-native payload
5. On success: `POST /v1/workspaces/{id}/writeback/{itemId}/ack` with `success: true`
6. On failure: ack with `success: false` and error message; Relayfile will retry or dead-letter

### Writeback mapping per provider

| Provider | Object Type | Nango Action | Notes |
|----------|-------------|--------------|-------|
| Zendesk | Tickets | `PATCH /api/v2/tickets/{id}` | Status, priority, assignee, comment |
| Shopify | Orders | Read-only | Orders cannot be modified via API |
| GitHub | Issues | `PATCH /repos/{owner}/{repo}/issues/{num}` | Title, body, state, labels, assignees |
| GitHub | Pull Requests | `PATCH /repos/{owner}/{repo}/pulls/{num}` | Title, body, state |
| Stripe | Customers | `POST /v1/customers/{id}` | Metadata, description, email |
| Stripe | Subscriptions | `POST /v1/subscriptions/{id}` | Cancel, update items |
| Linear | Issues | GraphQL mutation `issueUpdate` | Title, description, state, assignee, priority |
| Jira | Issues | `PUT /rest/api/3/issue/{key}` | Summary, description, status transition |
| Salesforce | Records | `PATCH /services/data/v59.0/sobjects/{type}/{id}` | Any writable field |

## Provider-Specific File Schemas

### Zendesk Ticket

```json
{
  "id": 48291,
  "subject": "Cannot login to dashboard",
  "description": "User reports 403 error when...",
  "status": "open",
  "priority": "high",
  "type": "incident",
  "requester": { "id": 901, "name": "Jane Doe", "email": "jane@example.com" },
  "assignee": { "id": 55, "name": "Support Agent" },
  "tags": ["login", "dashboard", "urgent"],
  "created_at": "2026-03-13T08:00:00Z",
  "updated_at": "2026-03-14T10:30:00Z",
  "comments": [
    { "id": 1, "author_id": 901, "body": "I keep getting 403...", "created_at": "2026-03-13T08:00:00Z" }
  ]
}
```

### Shopify Order

```json
{
  "id": 5001,
  "order_number": "#1042",
  "financial_status": "paid",
  "fulfillment_status": "unfulfilled",
  "total_price": "129.99",
  "currency": "USD",
  "customer": { "id": 42, "email": "buyer@example.com", "first_name": "John" },
  "line_items": [
    { "id": 100, "title": "Widget Pro", "quantity": 2, "price": "49.99" },
    { "id": 101, "title": "Shipping", "quantity": 1, "price": "30.01" }
  ],
  "created_at": "2026-03-12T15:00:00Z",
  "updated_at": "2026-03-14T09:00:00Z"
}
```

### GitHub Issue

```json
{
  "number": 137,
  "title": "Fix race condition in worker pool",
  "state": "open",
  "body": "When two workers grab the same job...",
  "user": { "login": "alice", "id": 12345 },
  "assignees": [{ "login": "bob", "id": 67890 }],
  "labels": [{ "name": "bug" }, { "name": "P1" }],
  "milestone": { "title": "v2.1" },
  "comments_count": 3,
  "created_at": "2026-03-10T12:00:00Z",
  "updated_at": "2026-03-14T08:00:00Z"
}
```

### Stripe Payment Intent

```json
{
  "id": "pi_3OxABC",
  "amount": 5000,
  "currency": "usd",
  "status": "succeeded",
  "customer": "cus_N1xyz",
  "description": "Order #1042",
  "payment_method": "pm_card_visa",
  "metadata": { "order_id": "5001" },
  "created": 1710400200,
  "charges": {
    "data": [
      { "id": "ch_3Ox", "amount": 5000, "status": "succeeded", "receipt_url": "https://..." }
    ]
  }
}
```

## Configuration

The Nango Bridge needs a config mapping per workspace:

```yaml
# nango-bridge-config.yaml
workspaces:
  - workspace_id: "ws_acme"
    relayfile_url: "https://relayfile.agent-relay.com"
    nango_base_url: "https://api.nango.dev"
    nango_secret_key: "${NANGO_SECRET_KEY}"
    connections:
      - connection_id: "conn_zendesk_acme"
        provider: "zendesk"
        enabled_objects: ["tickets", "users"]
        writeback_enabled: true
      - connection_id: "conn_shopify_acme"
        provider: "shopify"
        enabled_objects: ["orders", "products", "customers"]
        writeback_enabled: false
      - connection_id: "conn_github_acme"
        provider: "github"
        enabled_objects: ["issues", "pulls"]
        writeback_enabled: true
    poll_interval_seconds: 10
    max_writeback_retries: 3
```

## Error Handling

| Scenario | Behavior |
|----------|----------|
| Nango webhook delivery fails | Nango retries with exponential backoff |
| Relayfile ingest returns 429 (queue full) | Bridge retries with `Retry-After` header |
| Relayfile ingest returns 409 (duplicate) | Bridge logs and skips — idempotent |
| Writeback to provider fails (4xx) | Ack with `success: false`; Relayfile dead-letters after retries |
| Writeback to provider fails (5xx) | Bridge retries before acking failure |
| Nango token expired | Nango auto-refreshes OAuth tokens — transparent to bridge |
| Unknown provider/object type | Bridge drops event, logs warning, increments metric |

## Security

- The bridge authenticates to Relayfile using a workspace-scoped JWT with `sync:read` and `sync:trigger` scopes
- Nango webhook deliveries are verified using Nango's webhook signature (HMAC-SHA256)
- The bridge never stores provider credentials — all API calls go through Nango's proxy
- Writeback payloads are validated against known schemas before forwarding to prevent injection

## Implementation Status

### Completed

| Component | Location | Notes |
|-----------|----------|-------|
| Generic webhook ingest endpoint | `internal/httpapi/server.go` route `generic_webhook_ingest` | JWT auth with `sync:trigger` scope |
| Writeback pending list endpoint | `internal/httpapi/server.go` route `writeback_pending` | JWT auth with `sync:read` scope |
| Writeback ack endpoint | `internal/httpapi/server.go` route `writeback_ack` | JWT auth with `sync:trigger` scope |
| TS SDK `ingestWebhook()` | `sdk/relayfile-sdk/src/client.ts` | POST to `/webhooks/ingest` |
| TS SDK `listPendingWritebacks()` | `sdk/relayfile-sdk/src/client.ts` | GET from `/writeback/pending` |
| TS SDK `ackWriteback()` | `sdk/relayfile-sdk/src/client.ts` | POST to `/writeback/{itemId}/ack` |
| TS `NangoHelpers.ingestNangoWebhook()` | `sdk/relayfile-sdk/src/nango.ts` | Canonical path computation, semantic property mapping |
| TS `NangoHelpers.getProviderFiles()` | `sdk/relayfile-sdk/src/nango.ts` | Auto-paginating query with provider/status filters |
| TS `NangoHelpers.watchProviderEvents()` | `sdk/relayfile-sdk/src/nango.ts` | Async generator with polling + AbortSignal |
| TS SDK types | `sdk/relayfile-sdk/src/types.ts` | `IngestWebhookInput`, `WritebackItem`, `AckWritebackInput`, `AckWritebackResponse` |
| Python SDK types | `sdk/relayfile-sdk-py/src/relayfile/types.py` | Mirrors TS types with dataclasses |
| Python SDK errors | `sdk/relayfile-sdk-py/src/relayfile/errors.py` | `RelayFileApiError` hierarchy |
| Test coverage | `sdk/relayfile-sdk/src/client.test.ts` | Tests for ingest, writeback, and NangoHelpers |

### Remaining Work

1. **Python SDK client** (`sdk/relayfile-sdk-py/src/relayfile/client.py`) — `RelayFileClient` + `AsyncRelayFileClient` using `httpx`; mirror all TS SDK methods
2. **Python SDK Nango helpers** (`sdk/relayfile-sdk-py/src/relayfile/nango.py`) — `NangoHelpers` + `AsyncNangoHelpers`; mirror TS `nango.ts`
3. **Standalone Nango Bridge service** — Deployable worker that receives Nango webhooks, translates them via `NangoHelpers`, and runs the writeback polling loop
4. **Provider-specific diff logic** — Writeback bridge currently sends full file content; need minimal diff/patch computation per provider
5. **Rate limiting for writeback polling** — Bridge should respect `Retry-After` headers and backoff on 429s from Relayfile
6. **Observability** — Structured logging and metrics (ingest count, writeback latency, error rate per provider)
