# Storage Bridge Priority: Real-Time Notification Systems

Which storage systems to integrate first, and why real-time push notifications are our biggest value differentiator.

---

## The Core Argument

MCP gives agents tool calls. Mirage gives agents a cached read layer. Neither gives agents **continuous awareness** — the ability to know a file changed without asking.

That awareness is relayfile's strongest differentiator, and it is only possible where the storage system can push change notifications. Systems that support push notifications are therefore our highest-value integrations. A file in Google Drive that updates the moment a collaborator edits it is qualitatively different from a file that might be stale by up to 10 minutes.

This document ranks storage systems by their notification capability and implementation priority.

---

## Priority Tier 1: Native HTTP Push (Highest Value)

These systems natively deliver HTTP webhook callbacks when files change. Integration produces true real-time awareness — changes reach the relayfile workspace within seconds.

### Google Drive

**Notification mechanism:** Drive Push Notifications (Watch API)  
**Latency:** 2–10 seconds  
**Coverage:** Individual files and folder trees  
**Renewal:** Watch channels expire after max 1 week; adapter auto-renews

Google Drive's Watch API allows subscribing to change notifications on any file, folder, or the entire drive. When a file is created, edited, moved, or deleted, Drive sends an HTTP POST to a registered callback URL. The notification includes the resource ID and a change token; the adapter then calls the Drive Changes API to fetch the delta.

```text
User edits file in Google Drive
  │  (2–10s)
  ▼
Drive POST /relayfile-adapter/drive/callback
  │  { resourceId, changeToken }
  ▼
Adapter fetches changes: drive.changes.list(changeToken)
  │
  ▼
StorageBridgeEvent published
  │
  ▼
Relayfile workspace updated
  │  (< 15s total)
  ▼
Agent sees change via WebSocket
```

**Why this is our #1 priority:**  
Google Drive is where the majority of collaborative knowledge work happens. Agents that can see document changes in near-real-time can respond to human edits, draft follow-ups, or trigger downstream workflows without polling. No other integration delivers this cleanly for Drive today.

**Scopes required:** `drive.readonly` for ingest, `drive` for writeback  
**Nango integration:** Yes — Nango handles OAuth and can run scheduled syncs as fallback  
**Writeback support:** Yes — Drive API supports file create, update, and delete

---

### SharePoint / OneDrive

**Notification mechanism:** Microsoft Graph Subscriptions (webhooks)  
**Latency:** 5–30 seconds  
**Coverage:** Lists (document libraries), drives, and individual items  
**Renewal:** Subscriptions expire after max 29 days; adapter auto-renews

Microsoft Graph subscriptions notify on changes to SharePoint document libraries and OneDrive files. The notification payload is minimal (resource URL only); the adapter calls the Graph Delta API to fetch what changed since the last delta token.

SharePoint and OneDrive share the same Graph API surface — the same adapter handles both. SharePoint sites expose document libraries as drives; OneDrive is a personal drive. Both use the same subscription model.

```text
User uploads file to SharePoint document library
  │
  ▼
Graph sends notification to adapter webhook endpoint
  │  { subscriptionId, clientState, changeType, resource }
  ▼
Adapter: GET /v1.0/drives/{driveId}/root/delta?token={deltaToken}
  │
  ▼
StorageBridgeEvent(s) published for each changed item
```

**Why this is #2:**  
SharePoint is the dominant enterprise document store. Organizations running Microsoft 365 have their critical files here. Real-time awareness of SharePoint changes enables agents to act on document approval workflows, policy updates, and shared project files without polling.

**Scopes required:** `Files.Read.All` for ingest, `Files.ReadWrite.All` for writeback  
**Nango integration:** Yes — Nango has Microsoft Graph OAuth  
**Writeback support:** Yes — Drive API supports full file CRUD  
**Note:** Subscription validation requires a synchronous 200 response with the `validationToken` on initial handshake.

---

### Dropbox

**Notification mechanism:** Dropbox Webhooks  
**Latency:** 5–30 seconds  
**Coverage:** Account-level (all changes in a connected account)  
**Renewal:** Webhooks do not expire; registered once

