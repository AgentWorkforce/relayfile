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

```
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

```
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

```
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

```
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

These systems have no native push mechanism. Integration provides eventual consistency via Nango scheduled sync but not real-time awareness. Less differentiated from MCP or Mirage.

| System | Best available | Typical lag |
|---|---|---|
| AWS S3 | SQS events (requires setup) or Nango poll | 1–5 min (Nango) |
| SFTP | Poll | 5–60 min |
| FTP | Poll | 5–60 min |
| Local filesystem | inotify bridge | < 1s (but agent is local) |

S3 moves to Tier 1 when SQS Event Notifications are configured — the poll fallback is Tier 3.

---

## Implementation Roadmap

### Sprint 1 — Google Drive + GCS (4 weeks)

Drive and GCS share the Google OAuth model and the Cloud Pub/Sub infrastructure, making them natural to ship together.

**Deliverables:**
- Google Drive Watch API subscription manager (create, renew, delete)
- Drive Changes API delta fetcher
- GCS Pub/Sub notification bridge (from `storage-bridge-spec.md` Phase 1)
- Storage adapter worker with Google Cloud Pub/Sub backend
- File path conventions for `/drive/{accountId}/{path}` and `/gcs/{bucket}/{key}`
- Writeback for Drive (create, update, delete)
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

## Why This Ordering Wins

| Metric | After Sprint 1 | After Sprint 2 | After Sprint 3 |
|---|---|---|---|
| Total addressable users | ~3B (Google Workspace) | +1.5B (Microsoft 365) | +700M (Dropbox + Gmail personal) |
| Real-time integrations | 2 (Drive, GCS) | 5 (+SharePoint, OneDrive, Azure) | 7 (+Dropbox, Gmail) |
| Enterprise coverage | Google-stack | Full cloud (Google + Microsoft) | + Dropbox-heavy orgs |
| Differentiation vs MCP | Clear (real-time Drive) | Strong (full M365 real-time) | Very strong |
| Differentiation vs Mirage | Clear (push not pull) | Strong | Very strong |

The case for relayfile over MCP or Mirage becomes undeniable once we have Google Drive and SharePoint on push notifications — those are the two file systems where collaborative editing makes real-time awareness genuinely valuable rather than just technically interesting.
