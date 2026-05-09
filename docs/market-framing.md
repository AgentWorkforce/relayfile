# Relayfile Market Framing

## The Framing Problem

"Multi-agent coordination layer" is the right description of what relayfile is, but it is the wrong pitch for today's market. Most teams are not running multiple peer agents writing to shared state. Pitching coordination for a pattern they don't yet have creates a timing mismatch — the product sounds like infrastructure for a future they haven't hit yet.

The market that exists today is larger and more immediate, and relayfile serves it directly.

---

## The Developer Pitch

**Make your agent proactive with a few lines of code.**

Today most agents are reactive — they respond when called. Relayfile makes them proactive: the agent knows when something changed in the outside world and acts without being polled or triggered manually. A Jira ticket moves to blocked, the agent is notified. A PR is merged, the agent updates its working state. A customer sends a message, the agent picks it up in real time.

That shift — from reactive to proactive — is one SDK call:

```typescript
const sync = new RelayFileSync({ workspace: 'acme/support' });
sync.on('change', (file) => agent.handle(file));
```

No webhook endpoints. No signature verification. No polling loops. The agent just listens.

---

## The Critical Distinction: Local Agent vs. Cloud Agent

This is the question investors ask, and it has a sharp answer.

**A local developer using an agent** (Cursor, Claude Code, Windsurf) is at a keyboard. Their files live on their machine. If something changes, they see it. The agent is a co-pilot — one human, one machine, one session. Local files work. Mirage works. Relayfile adds infrastructure complexity for a problem that barely exists at this scale.

**A cloud agent running in production** is a different category entirely. The agent is the product — it runs in the vendor's cloud infrastructure, not on a user's laptop. It has no filesystem. It has no persistent state between runs. It has no built-in way to receive webhooks. When a run finishes and the next one starts, the agent wakes up with amnesia unless someone built the state layer.

Lindy deploys an agent for a customer. That agent runs in Lindy's infrastructure. It needs to know what is on the customer's Linear board right now. It needs to receive a webhook when a ticket changes while it is mid-task. It needs to persist its working state so the next run continues where the last one stopped. It cannot conflict with the human who also has Linear open.

There is no local filesystem. There is no fast-enough polling. There is no built-in state persistence. Every company shipping cloud agents is building this infrastructure from scratch.

**Relayfile is for companies shipping cloud agents as a product — not for developers using agents as tools.**

This is the investor answer. It is also the correct ICP filter.

---

## The Core Problem

Every team shipping cloud agent products runs into the same class of failures. The names differ. The root cause is always the same: **the agent's view of the world is wrong by the time it acts.**

### Problem 1 — No persistent workspace

Cloud agents have no filesystem. Between runs, between retries, between handoffs to a different instance, state must be stored somewhere. The default solution is a database table or a blob in S3. Neither supports real-time reads, concurrent writes, or conflict detection. Every team builds an ad hoc version of a shared workspace and none of them generalize.

### Problem 2 — Stale reads

The agent reads a record from an external system, spends time reasoning or executing multi-step tasks, and by the time it writes back the record has changed. The agent never knew. It overwrites the new state with a response based on the old one.

This is the TOCTOU problem (time-of-check to time-of-use), endemic to every polling-based architecture. A 30-second polling interval means every agent action is based on state that is at least 30 seconds old. For cloud agents running long tasks, the drift can be minutes.

### Problem 3 — Silent concurrent writes

A human and a cloud agent both update the same record within seconds of each other. One wins. Neither knows the other acted. The human assumes their change is in effect. The agent assumes its change is in effect. Both are sometimes wrong.

There is no standard mechanism in Salesforce, Linear, Jira, or Notion to detect this at the application layer. Teams discover it in postmortems when a customer complains that their case was closed while they were still waiting for a response.

### Problem 4 — No awareness during long-running tasks

The cloud agent starts a multi-step task. Halfway through, the context changes: the human cancels the request, a higher-priority item arrives, the underlying data is updated. The agent has no channel to receive that signal. It finishes the task on a premise that is no longer true.

### Problem 5 — Webhook infrastructure rebuilt for every product

