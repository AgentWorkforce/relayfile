# Relayfile Market Framing

## The Framing Problem

"Multi-agent coordination layer" is the right description of what relayfile is, but it is the wrong pitch for today's market. Most teams are not running multiple peer agents writing to shared state. Pitching coordination for a pattern they don't yet have creates a timing mismatch — the product sounds like infrastructure for a future they haven't hit yet.

The market that exists today is larger and more immediate, and relayfile serves it directly.

---

## The Core Problem

Every team shipping AI automation runs into the same class of failures. The names differ. The root cause is always the same: **the agent's view of the world is wrong by the time it acts.**

### Problem 1 — Stale reads

The agent reads a record, spends time reasoning or executing, and by the time it writes back the record has changed. The agent never knew. It overwrites the new state with a response based on the old one.

This is the TOCTOU problem (time-of-check to time-of-use), and it is endemic to every polling-based agent architecture. A 30-second polling interval means every agent action is based on state that is at least 30 seconds old — often much more.

### Problem 2 — Silent concurrent writes

A human and an AI agent both update the same record within seconds of each other. One wins. Neither knows the other acted. The human assumes their change is in effect. The agent assumes its change is in effect. Both are sometimes wrong.

There is no standard mechanism in Salesforce, Linear, Jira, or Notion to detect this at the application layer. Teams discover it in postmortems when a customer complains that their case was closed while they were still waiting for a response.

### Problem 3 — No awareness during long-running tasks

The agent starts a multi-step task. Halfway through, the context changes: the human cancels the request, a higher-priority item arrives, the underlying data is updated by another process. The agent has no channel to receive that signal. It finishes the task on a premise that is no longer true.

### Problem 4 — State loss between sessions

An agent session ends. A new session begins — scheduled run, retry, handoff to a different agent — with no memory of what the previous session observed or changed. Teams paper over this with text files in prompts, database rows, or manually constructed context strings. All of it is ad hoc. None of it is reliable under concurrent access.

### Problem 5 — Webhook infrastructure rebuilt for every product

Every team building an agent product builds the same pipeline: receive webhook from Slack/GitHub/Jira/Linear, deduplicate, order, retry on failure, dead-letter on repeated failure, persist current state somewhere. Then build a second pipeline to deliver changes to the agent. Then a third to handle conflict resolution.

This is not differentiated work. It is undifferentiated infrastructure that every company is building from scratch because no standard exists.

---

## Why This Is Unserved

| Layer | Existing tools | What they do | What they miss |
|---|---|---|---|
| Tool execution | Composio, Merge, Executor | Call the API | No state between calls |
| Data ingestion | Nango, Paragon Managed Sync | Get data in on a schedule | Polling latency; no conflict model |
| **State between reads and writes** | **Nobody** | — | **Real-time currency; conflict detection; agent awareness** |

Composio and Merge solve execution. Nango and Paragon solve ingestion. Nobody has standardized the layer where the agent holds state between reads and writes, knows in real time when something changed underneath it, and detects conflicts before acting on outdated information.

---

## The Pitch

**Not:** "Relayfile is a coordination layer for multi-agent workflows."

**Yes:** "Relayfile is the real-time shared workspace that keeps your agent's view of the world current — without polling."

One agent. One human. The agent always knows when something changed, before it acts on stale state. That is the wedge. Multi-agent coordination is what customers grow into, not what they buy on day one.

---

## ICPs Today

### Tier 1 — AI Automation Platforms

Companies building agents that take actions in their users' SaaS tools on their behalf. This is the highest-density ICP: every company in this category is building the same coordination infrastructure from scratch.

**Lindy, Relevance AI, Dust, n8n (agent mode), Zapier AI Actions, Make.com AI, Relay.app, Bardeen, Gumloop**

The problem: An agent is mid-task on a user's Linear board or Salesforce pipeline. The user makes a change. The agent has no way to know. It completes the task on a premise that no longer holds. The fix today is polling every N seconds — slow, expensive, and still racy.

These companies are all solving this. Their solutions are all one-offs. Relayfile is the infrastructure they should be buying.

