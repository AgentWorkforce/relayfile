# Relayfile Market Framing

## The Framing Problem

"Multi-agent coordination layer" is the right description of what relayfile is, but it is the wrong pitch for today's market. Most teams are not running multiple peer agents writing to shared state. Pitching coordination for a pattern they don't yet have creates a timing mismatch — the product sounds like infrastructure for a future they haven't hit yet.

The market that exists today is larger and more immediate. The academic research validates the architecture. The competitive landscape has clear gaps. And the enterprise opportunity is underserved by a small incumbent that relayfile can displace.

---

## Academic Validation

Before the market framing, the architecture: a 2024 paper from Tsinghua, Huawei, and collaborators — *"Proactive Agent: Shifting LLM Agents from Reactive Responses to Active Assistance"* (arXiv:2410.12361) — empirically validates relayfile's core design choices across a benchmark of 6,790 training events and 233 real-world test cases.

**What the paper proves:**

1. **Push architecture, not polling, is the correct model for proactive agents.** The paper formalizes proactive agents as event stream consumers. An agent that polls cannot be proactive by definition — it is always acting on state that is N seconds old.

2. **Persistent state per session is non-negotiable.** The paper proves mathematically that a stateless agent cannot infer intent. It has no basis for knowing what the user is building toward. Durable, queryable memory per session is a hard requirement, not an optimization.

3. **Shared environmental state is the coordination primitive.** The paper's "Environment Gym" — a shared workspace that both agents and the simulated user read and write — is structurally identical to relayfile's VFS. Coordination happens through shared state, not direct message passing.

4. **The hardest problem is calibrated restraint.** Without domain-specific fine-tuning, models "tend to provide as much assistance as possible, instead of necessary assistance." False alarms destroy user trust faster than missed detections. Even GPT-4o only achieves 64.60% F1 on proactive task prediction — the problem is hard at the frontier. This maps to relayfile's event normalization layer: not every webhook should fire the agent.

5. **Multi-candidate fan-out dramatically outperforms single proposals.** Proposing three candidate tasks (Pred@3) and letting users filter improved GPT-4o from 64.60% to 77.72% F1. This is relayfile's fan-out model validated: broadcast to multiple agents, let the right one handle it.

The paper's core quote: *"Currently, most existing LLM-based agents predominantly work in the reactive paradigm: they require explicit human instructions to initiate task completion and remain dormant in terms of providing services until prompted by user instructions."* That is the problem relayfile sells against.

---

## The Developer Pitch

**Make your agent proactive with a few lines of code.**

Today most cloud agents are reactive — they respond when called. Relayfile makes them proactive: the agent knows when something changed in the outside world and acts without being polled or manually triggered. A Jira ticket moves to blocked, the agent is notified. A PR is merged, the agent updates its working state. A customer sends a message, the agent picks it up in real time.

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

MindStudio, a popular agent-building platform, documents this gap directly in their blog: they recommend using Jira or Linear tickets as the shared state layer for multi-agent systems. That is a workaround, not a solution — and it is relayfile's market.

### Problem 2 — Stale reads

The agent reads a record from an external system, spends time reasoning or executing multi-step tasks, and by the time it writes back the record has changed. The agent never knew. It overwrites the new state with a response based on the old one.

This is the TOCTOU problem (time-of-check to time-of-use), endemic to every polling-based architecture. A 30-second polling interval means every agent action is based on state that is at least 30 seconds old. For cloud agents running long tasks, the drift can be minutes. The arXiv paper proves this is not solvable with better polling — the architecture must change to push.

### Problem 3 — Silent concurrent writes

A human and a cloud agent both update the same record within seconds of each other. One wins. Neither knows the other acted. The human assumes their change is in effect. The agent assumes its change is in effect. Both are sometimes wrong.

There is no standard mechanism in Salesforce, Linear, Jira, or Notion to detect this at the application layer. Teams discover it in postmortems when a customer complains that their case was closed while they were still waiting for a response.

### Problem 4 — No awareness during long-running tasks

The cloud agent starts a multi-step task. Halfway through, the context changes: the human cancels the request, a higher-priority item arrives, the underlying data is updated. The agent has no channel to receive that signal. It finishes the task on a premise that is no longer true.

### Problem 5 — Webhook infrastructure rebuilt for every product

Every team building a cloud agent product builds the same pipeline from scratch: receive webhook, verify the signature (different format per provider), deduplicate, order, retry on failure, dead-letter, persist state, deliver to the agent. Then repeat for every provider.