Every team building a cloud agent product builds the same pipeline from scratch: receive webhook, verify the signature (different format per provider), deduplicate, order, retry on failure, dead-letter, persist state, deliver to the agent. Then repeat for every provider.

This is undifferentiated infrastructure. Every Tier 1 ICP below has already built a version of this and is maintaining it instead of building their actual product.

### Problem 6 — State loss between agent sessions

A cloud agent run ends. The next run starts — scheduled, retried, or handed to a different instance — with no memory of what the previous run observed or changed. Teams paper over this with prompt injection, database flags, or manually constructed context strings. None of it is reliable under concurrent access or at scale.

---

## Why This Is Unserved

| Layer | Existing tools | What they do | What they miss |
|---|---|---|---|
| Tool execution | Composio, Merge, Executor | Call the API on demand | No state between calls |
| Data ingestion | Nango, Paragon Managed Sync | Get data in on a schedule | Polling latency; no conflict model |
| **Persistent agent workspace** | **Nobody** | — | **Real-time currency; conflict detection; state across runs** |

Composio and Merge solve execution. Nango and Paragon solve ingestion. Nobody has standardized the layer where the cloud agent holds its working state between runs, knows in real time when something changed underneath it, and detects conflicts before acting on outdated information.

---

## The Pitch

**Not:** "Relayfile is a coordination layer for multi-agent workflows."

**Not:** "Relayfile is for developers building with AI agents." (Too broad — includes local dev use cases where it adds no value.)

**Yes:** "Make your agent proactive with a few lines of code." — and underneath that: a persistent cloud workspace, real-time event delivery, conflict-safe writes, and state that survives between runs, with none of the webhook infrastructure built by you.

Multi-agent coordination is what customers grow into. The entry is simpler: the agent needs a workspace that exists between runs and knows when the world changed.

---

## ICPs Today

The right filter: **companies shipping cloud agents as a product**, where the agent runs in the vendor's infrastructure and has no local filesystem. Not developers using agents as tools on their own machines.

---

### Tier 1 — AI Automation Platforms

Cloud agents that take actions in users' SaaS tools on their behalf. The highest-density ICP: every company here is building the same coordination infrastructure from scratch, in their cloud, for every customer.

**Lindy, Relevance AI, Dust, n8n (agent mode), Zapier AI Actions, Make.com AI, Relay.app, Bardeen, Gumloop**

The problem: The agent runs in the vendor's cloud. It needs a workspace scoped to each customer, a real-time feed of what's happening in that customer's tools, and persistent state across runs. Today they build polling loops, database flags, and custom webhook handlers. Relayfile replaces all of it.

---

### Tier 1 — Autonomous Coding Agents

Cloud-hosted agents that work software tickets end-to-end without a human in the loop during execution.

**Cognition (Devin), Factory, SWE-agent deployments, GitHub Copilot Workspace (cloud mode)**

The problem: The agent runs in a cloud sandbox. It needs to know if the ticket scope changed mid-task, if a human pushed a conflicting change to the same branch, or if another agent instance was assigned to the same issue. There is no local filesystem — the working state must live somewhere durable between steps.

Note: Cursor and Windsurf are primarily local developer tools and are not the ICP. Cloud-hosted, fully-autonomous agents are.

---

### Tier 1 — AI Customer Support Platforms

Cloud agents that handle customer cases autonomously, often in parallel with human support agents working the same queue.

**Intercom (Fin), Zendesk AI, Salesforce Service Cloud AI, Freshdesk (Freddy), Kustomer AI, Forethought, Thankful**

The problem: The AI agent runs in the vendor's cloud and drafts a resolution. A human support agent opens the same case 15 seconds later. Neither knows the other is active. One resolution goes out. The customer receives two responses, or the human's work is silently overwritten.

These companies handle this today with queue locking and manual review — neither scales with AI volume.

---

### Tier 1 — AI Sales and CRM Automation

Cloud agents that enrich, update, and act on CRM records while humans are in active sales motions against the same data.

**Clay, Amplemarket, Apollo.io AI, Outreach AI, HubSpot AI workflows, Salesforce Einstein agents**

The problem: The enrichment agent runs in the vendor's cloud and updates contact fields. The sales rep opens the same contact during a live call and is looking at pre-enrichment data. Or the rep updates the deal stage while the agent is mid-write and one update is silently dropped.