Dropbox webhooks notify when any change occurs in a connected account. The notification is intentionally minimal — it signals that something changed, without specifying what. The adapter then calls `/files/list_folder/continue` with a stored cursor to retrieve the actual delta. This two-step design is Dropbox's intentional architecture.

```text
File added/modified/deleted in Dropbox
  │
  ▼
Dropbox POST /adapter/dropbox/callback
  │  { list_folder: { accounts: ["dbid:abc"] } }
  ▼
Adapter: POST /2/files/list_folder/continue { cursor }
  │  returns list of changed entries
  ▼
StorageBridgeEvent published for each changed entry
```

**Why this is #3:**  
Dropbox remains widely used for file sharing across organizations, particularly with external collaborators. The two-step notification+poll model is straightforward to implement and the cursor-based delta is reliable.

**Scopes required:** `files.metadata.read` + `files.content.read`  
**Nango integration:** Yes  
**Writeback support:** Yes — Dropbox API supports upload and delete  
**Note:** Webhook endpoint must respond to GET challenge for verification before receiving events.

---

### Gmail

**Notification mechanism:** Gmail Push Notifications via Google Cloud Pub/Sub  
**Latency:** 5–30 seconds  
**Coverage:** Full mailbox, label-filtered, or specific threads  
**Renewal:** Watch subscriptions expire after 7 days; adapter auto-renews

Gmail's Watch API routes change notifications through a Google Cloud Pub/Sub topic. When new mail arrives or labels change, Gmail publishes a notification to the Pub/Sub topic. The adapter subscribes to the topic and calls the Gmail History API to fetch the actual changes since the last `historyId`.

Emails are surfaced in the relayfile workspace as files: each thread becomes a `.json` file at `/gmail/{account}/threads/{threadId}.json` containing the full thread with messages, labels, and attachments.

```text
Email received or label changed
  │
  ▼
Gmail publishes to Cloud Pub/Sub topic
  │
  ▼
Adapter receives Pub/Sub message
  │  { emailAddress, historyId }
  ▼
Adapter: gmail.users.history.list(startHistoryId)
  │  returns added/deleted messages and label changes
  ▼
StorageBridgeEvent published per changed thread
```

**Why Gmail:**  
Email is a primary communication channel for business decisions, contracts, and approvals. Agents that can read email in real-time can draft responses, extract action items, and trigger workflows without human copy-paste. Gmail's Pub/Sub mechanism makes this genuinely real-time.

**Scopes required:** `gmail.readonly` for ingest (labels + metadata + content)  
**Nango integration:** Yes — Google OAuth via Nango  
**Writeback support:** Yes — send drafts, apply labels, archive  
**Note:** Requires a Google Cloud Pub/Sub topic in the agent's GCP project with Gmail service account granted publisher access.

---

### Google Cloud Storage (GCS)

**Notification mechanism:** Native GCS Pub/Sub Notifications  
**Latency:** 2–10 seconds  
**Coverage:** Object-level (create, update, delete, metadata change)  
**Renewal:** Notifications do not expire; configured once per bucket

Already specified in `storage-bridge-spec.md`. Included here for completeness in the priority ranking.

**Why high priority:** GCS is the default storage for GCP workloads. Data pipelines, ML training sets, and analytics exports live here. Real-time awareness means agents can trigger downstream processing the moment new data lands.

---

### Azure Blob Storage

**Notification mechanism:** Azure Event Grid  
**Latency:** 5–30 seconds  
**Coverage:** Container-level or storage account-level, blob created and deleted  
**Renewal:** Event subscriptions do not expire

Already specified in `storage-bridge-spec.md`. Included here for completeness.

**Why high priority:** Azure Blob is the dominant cloud storage in enterprise Microsoft environments — the same organizations using SharePoint and OneDrive. Pairing Azure Blob with SharePoint/OneDrive adapters gives full Microsoft 365 coverage.

---

## Priority Tier 2: Webhook Support, More Complex Setup

These systems support webhooks but require more setup, have limitations, or are less universally used.

### Box

**Notification mechanism:** Box Webhooks (v2)  
**Latency:** 5–60 seconds  
**Coverage:** File and folder level; specific events (UPLOAD, COPY, DELETE, etc.)  
**Renewal:** Webhooks do not expire