---

### Tier 1 — AI Coding Tools

Companies where AI agents take actions on codebases — often in parallel with human developers or other AI sessions.

**Cursor, Windsurf, Cognition (Devin), GitHub Copilot Workspace, Replit Agent, Sourcegraph Cody, Factory**

The problem: A developer has two Cursor sessions open on the same repo. Or Devin is working a ticket while the developer is also making changes. There is no shared representation of what changed and when. Conflicts are discovered at commit time or not at all. Agent sessions have no memory of what prior sessions did — every run starts from scratch.

The coordination need here is acute. These are fast-moving codebases with multiple writers. The cost of a stale-read conflict is a broken build or a reverted commit.

---

### Tier 1 — AI Customer Support Platforms

Companies where AI and human agents work the same support queue concurrently.

**Intercom (Fin), Zendesk AI, Salesforce Service Cloud AI, Freshdesk (Freddy), Kustomer AI, Forethought, Thankful**

The problem: An AI agent drafts a resolution to a ticket. A human support agent picks up the same ticket 15 seconds later and starts typing a different response. Neither knows the other is active. One response goes out. The customer receives two, or the human overwrites the AI response that was already marked as sent.

This is a real operational failure that these companies handle today with locking, queuing, or manual review — none of which scale with AI volume.

---

### Tier 1 — AI Sales and CRM Tools

Companies where AI enriches, updates, or acts on CRM records while humans are in active sales motions.

**HubSpot AI, Salesforce Einstein, Clay, Apollo.io AI, Outreach AI, Gong, Chorus, Amplemarket**

The problem: A sales rep is on a live call. Clay's AI is simultaneously enriching the contact record with new data and updating fields. The rep's view of the contact is now wrong — they're looking at what the record said before the enrichment ran. Or worse: the rep updates the deal stage while the AI is mid-write, and one of the updates is silently dropped.

CRM is high-stakes. A lost update to a deal stage or a wrong contact field costs real pipeline. These companies need the AI's writes to be aware of the human's activity.

---

### Tier 2 — AI-Embedded Project Management

Teams building AI features inside work management tools, or building on top of them with agents.

**Linear AI, Jira AI (Atlassian Intelligence), Asana AI, Monday.com AI, Notion AI, Height**

The problem: An AI triage agent is processing the backlog — labeling issues, assigning owners, setting priorities. A team lead is doing the same thing manually in the same queue. They are concurrently touching the same set of issues. The AI's changes overwrite the human's, or the human's overwrite the AI's. Neither party has visibility into what the other changed.

At scale — hundreds of issues processed per hour — this produces a backlog that neither the human nor the AI recognizes as accurate.

---

### Tier 2 — AI Code Review and PR Automation

Companies building AI agents that review pull requests, often alongside human reviewers and other AI tools.

**CodeRabbit, Greptile, Graphite, Trunk, Bloop, GitHub Copilot (review mode)**

The problem: Three things happen to a PR in the same 60-second window: a human reviewer leaves a comment, the author pushes a new commit, and the AI reviewer posts its analysis. The AI's analysis was based on the pre-push state. The human reviewer's comment thread is now stale. The author doesn't know which review comments are still valid.

These companies handle this today with re-trigger logic — re-run the AI review on every new commit. That is expensive and doesn't solve the staleness problem for comments already posted.

---

### Tier 2 — AI Document and Knowledge Platforms

Companies where AI and humans are concurrently editing, summarizing, or restructuring the same documents.

**Notion AI, Coda AI, Confluence AI (Atlassian), Google Workspace AI, Quip AI, Guru, Tettra**

The problem: A human is editing a wiki page. The AI is simultaneously re-summarizing it based on a recent meeting transcript. The human saves. The AI saves. One overwrites the other. The user sees their edits disappear, or the AI's summary is based on a version of the doc that no longer exists.

Document platforms have optimistic locking at the session level but not across the human/AI boundary — the AI writes through a service account that has no awareness of active human sessions.

---

### Tier 2 — AI DevOps and Incident Response

Companies where AI agents triage, diagnose, or act on incidents while on-call engineers are simultaneously investigating.