---

### Tier 1 — AI DevOps and Incident Response

Cloud agents that triage and take remediation actions during incidents, running concurrently with on-call engineers.

**PagerDuty AI, Incident.io AI, Rootly AI, Grafana Incident AI, FireHydrant, Cortex**

The problem: The agent fires at 2am and starts running runbooks in the vendor's cloud. The on-call engineer wakes up and starts investigating independently. By the time they sync, the agent has taken actions the engineer didn't know about, and the engineer has updated the incident timeline with observations the agent didn't see.

---

### Tier 2 — AI-Embedded Project Management

Cloud-hosted AI features inside work management tools that triage, assign, and update issues concurrently with human team activity.

**Linear AI, Jira AI (Atlassian Intelligence), Asana AI, Monday.com AI, Notion AI**

The problem: The AI triage feature runs as a background cloud process touching the same backlog a team lead is manually processing. At scale — hundreds of issues per hour — the backlog becomes incoherent because neither writer knows what the other changed.

---

### Tier 2 — AI Code Review and PR Automation

Cloud agents that review pull requests based on a snapshot of the PR at trigger time.

**CodeRabbit, Greptile, Graphite, Trunk, Sourcegraph Cody (review mode)**

The problem: The review agent runs in the vendor's cloud against the PR as it existed when triggered. The author pushes a new commit mid-review. The review comments are now based on stale code. The agent has no way to know the code changed without being fully re-triggered.

---

### Tier 2 — Healthcare AI

Cloud agents that read and write to patient records alongside clinicians, where a stale or conflicted write is a compliance failure.

**Ambience Healthcare, Nabla, Abridge, Nuance (DAX), Suki, Notable Health**

The problem: The AI agent runs in the vendor's HIPAA-compliant cloud and pre-fills structured EHR fields while the clinician is simultaneously reviewing and dictating. An undetected conflict is not just a product bug — it is an auditable regulatory failure.

---

### Tier 2 — AI-Powered E-Commerce Operations

Cloud agents that manage pricing and inventory while human merchandisers are working the same catalog.

**Shopify AI automation, Feedonomics, Linnworks AI, Akeneo AI**

The problem: The AI pricing agent runs on a schedule in the vendor's cloud. The merchandiser is manually adjusting the same SKUs. One set of changes wins silently. The merchandiser's intentional strategy is overwritten, or the agent's campaign goes live at the wrong price.

---

### Tier 2 — AI DevOps and Incident Response *(see Tier 1)*

### Tier 3 — AI Research and Analysis Platforms

Cloud agents that synthesize information across sources into shared outputs.

**Perplexity (enterprise), Elicit, Brightwave, Hebbia**

The problem: Multiple research sessions on overlapping topics each build their own view of source material in the vendor's cloud, with no shared representation of what has been found or what contradicts another session's findings.

---

### Tier 3 — AI Content Operations

Cloud-hosted AI that generates and revises content concurrently with human editors.

**Jasper, Copy.ai, Writer**

The problem: Multiple contributors and AI drafts converge on the same content asset with no conflict detection when a human edit and an AI revision both modify the same section.

---

## The Build-vs-Buy Argument

Every company in Tier 1 has already built a version of this infrastructure. It took 4–12 weeks of engineering. It handles their specific topology and breaks when they add a new integration, change their agent framework, or scale to more concurrent runs.

This is undifferentiated infrastructure: expensive to build, expensive to maintain, not a competitive advantage, and blocking higher-value work. Relayfile is what every company on this list should be buying instead of building.

---

## Progression Path

Teams do not buy relayfile for multi-agent coordination on day one. The natural progression:

1. **Entry:** "Make my cloud agent proactive." Real-time event delivery, no polling, agent knows when the world changed.
2. **Expansion:** "Give my agent persistent state across runs." Workspace survives between sessions, picks up where it left off.
3. **Platform:** "Coordinate multiple agents and humans on the same shared records without conflicts." The full coordination layer.

The entry pitch is unambiguous and solvable today. The platform pitch is where relayfile's full value is captured as agentic architectures mature.