Box supports granular webhook subscriptions per file or folder with specific event type filtering. Notification payloads include the triggering event type, the affected resource, and a timestamp. No second fetch needed for basic metadata — content still requires a separate download.

**Priority:** Medium. Box is common in regulated industries (legal, healthcare, finance) but less universal than Drive or SharePoint.

### Notion

**Notification mechanism:** Notion Webhooks (beta as of 2026)  
**Coverage:** Database and page-level changes

Already handled by the Nango SaaS bridge (`nango-bridge-design.md`). Notion's webhook support is maturing; the Nango bridge covers it via scheduled sync today with real-time via webhooks as the endpoint stabilizes.

### Slack (Files)

**Notification mechanism:** Slack Events API  
**Coverage:** File shared, file deleted events

Already covered by the Nango SaaS bridge. Slack files are not a primary use case for storage bridge — they're better handled as part of the broader Slack message/channel integration.

---

## Priority Tier 3: Scheduled Sync Only (Lower Value-Add)

These systems either have no native push mechanism or are listed here only for their fallback mode. Integration provides eventual consistency via Nango scheduled sync but not real-time awareness. Less differentiated from MCP or Mirage.

| System | Best available | Typical lag |
|---|---|---|
| AWS S3 | Tier 1 with SQS Event Notifications; Nango poll is fallback only | <10s with SQS; 1–5 min fallback |
| SFTP | Poll | 5–60 min |
| FTP | Poll | 5–60 min |
| Local filesystem | inotify bridge | < 1s (but agent is local) |

S3 with SQS Event Notifications is Tier 1; polling via Nango or other pollers is a Tier 3 fallback.

---

## Implementation Roadmap

### Sprint 1 — Google Drive + GCS (4 weeks)

Drive and GCS share the Google OAuth model and the Cloud Pub/Sub infrastructure, making them natural to ship together.

**MVP deliverables:**
- Google Drive Watch API subscription manager (create, renew, delete)
- Drive Changes API delta fetcher
- GCS Pub/Sub notification bridge (from `storage-bridge-spec.md` Phase 2)

**Deferred follow-up:**
- Full Storage adapter worker with Google Cloud Pub/Sub backend
- File path conventions for `/drive/{accountId}/{path}` and `/gcs/{bucket}/{key}`
- Drive writeback (create, update, delete)
- Nango scheduled sync fallback for both

**Why first:** Largest addressable market, shared infrastructure, Google OAuth already in Nango.

---

### Sprint 2 — SharePoint + OneDrive + Azure Blob (4 weeks)

Microsoft Graph subscriptions cover SharePoint and OneDrive with the same adapter. Azure Blob Event Grid is the natural companion for Microsoft-stack deployments.

**Deliverables:**
- Microsoft Graph subscription manager (create, renew, delete)
- Drive Delta API poller for SharePoint document libraries and OneDrive
- Azure Event Grid bridge (from `storage-bridge-spec.md`)
- File path conventions for `/sharepoint/{siteId}/{driveId}/{path}` and `/onedrive/{accountId}/{path}`
- Writeback for SharePoint and OneDrive
- Nango fallback for all three

**Why second:** Full Microsoft 365 coverage in one sprint. Enterprises with SharePoint almost certainly also use OneDrive and Azure.

---

### Sprint 3 — Dropbox + Gmail (3 weeks)

**Deliverables:**
- Dropbox webhook receiver + cursor-based delta fetcher
- Gmail Watch API subscription manager + History API delta fetcher
- Cloud Pub/Sub subscription for Gmail notifications
- File path conventions for `/dropbox/{accountId}/{path}` and `/gmail/{account}/threads/{id}.json`
- Writeback for Dropbox (upload, delete) and Gmail (draft, label, archive)

---

### Sprint 4 — S3 real-time + Box (3 weeks)

**Deliverables:**
- S3 SQS bridge (from `storage-bridge-spec.md` Phase 1)
- Box webhook receiver + content fetcher
- Nango fallback for S3 (already usable before this sprint)

---