This is undifferentiated infrastructure. Every Tier 1 ICP below has already built a version of this and is maintaining it instead of building their actual product. See `docs/guides/proactive-agents.md` for the concrete before/after.

### Problem 6 — State loss between agent sessions

A cloud agent run ends. The next run starts — scheduled, retried, or handed to a different instance — with no memory of what the previous run observed or changed. Teams paper over this with prompt injection, database flags, or manually constructed context strings. None of it is reliable under concurrent access or at scale.

---

## The Competitive Landscape

### What exists today and where it falls short

**MindStudio** ($20/mo, 150K+ agents deployed): No-code visual workflow builder. 1,000+ integrations, 200+ models. Their proactive agent pattern is a "heartbeat" — polling every N minutes, wake, query APIs, reason, act. Their webhook trigger uses URL obscurity with no signature verification. They do not have a persistent shared workspace. Their own blog posts document the gap: they recommend file-based memory, shared SQLite, and issue trackers as state layers — all workarounds for the missing coordination layer. MindStudio is a natural distribution channel for relayfile, not a competitor.

**Tonkean** (~$10K+/mo, F500 customers including Google, Workday, OpenAI): Enterprise G&A orchestration for procurement, legal, finance, and HR. The most technically sophisticated incumbent. Their patented async state engine handles durable workflow state with pause/resume on external events. Their context graph captures human decisions and cross-run lineage. Their proactive agents use webhook triggers and scheduled monitoring. This is real, well-designed infrastructure — for enterprise internal operations teams.

Tonkean's moat is depth in G&A workflows. Their vulnerability is narrowness: they are an internal ops tool, not infrastructure for companies building agent products. They do not expose an SDK. They do not support companies shipping agents to their own customers. They raised $83M total with a $50M Series B in 2021 — four years ago, with no publicized Series C. They are a mid-sized company in a category that is about to get much larger.

**Lindy, Relevance AI, Dust, n8n agents**: AI automation platforms that patch the state problem with workarounds (conversation memory, per-agent variables, ad hoc databases). None have a proper coordination layer. All are building for the same ICP as relayfile and all are paying the infrastructure tax.

**Composio, Merge, Nango, Paragon**: Tool execution and data ingestion layers. They solve how agents call APIs and how data gets in. None of them solve the coordination layer — what the agent holds between reads and writes.

### The gap

| Layer | Existing tools | Gap |
|---|---|---|
| Tool execution | Composio, Merge, Executor | No state between calls |
| Data ingestion | Nango, Paragon | Polling latency; no conflict model |
| Workflow building | MindStudio, Lindy, Relevance AI | Ad hoc state workarounds |
| Enterprise G&A ops | Tonkean | Only internal ops, not agent product companies; narrow vertical; stale funding |
| **Persistent agent workspace** | **Nobody at the infrastructure layer** | **Real-time push, conflict detection, state across runs, for any use case** |

---

## The Enterprise Opportunity

Tonkean proves the enterprise pays for sophisticated agent state management. Their customers are paying $10K+/month for durable workflow state, context graphs, and proactive orchestration. The market is real.

Tonkean's weaknesses are relayfile's opening:

**Vertical lock-in.** Tonkean is purpose-built for G&A (procurement, legal, finance). An enterprise engineering team, IT ops team, or AI product team cannot use Tonkean for their use case. Relayfile is horizontal — the same coordination layer works for any agent workflow in any function.

**Closed ecosystem.** Tonkean does not expose an SDK. You cannot embed Tonkean's state engine into your own product. Companies building internal AI agents or shipping AI features to their customers cannot use Tonkean as infrastructure — they must build on top of their workflow builder or not use it at all. Relayfile is infrastructure-first: the SDK is the product.

**Funding gap.** The last publicized Tonkean round was a $50M Series B in 2021. The Cinch acquisition (December 2025) signals they needed inorganic growth. A well-capitalized, horizontally-positioned competitor with a developer-first go-to-market can move faster.

**No agent product company story.** Tonkean targets procurement teams at large enterprises. Relayfile targets the companies those enterprises are buying AI products from — and the enterprises themselves building internal AI agents. These are different buyers with different needs, and Tonkean cannot serve both.

### Enterprise go-to-market

The enterprise play is not head-to-head with Tonkean on G&A workflow orchestration. It is the adjacent and larger opportunity: enterprises building internal AI agents across engineering, IT, customer success, and operations — and enterprises that are AI product companies themselves (Salesforce, SAP, ServiceNow building AI features for their customers).

