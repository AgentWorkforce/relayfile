# Integration Priority

Which integrations to build and in what order, based on where relayfile's value-add is highest.

---

## The Prioritization Framework

Relayfile's core value is not being a better notification bridge or a smarter cache. It is the **shared workspace where humans and agents work on the same things at the same time**. That means value is highest where:

1. **Multiple writers** — humans and agents both mutate the same records concurrently
2. **Living state** — records evolve continuously, not just at ingestion time
3. **Cross-record relationships** — the graph of connections between records matters to agents
4. **Consequential writeback** — an agent writing back has real business impact, so conflict detection and ACLs are not optional
5. **Webhook support** — the system can push changes rather than requiring polling

Where all five are true, relayfile is genuinely irreplaceable. Where none are true, MCP or Mirage compete just as well.

This framework produces four tiers:

| Tier | Characteristics | Relayfile value |
|---|---|---|
| **1 — Collaboration** | Multi-writer, living state, rich relations | Irreplaceable |
| **2 — Action** | Human+agent writes, consequential, ACL-sensitive | Very high |
| **3 — Awareness** | Real-time read, cross-record, some writes | High |
| **4 — Data** | Mostly read, append-only, low concurrency | Moderate |

---

## Tier 1: Collaboration (Highest Value)

These are the systems where humans and agents are actively reading and writing the same records at the same time. Conflict detection, forks, ACLs, and real-time fan-out are all load-bearing here. MCP and Mirage offer nothing comparable.

### Already Built

| Adapter | Why it's Tier 1 | Status |
|---|---|---|
| **Linear** | Issues live-edited by humans while agents triage, comment, and update status simultaneously. Relations between issues, projects, and cycles are rich. | ✓ Built |
| **Jira** | Enterprise issue tracking with complex workflows, assignees, comments, and linked issues. Agent writes to status and comments are consequential. | ✓ Built |
| **GitHub** | PRs, issues, and code review are the canonical multi-writer collaboration surface. Agents commenting, reviewing, and updating issues alongside humans. | ✓ Built |
| **GitLab** | Same as GitHub for GitLab-native teams. | ✓ Built |
| **Notion** | Collaborative knowledge base with nested pages, databases, and relations. Humans edit pages while agents synthesize, summarize, and link records. | ✓ Built |
| **Asana** | Task and project management with assignees, dependencies, and timelines. Agent and human updates to task status and comments. | ✓ Built |
| **ClickUp** | Broad task management with docs, whiteboards, and automations. Heavy multi-writer usage. | ✓ Built |

### Missing — Build Next

| Integration | Why it belongs here | Priority |
|---|---|---|
| **Confluence** | The dominant enterprise knowledge base. Pages are collaboratively authored by humans; agents need to read, synthesize, and update them alongside human editors. Relations between Confluence pages and Jira issues are critical — already have Jira, Confluence completes the Atlassian pair. Webhooks available. | **#1** |
| **Google Docs / Sheets** | Where the majority of collaborative knowledge work happens outside dedicated tools. Docs are living documents edited by multiple humans in real-time; agents that can see edits as they happen and write back are qualitatively more useful than any poll-based integration. Drive Watch API provides real-time push. | **#2** |
| **SharePoint / OneDrive** | Enterprise equivalent of Google Docs for Microsoft 365 organizations. Document libraries are the primary collaboration surface for large enterprises. Graph subscriptions give real-time push. Completing the Microsoft stack (already have Teams). | **#3** |
| **Monday.com** | Fast-growing project management platform with 225k+ organizations. Item boards have rich field types, automations, and cross-board relations. Webhooks available. | **#4** |
| **Coda** | Collaborative doc-database hybrid popular with product and ops teams. Tables with cross-doc relations and automations. Webhooks available. | **#5** |
| **Figma** | Design collaboration platform where comments, component links, and file versions are the coordination surface. Agents reviewing designs, creating comment threads, and extracting specs alongside designers. Webhook support for file changes and comments. | **#6** |
| **Miro** | Collaborative whiteboard used heavily in planning and retros. Board items, sticky notes, and connections have relational structure. REST API with webhooks. | **#7** |