## Additional Systems: Supabase, Telegram, R2, MongoDB, Convex, Hugging Face

### Supabase Storage

**Notification mechanism:** HTTP webhook (pg-boss queue, Postgres-backed)  
**Latency:** ~4s timeout per delivery; 30s max queue lifetime  
**Coverage:** `object-created`, `object-removed`, `object-updated`, `bucket-created`, `bucket-deleted`, plus fine-grained S3-style events (Put, Copy, Move, Delete)  
**Status:** Implemented in Supabase Storage codebase; self-hosted via env vars. Cloud availability unclear — not prominently documented as of 2026.

Supabase Storage has a complete webhook delivery system internally, including per-event filtering, Bearer token auth on the callback, and Postgres-backed retry via pg-boss. For self-hosted Supabase deployments this is Tier 1. For cloud-hosted Supabase the feature appears to be in soft beta.

Supabase Realtime covers **database changes only** — not storage file events.

**Priority:** Tier 1 for self-hosted Supabase deployments (developer-heavy audience); Tier 2 for cloud until the feature is generally available.  
**Nango support:** No Supabase integration in Nango's current catalog. Scheduled fallback requires custom Nango integration or direct S3-compatible API polling.  
**Writeback support:** Yes — Supabase Storage is S3-compatible.

---

### MongoDB Atlas

**Notification mechanism:** Atlas Database Triggers → Atlas Function → HTTP POST  
**Latency:** Near-real-time (change stream based); up to 10,000 concurrent trigger executions  
**Coverage:** Insert, Update, Delete, Replace on any collection; collection-level operations (Create, Drop, Rename)  
**Alternative:** Atlas Triggers can forward directly to AWS EventBridge without writing Function code

MongoDB Atlas Triggers watch a change stream and invoke an Atlas Function on each matched event. The Function runs serverless JavaScript/TypeScript and can `fetch()` any external HTTP endpoint, making it a genuine push mechanism to relayfile's ingest endpoint. At-least-once delivery.

This is the cleanest real-time bridge for MongoDB without running self-hosted infrastructure. Self-hosted MongoDB requires a change stream listener process (similar to the Postgres LISTEN/NOTIFY bridge in `storage-bridge-spec.md`).

Documents are serialized as JSON files: `/mongodb/{cluster}/{database}/{collection}/{_id}.json`.

**Priority:** Tier 1 for Atlas users; Tier 2 for self-hosted MongoDB.  
**Nango support:** No MongoDB integration in Nango's current catalog. Scheduled fallback requires custom integration.  
**Writeback support:** Yes — Atlas Data API or driver-based upsert.

---

### Cloudflare R2

**Notification mechanism:** R2 Event Notifications → Cloudflare Queues → Worker (push) or HTTP pull  
**Latency:** No published SLA; queue throughput 5,000 msg/sec per queue  
**Coverage:** `object-create` (PutObject, CopyObject, CompleteMultipartUpload), `object-delete` (DeleteObject, LifecycleDeletion)  
**Filtering:** Up to 100 rules per bucket with prefix/suffix filters

R2 does not push HTTP webhooks to arbitrary external URLs directly. Events flow into a Cloudflare Queue. To get events out:

- **Path A (recommended):** A Cloudflare Worker acts as the queue consumer and calls the relayfile ingest endpoint via `fetch()`. Latency is low; runs inside Cloudflare's network.
- **Path B:** The relayfile adapter polls the Queues HTTP pull consumer API. Simple but adds 1–5s of polling latency.

Path A makes R2 effectively Tier 1 for teams already on Cloudflare. The Worker is ~20 lines and can be deployed alongside an existing R2 setup.

**Priority:** Tier 1 with a Cloudflare Worker bridge; Tier 2 with HTTP pull polling.  
**Nango support:** No R2 integration in Nango's current catalog.  
**Writeback support:** Yes — R2 is S3-compatible.

---

### Telegram

**Notification mechanism:** Bot API `setWebhook` — HTTP POST per update  
**Latency:** < 1 second  
**Coverage:** Every message, file, photo, document, or sticker sent to the bot