**Entry points:**

1. **Engineering and DevOps teams at enterprises** — internal agents for code review automation, incident response, deployment coordination. Relayfile's integrations with GitHub, Linear, Jira, and PagerDuty are strongest here. The buyer is the VP of Engineering or Platform team, not procurement.

2. **Enterprise AI product teams** — the team at Salesforce building Einstein, at SAP building Joule, at Zendesk building Fin. They need the coordination layer for the AI features they're shipping to their customers. Relayfile is infrastructure for their product, not a workflow tool for their ops.

3. **System integrators** — Accenture, Deloitte, KPMG building AI solutions for enterprise clients. They need reusable coordination infrastructure they can embed in every engagement. Relayfile as a partner platform accelerates their delivery.

**Enterprise requirements relayfile already has:**
- On-premise and self-hosted deployment (Enterprise plan)
- SSO / SAML
- Audit logs
- ACLs (workspace-level and file-level via relayauth)
- 99.9% SLA
- Dedicated support

**What the enterprise pitch looks like:**

*"Every enterprise building internal AI agents is writing the same infrastructure from scratch — webhook pipelines, state stores, conflict handling. Relayfile is that infrastructure, deployable on-premise, with the audit logs and access controls your security team requires. We are what Tonkean's state engine would be if it were an SDK you could embed anywhere, not a workflow builder you have to use."*

---

## ICPs

### Tier 1 — AI Automation Platforms (Primary, Near-Term)

Cloud agents that take actions in users' SaaS tools on their behalf. Every company here is building the same coordination infrastructure from scratch, in their cloud, for every customer.

**Lindy, Relevance AI, Dust, n8n (agent mode), Zapier AI Actions, Make.com AI, Relay.app, Bardeen, Gumloop**

The agent runs in the vendor's cloud. It needs a workspace scoped to each customer, a real-time feed of what's changing in that customer's tools, and persistent state across runs. Today they build polling loops, database flags, and custom webhook handlers. Relayfile replaces all of it.

---

### Tier 1 — Autonomous Coding Agents

Cloud-hosted agents that work software tickets end-to-end without a human in the loop during execution.

**Cognition (Devin), Factory, SWE-agent deployments, GitHub Copilot Workspace (cloud mode)**

The agent runs in a cloud sandbox with no local filesystem. Working state must live somewhere durable between steps. The agent needs to know if ticket scope changed mid-task, if a human pushed a conflicting commit, or if another instance was assigned to the same issue.

Note: Cursor and Windsurf are primarily local developer tools and are not this ICP. Cloud-hosted autonomous agents are.

---

### Tier 1 — AI Customer Support Platforms

Cloud agents handling customer cases autonomously, in parallel with human agents on the same queue.

**Intercom (Fin), Zendesk AI, Salesforce Service Cloud AI, Freshdesk (Freddy), Kustomer AI, Forethought, Thankful**

The AI agent runs in the vendor's cloud and drafts a resolution. A human support agent opens the same case 15 seconds later. Neither knows the other is active. Queue locking and manual review are the current solutions — neither scales with AI volume.

---

### Tier 1 — AI Sales and CRM Automation

Cloud agents enriching, updating, and acting on CRM records while humans are in active sales motions.

**Clay, Amplemarket, Apollo.io AI, Outreach AI, HubSpot AI workflows, Salesforce Einstein agents**

The enrichment agent runs in the vendor's cloud and updates contact fields. The sales rep opens the same contact during a live call and is looking at pre-enrichment data. Or the rep updates the deal stage while the agent is mid-write and one update is silently dropped.

---

### Tier 1 — AI DevOps and Incident Response

Cloud agents triaging and taking remediation actions during incidents, running concurrently with on-call engineers.

**PagerDuty AI, Incident.io AI, Rootly AI, Grafana Incident AI, FireHydrant, Cortex**

The agent fires at 2am and starts running runbooks. The on-call engineer wakes up and starts investigating independently. By the time they sync, the agent has taken actions the engineer didn't know about, and the engineer has updated the timeline with observations the agent didn't see.

---

### Tier 2 — Enterprise Internal AI Agents (Enterprise Play — Tonkean Territory)

Large enterprises building internal AI agents across engineering, IT, finance, HR, and operations. This is the market Tonkean currently serves for G&A workflows — but Tonkean cannot serve engineering, IT, or product teams, and cannot be embedded in enterprise-built AI products.

**F500 engineering teams, enterprise platform teams, AI centers of excellence**