**PagerDuty AI, Incident.io AI, Rootly AI, Grafana Incident AI, FireHydrant, Cortex**

The problem: An incident fires at 2am. The AI agent starts diagnosing — pulling logs, running runbooks, updating the incident timeline. The on-call engineer wakes up and starts investigating independently. By the time they sync, the AI has taken actions the engineer didn't know about, and the engineer has updated the timeline with observations the AI didn't incorporate.

In incident response, a stale or conflicted state is directly dangerous. These companies need the AI's actions and the human's actions to be visible to each other in real time.

---

### Tier 2 — Healthcare AI

Companies building clinical AI that reads and writes to patient records alongside clinicians.

**Ambience Healthcare, Nabla, Abridge, Nuance (DAX), Suki, Notable Health**

The problem: A clinician is reviewing a patient chart and dictating notes. The AI is simultaneously generating a clinical summary and pre-filling structured fields. Both are writing to the EHR. If they conflict, clinical data integrity is compromised. In regulated environments, an undetected overwrite is a compliance failure.

The stakes here are higher than in any other category. These companies need conflict detection that is auditable and provable, not just best-effort.

---

### Tier 2 — AI-Powered E-Commerce Operations

Companies where AI manages inventory, pricing, and product listings while human merchandisers are actively working.

**Shopify (Sidekick and AI features), BigCommerce AI, Feedonomics, Linnworks AI, Akeneo AI**

The problem: A merchandiser is repricing a product category. The AI pricing agent is simultaneously running a discount campaign on the same SKUs. One set of changes wins. The merchandiser's intentional pricing strategy is silently overwritten, or the AI's campaign goes live at the wrong price.

---

### Tier 3 — AI Research and Analysis Tools

Companies where AI agents synthesize information into shared outputs that multiple users or agents contribute to.

**Perplexity (enterprise), Elicit, Consensus, Brightwave, Hebbia**

The problem: Multiple analysts are using AI to research the same topic. Each AI session builds a separate view of the source material. There is no shared representation of what has been found, what is still being investigated, and what contradictions exist. Teams end up with duplicate work and conflicting conclusions.

---

### Tier 3 — AI Content Creation Platforms

Companies where multiple AI drafts of the same content exist alongside human editing.

**Jasper, Copy.ai, Writer, Wordtune, Lex**

The problem: A content team has three people and an AI generating variations of the same landing page copy. All four are writing simultaneously. The "current version" is undefined. There is no conflict detection when two people accept different AI suggestions for the same paragraph.

---

### Tier 3 — Financial Services AI

Companies where AI analyzes portfolios, flags compliance issues, or generates trade recommendations while human advisors are active on the same accounts.

**Betterment, Wealthfront AI features, Addepar AI, Riskalyze AI, Compliance.ai**

The problem: The AI flags a compliance issue on an account and initiates a review workflow. The advisor closes the issue manually based on a client call. The AI's workflow continues against a resolved issue. Duplicate notifications go to the client.

---

## The Build-vs-Buy Argument

Every company in Tier 1 has already built a version of this infrastructure. It took 4–12 weeks of engineering. It handles their specific topology (their SaaS tools, their agent architecture, their polling intervals) and nothing else. It breaks when they add a new integration or change their agent framework.

This is classic undifferentiated infrastructure: expensive to build, expensive to maintain, not a competitive advantage, and blocking higher-value work.

Relayfile is the standard infrastructure layer that every company on this list should be buying instead of building.

---

## Progression Path

Teams do not buy relayfile for multi-agent coordination on day one. They buy it to stop building polling loops. The natural progression:

1. **Entry:** "Keep my agent's view of the world current without polling." Single agent, real-time awareness, no stale reads.
2. **Expansion:** "Let multiple agents share state without conflicts." Two or three agents on the same workspace.
3. **Platform:** "Coordinate arbitrary numbers of agents, humans, and automated processes on the same shared records." The full coordination layer.

The entry pitch is unambiguous and solvable today. The platform pitch is where relayfile's full value is captured as agentic architectures mature.