Telegram's Bot API delivers every update (message, file, inline query, callback) to a registered HTTPS webhook endpoint in real-time. Files sent to a Telegram bot appear as `document`, `photo`, `video`, or `audio` objects with a `file_id`; the adapter calls `getFile` to obtain a download URL and fetches the content.

The use case differs from traditional storage: Telegram is a **delivery channel**, not a storage backend. But for agent workflows where humans share files via Telegram (contracts, reports, images), making those files instantly available in the relayfile workspace unlocks a class of agent that can respond to document uploads in near-real-time.

Files are surfaced at `/telegram/{botUsername}/chats/{chatId}/{messageId}/{fileName}`.

**Priority:** Tier 1 technically; niche use case. Prioritize after the primary storage systems. High value for teams already using Telegram as a workflow tool.  
**Nango support:** No Telegram integration in Nango's current catalog.  
**Writeback support:** Yes — bot can send messages and files back to the chat.

---

### Redis

Already specified in `storage-bridge-spec.md` as a Tier 2 system. Keyspace notifications are internal Redis pub/sub — not HTTP push to external endpoints. A bridge process subscribes and forwards to the pub/sub topic. No Nango integration available; scheduled fallback would require custom polling.

---

### Convex

**Notification mechanism:** No native outbound webhook. User-built via `ctx.scheduler` + Action + `fetch()`  
**Latency:** Depends on scheduler queue depth; no SLA  
**File storage:** No event or notification API

Convex's real-time model is client-subscription-based (WebSocket). There is no platform-level outbound webhook when data or files change. The closest approximation is: add a database Trigger (community library `convex-helpers`) that schedules a Convex Action, which then calls `fetch()` to POST to the relayfile ingest endpoint. This requires Convex users to write this integration themselves.

Convex file storage has no events at all — files must be polled via `storage.getUrl()`.

**Priority:** Tier 3. Convex's audience is growing but the integration requires user-authored code on the Convex side, making it unsuitable as a first-class bridge. Revisit when Convex ships native outbound webhooks.  
**Nango support:** No Convex integration in Nango's current catalog.

---

### Hugging Face Storage Buckets

**Notification mechanism:** None  
**Latency:** Manual sync only  
**Coverage:** N/A

Hugging Face Buckets are S3-like mutable object storage for ML artifacts (checkpoints, optimizer states, data shards). As of 2026 there are no native event notifications, webhooks, or push mechanisms. Changes are detected only via the `hf` CLI or Python SDK `sync_bucket()` on demand.

**Priority:** Tier 3. Can be polled on a schedule using the HF Hub API (`list_repo_tree` with `recursive=True`). Value for ML-heavy agent workflows that process training artifacts, but no real-time differentiation.  
**Nango support:** No Hugging Face integration in Nango's current catalog. Would require a custom integration.  
**Writeback support:** Yes — HF Hub API and `hf_hub_upload`.

---

## Nango Integration Catalog

Nango's `flows.yaml` (121 integrations as of 2026) includes the following storage and file-related systems that get scheduled sync fallback for free when using the Nango bridge path from `nango-bridge-design.md`:

| Integration | Type | Nango slug | Notes |
|---|---|---|---|
| Google Drive | Cloud storage | `google-drive` | Also has real-time Watch API (Sprint 1) |
| OneDrive | Cloud storage | `one-drive` | Also has Graph subscriptions (Sprint 2) |
| SharePoint Online | Enterprise docs | `sharepoint-online` | Also has Graph subscriptions (Sprint 2) |
| Dropbox | Cloud storage | `dropbox` | Also has webhooks (Sprint 3) |
| Box | Enterprise storage | `box` | Also has webhooks (Sprint 4) |
| Notion | Docs/database | `notion` | Covered by nango-bridge-design.md |
| Confluence | Documentation | `confluence` | Page-level sync via REST API |
| Google Docs | Documents | `google-docs` | Individual doc sync |
| Google Sheets | Spreadsheets | `google-sheet` | Sheet sync as structured JSON |
| Smartsheet | Spreadsheets | `smartsheet` | Sheet and row sync |
| Nextcloud | Self-hosted files | `next-cloud-ocs` | WebDAV-compatible; OCS API |
| Airtable | Database/grid | `airtable` | Record-level sync |
| Databricks Workspace | ML platform | `databricks-workspace` | Notebook and artifact sync |