The agent runs in the enterprise's own infrastructure. It needs a coordination layer that works with their existing tools (Jira, ServiceNow, GitHub, Salesforce), can be deployed on-premise, passes their security review (SOC 2, HIPAA, FedRAMP as applicable), and exposes the audit trail their compliance team requires.

Tonkean serves procurement teams in this segment. Relayfile serves engineering, IT, and AI product teams — and is embeddable as SDK infrastructure rather than requiring adoption of a new workflow builder.

**The pitch:** *"Tonkean's state engine for your use case, deployed on your infrastructure, as an SDK you can embed anywhere."*

---

### Tier 2 — AI-Embedded SaaS Products (Enterprise Product Companies)

Large SaaS companies building AI features where AI and humans write to the same records.

**Salesforce (Einstein), SAP (Joule), ServiceNow AI, HubSpot AI, Zendesk AI, Atlassian Intelligence**

These companies are shipping AI agents that operate on the same records as their customers' human users. Their AI features need conflict detection, real-time awareness, and state persistence — infrastructure that their current platforms don't provide. Relayfile is the coordination layer they embed in their product.

---

### Tier 2 — AI-Embedded Project Management

Cloud-hosted AI features inside work management tools that triage, assign, and update issues concurrently with human team activity.

**Linear AI, Jira AI (Atlassian Intelligence), Asana AI, Monday.com AI, Notion AI**

The AI triage feature runs as a background cloud process touching the same backlog a team lead is manually processing. At scale — hundreds of issues per hour — the backlog becomes incoherent because neither writer knows what the other changed.

---

### Tier 2 — AI Code Review and PR Automation

Cloud agents reviewing pull requests based on a snapshot of the PR at trigger time.

**CodeRabbit, Greptile, Graphite, Trunk, Sourcegraph Cody (review mode)**

The review agent runs against the PR as it existed when triggered. The author pushes a new commit mid-review. The review comments are now based on stale code. The agent has no way to know the code changed without being fully re-triggered.

---

### Tier 2 — Healthcare AI

Cloud agents reading and writing to patient records alongside clinicians.

**Ambience Healthcare, Nabla, Abridge, Nuance (DAX), Suki, Notable Health**

An undetected conflict between the AI's write and the clinician's write is not just a product bug — it is an auditable regulatory failure. These companies need conflict detection that is provable, logged, and compliant.

---

### Tier 2 — AI-Powered E-Commerce Operations

Cloud agents managing pricing and inventory while human merchandisers are working the same catalog.

**Shopify AI automation, Feedonomics, Linnworks AI, Akeneo AI**

The AI pricing agent runs on a schedule and applies discount rules. The merchandiser is manually adjusting the same SKUs. One set of changes wins silently.

---

### Tier 3 — AI Research and Analysis Platforms

**Perplexity (enterprise), Elicit, Brightwave, Hebbia**

Multiple research sessions on overlapping topics with no shared representation of findings, contradictions, or in-progress work.

---

### Tier 3 — AI Content Operations

**Jasper, Copy.ai, Writer**

Multiple contributors and AI drafts converging on the same asset with no conflict detection.

---

## The Build-vs-Buy Argument

Every company in Tier 1 has already built a version of this infrastructure. It took 4–12 weeks of engineering. It handles their specific topology and breaks when they add a new integration, change their agent framework, or scale to more concurrent runs.

Tonkean proves the enterprise pays $10K+/month for a pre-built version of this. MindStudio's blog content proves the SMB pays in engineering time for workarounds. The arXiv paper proves the architecture (push events + persistent shared state) is the correct solution.

Relayfile is what every company on this list should be buying instead of building — at a price point calibrated to their scale, deployable on their infrastructure, embeddable in their product.

---

## Progression Path

Teams do not buy relayfile for multi-agent coordination on day one. The natural progression:

1. **Entry:** "Make my cloud agent proactive." Real-time event delivery, no polling, agent knows when the world changed. Single agent, immediate value.
2. **Expansion:** "Give my agent persistent state across runs." Workspace survives between sessions, picks up where it left off. Multiple runs, coherent behavior.
3. **Platform:** "Coordinate multiple agents and humans on the same shared records without conflicts." The full coordination layer. The Tonkean-level problem, solved at the infrastructure level for any use case.

The entry pitch is unambiguous and solvable today. The enterprise pitch is the Tonkean displacement play for teams that cannot or will not adopt a G&A-specific workflow builder. The platform pitch is where relayfile's full value is captured as agentic architectures mature across every industry.