---

## Tier 2: Action (Very High Value)

These systems have consequential agent writes — updating a CRM record, resolving a support ticket, escalating an incident. Conflict detection and ACLs matter because getting it wrong has real business impact. Cross-record relations (contact → deal → company) are rich.

### Already Built

| Adapter | Why it's Tier 2 | Status |
|---|---|---|
| **Salesforce** | CRM records (leads, contacts, opportunities, accounts) are the most consequential writes an agent can make. Relations between objects are deep. | ✓ Built |
| **HubSpot** | Marketing and sales CRM with contacts, deals, companies, and activities. Agent writes to deal stage and contact properties are high-impact. | ✓ Built |
| **Pipedrive** | Sales-focused CRM with deal pipeline. Common for SMB sales teams. | ✓ Built |
| **Zendesk** | Customer support tickets with comments, assignees, and status transitions. Agent responses and status updates are public-facing and consequential. | ✓ Built |
| **Intercom** | Customer messaging platform. Agent-drafted responses to customer conversations require ACL enforcement and conflict detection (two agents shouldn't both reply). | ✓ Built |

### Missing — Build Next

| Integration | Why it belongs here | Priority |
|---|---|---|
| **Freshdesk** | Second-largest helpdesk platform after Zendesk. Many organizations run Freshdesk specifically; a Zendesk adapter doesn't help them. Webhooks available. | **#1** |
| **ServiceNow** | Dominant enterprise ITSM platform. Incident, change, and problem records are high-stakes; agent writes must be conflict-safe. REST API with table-level webhooks. | **#2** |
| **PagerDuty** | Incident management. Agents acknowledging, escalating, and resolving incidents alongside on-call humans is a canonical multi-writer scenario. Webhooks available. | **#3** |
| **Freshsales** | CRM from Freshworks ecosystem. Common companion to Freshdesk in SMB. | **#4** |
| **Trello** | Simpler card-based project management with large user base. Cards, lists, and attachments. Webhooks available. | **#5** |

---

## Tier 3: Awareness (High Value)

These systems are primarily real-time communication channels. Agents read and react to messages and events; writeback happens (sending messages, posting updates) but concurrency conflicts are softer than Tier 1/2.

### Already Built

| Adapter | Why it's Tier 3 | Status |
|---|---|---|
| **Slack** | The primary human communication channel. Agents reading channels and threads in real-time and posting responses. Cross-record relations to Linear, GitHub, Jira (via link unfurls and integrations) are valuable. | ✓ Built |
| **Microsoft Teams** | Enterprise equivalent of Slack. Heavy in Microsoft 365 organizations (same stack as SharePoint/OneDrive). | ✓ Built |

### Missing — Build Next

| Integration | Why it belongs here | Priority |
|---|---|---|
| **Discord** | Primary communication platform for developer communities, open-source projects, and web3 teams. Bot webhooks give real-time message delivery. Growing for internal team use. | **#1** |
| **Gmail** | Email remains the primary communication channel for B2B workflows. Agents reading threads and drafting replies with real-time push via Gmail Watch API + Pub/Sub. | **#2** |
| **Outlook / Exchange** | Enterprise email for Microsoft 365 organizations. Graph API subscriptions for real-time push. Completes the Microsoft stack. | **#3** |
| **Telegram** | Real-time bot webhooks (< 1s). Valuable for teams that use Telegram as a workflow channel, particularly in crypto, fintech, and international organizations. | **#4** |

---

## Tier 4: Data (Moderate Value)

These systems are primarily read-heavy. Agents ingest data, run analysis, and occasionally write back. The concurrency problem is soft; MCP competes reasonably well here. Relayfile adds value through the semantic layer (cross-provider queries, relations to Tier 1/2 records) and scheduled freshness, but is not irreplaceable.

### Already Built

| Adapter | Why it's Tier 4 | Status |
|---|---|---|
| **Airtable** | Database/spreadsheet hybrid. Read-heavy for most agent workflows; occasional writes. Nango already has this. | ✓ Built |
| **Shopify** | E-commerce platform. Order and product data read-heavy; writeback limited by Shopify's API (orders largely read-only). | ✓ Built |
| **Stripe** | Payment data. Primarily read for analytics and reconciliation; some consequential writes (refunds, subscription updates). | ✓ Built |
| **Mixpanel** | Analytics event data. Read-only for agents. Value is in cross-referencing with CRM (Salesforce contacts) via relations. | ✓ Built |
| **Segment** | Customer data platform. Write-heavy from products into Segment; agents mostly read user traits and events. | ✓ Built |
| **Mailgun** | Email delivery service. Transactional; agents read delivery status and bounces. Low concurrency. | ✓ Built |
| **SendGrid** | Same as Mailgun. | ✓ Built |
| **Calendly** | Scheduling. Agents reading availability and booking data. Low write-back, low concurrency. | ✓ Built |

### Missing — Lower Priority

| Integration | Notes | Priority |
|---|---|---|
| **Snowflake** | Cloud data warehouse. Read-heavy; agents query data but rarely write back. Value mainly through cross-provider relations (user IDs linking to Salesforce contacts). | Low |
| **BigQuery** | Same as Snowflake for GCP teams. | Low |
| **Twilio** | SMS/voice. Transactional; no real concurrency problem. | Low |
| **Typeform** | Form responses. Append-only, read by agents. | Low |
| **Webflow** | CMS. Content read-heavy; some writeback (publishing pages). | Low |

---

## Summary: What to Build Next

Mapping against the existing 24 adapters, the highest-value gaps in order:

| # | Integration | Tier | Gap it fills |
|---|---|---|---|
| 1 | **Confluence** | 1 — Collaboration | Completes Atlassian stack; Jira alone is half the picture |
| 2 | **Google Docs / Drive** | 1 — Collaboration | Largest collaborative document surface in the world |
| 3 | **SharePoint / OneDrive** | 1 — Collaboration | Enterprise document collaboration for Microsoft orgs |
| 4 | **Freshdesk** | 2 — Action | Covers the half of support teams not on Zendesk |
| 5 | **Gmail** | 3 — Awareness | Primary B2B communication channel; real-time push available |
| 6 | **ServiceNow** | 2 — Action | Enterprise ITSM; high-stakes writes need conflict safety |
| 7 | **PagerDuty** | 2 — Action | Incident management; canonical multi-writer scenario |
| 8 | **Monday.com** | 1 — Collaboration | 225k+ organizations; rich project structure |
| 9 | **Discord** | 3 — Awareness | Developer communities and modern team communication |
| 10 | **Figma** | 1 — Collaboration | Design collaboration; comments and specs alongside designers |
| 11 | **Outlook** | 3 — Awareness | Completes Microsoft 365 stack |
| 12 | **Coda** | 1 — Collaboration | Collaborative doc-database popular in product/ops teams |
| 13 | **Trello** | 2 — Action | Large user base; simple but widely deployed |
| 14 | **Miro** | 1 — Collaboration | Planning and retro workflows |

---

## What Not to Build Yet

- **Pure storage backends** (S3, GCS, R2, SFTP) — covered in `storage-bridge-spec.md`; valuable infrastructure but not where the coordination value is
- **Analytics-only tools** (Looker, Metabase, Amplitude) — read-only for agents; MCP handles this fine
- **Pure notification/transactional tools** (Twilio, Plaid, Braintree) — low concurrency, low relation depth; MCP or direct SDK is sufficient
- **Self-hosted databases** (Postgres, MySQL, MongoDB) — covered in `storage-bridge-spec.md`; useful but not the coordination differentiation story

These are worth doing eventually for completeness, but building them before Confluence, Google Docs, or Freshdesk would be optimizing for breadth over depth at the wrong moment.