**What this means in practice:** Any system in this table gets a working scheduled sync integration immediately via the existing Nango bridge path. The storage bridge spec adds real-time push on top for systems that support it. The combination — Nango scheduled fallback + real-time bridge when available — gives us defense in depth: agents always have access to reasonably fresh data even during bridge outages.

Systems **not** in Nango's catalog (S3, R2, Postgres, Redis, MongoDB, Supabase Storage, Convex, Hugging Face) require either a custom Nango integration or a direct bridge per `storage-bridge-spec.md`.

---

## Updated Full Priority Table

| System | Push notifications | Nango scheduled fallback | Priority | Sprint |
|---|---|---|---|---|
| Google Drive | ✓ Watch API (< 10s) | ✓ | **Highest** | 1 |
| GCS | ✓ Pub/Sub (< 10s) | — | **Highest** | 1 |
| SharePoint / OneDrive | ✓ Graph subscriptions (< 30s) | ✓ | **Highest** | 2 |
| Azure Blob | ✓ Event Grid (< 30s) | — | **Highest** | 2 |
| Dropbox | ✓ Webhooks (< 30s) | ✓ | High | 3 |
| Gmail | ✓ Pub/Sub (< 30s) | — | High | 3 |
| MongoDB Atlas | ✓ Atlas Triggers (near real-time) | — | High | 5 |
| Cloudflare R2 | ✓ via CF Worker (< 5s) | — | High | 5 |
| Supabase Storage | ✓ self-hosted; ○ cloud beta | — | Medium | 5 |
| Box | ✓ Webhooks (< 60s) | ✓ | Medium | 4 |
| Telegram | ✓ Bot webhooks (< 1s) | — | Medium (niche) | 6 |
| Confluence | — | ✓ | Medium | Nango only |
| Google Docs / Sheets | — | ✓ | Medium | Nango only |
| Smartsheet | — | ✓ | Medium | Nango only |
| Nextcloud | — | ✓ | Medium | Nango only |
| Airtable | — | ✓ | Medium | Nango only |
| AWS S3 | ✓ SQS events (requires setup) | — | Medium | 4 |
| Postgres | ✓ LISTEN/NOTIFY or Debezium | — | Medium | spec Phase 2 |
| Redis | ○ Keyspace notifs (internal) | — | Lower | spec Phase 3 |
| Databricks Workspace | — | ✓ | Lower | Nango only |
| Hugging Face Buckets | — | — | Lower | Custom poll |
| Convex | — (user-built only) | — | Lower | Defer |
| SFTP / FTP | — | — | Lowest | Poll only |

✓ = native push to external HTTP endpoint  ○ = push but with caveats  — = not available

---

## Why This Ordering Wins

| Metric | After Sprint 1 | After Sprint 2 | After Sprint 3 | After Sprint 5 |
|---|---|---|---|---|
| Total addressable users | Approx. ~3B (Google Workspace) | Approx. +1.5B (Microsoft 365) | Illustrative +700M (Dropbox + Gmail) | +MongoDB + R2 developers |
| Real-time integrations | 2 (Drive, GCS) | 5 (+SharePoint, OneDrive, Azure) | 7 (+Dropbox, Gmail) | 10 (+MongoDB, R2, Supabase) |
| Scheduled fallback integrations | 2 | 5 | 7 | 13 (Nango catalog) |
| Enterprise coverage | Google-stack | Full cloud | + Dropbox-heavy orgs | + MongoDB/CF developers |
| Differentiation vs MCP | Clear | Strong | Very strong | Decisive |
| Differentiation vs Mirage | Clear (push not pull) | Strong | Very strong | Decisive |

The case for relayfile over MCP or Mirage becomes undeniable once we have Google Drive and SharePoint on push notifications — those are the two file systems where collaborative editing makes real-time awareness genuinely valuable rather than just technically interesting. Nango's existing catalog means every integration in that table gets scheduled sync for free, giving us breadth while we build depth on real-time bridges.
