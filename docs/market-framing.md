# Relayfile Market Framing

## The Framing Problem

"Multi-agent coordination layer" is the right description of what relayfile is, but it is the wrong pitch for today's market. Most teams are not running multiple peer agents writing to shared state. Pitching coordination for a pattern they don't yet have creates a timing mismatch — the product sounds like infrastructure for a future they haven't hit yet.

The market that exists today is larger and more immediate, and relayfile serves it directly.

---

## The Real Market Today

### Human + AI on the Same Records

Every company shipping AI features is hitting this problem right now:

- A human updates a Linear ticket. The AI agent that was working on it doesn't know, and submits a PR that conflicts with what the human decided.
- A sales rep is on a call. HubSpot's AI rewrites the contact record mid-conversation.
- A human merges a PR. Copilot's agent was mid-review and now its suggestions are stale.
- A support agent picks up a ticket. The AI already drafted a response based on state that changed 30 seconds ago.

This is not a future problem. It is happening at every B2B SaaS company that has shipped AI features — which is now most of them.

The solutions in use today are all bad: polling loops with arbitrary intervals, database flags that drift, accepting stale reads, letting last-write-win and cleaning up conflicts manually. None of it is coordinated. None of it is real-time.

### AI Automation Companies

The more concentrated ICP: companies building agents that take actions in their users' SaaS tools on their behalf.

Lindy, Relevance AI, Dust, n8n's agent features, Zapier AI Actions, Make.com agents — these companies are all solving the same infrastructure problem from scratch:

- How do I know if a human changed something while my agent was running?
- How do I prevent two agent runs from conflicting on the same user record?
- How does the agent maintain a coherent view of state across a long-running task without polling every 10 seconds?

Every one of these companies has built an ad hoc version of the same thing: a polling loop, a database flag, a custom webhook handler, a job queue. None of it generalizes. None of it handles concurrent writes gracefully. None of it fans out to the agent in real time when the underlying data changes.

This is the market. It is large, it is today, and it is genuinely unserved.

---

## The Pitch

**Not:** "Relayfile is a coordination layer for multi-agent workflows."

**Yes:** "Relayfile is the real-time shared workspace that keeps your agent's view of the world current — without polling."

One agent. One human. The agent always knows when something changed, before it acts on stale state.

That is the wedge. Multi-agent coordination is what customers grow into, not what they buy on day one.

---

## Why This Market Is Unserved

| Layer | Existing tools | Gap |
|---|---|---|
| Tool execution | Composio, Merge, Executor | Call the API |
| Data ingestion | Nango, Paragon Managed Sync | Get the data in |
| **State between reads and writes** | **Nobody** | **Keep the agent's view current; detect when it goes stale** |

Composio and Merge solve the execution problem. Nango and Paragon solve the ingestion problem. Nobody has standardized the layer where the agent *holds* state between reads and writes, knows in real time when something changed, and detects conflicts before acting on outdated information.

That is relayfile's position. It is not adjacent to these tools — it sits on top of all of them.

---

## ICP Today

**Primary:** AI automation companies (teams building agents that act in users' SaaS tools). They are reinventing this infrastructure for every product they ship.

**Secondary:** B2B SaaS companies embedding AI features where AI and humans write to the same records (Notion, Linear, Intercom, HubSpot, Salesforce — and every vertical SaaS doing the same).

**Growing into:** Teams running parallel agents on shared workspaces — the multi-agent pattern that will become the norm as agentic architectures mature.

---

## Why Now

The number of teams shipping autonomous AI actions (not just AI suggestions) is accelerating. Every AI automation company that reaches production hits the coordination wall within months. The infrastructure they build to get past it is expensive, brittle, and not their core product. Relayfile is what they should be buying instead.

The framing shifts from "coordination layer" (a solution looking for a problem) to "no more stale agent state" (a problem every AI automation team is already paying to solve, badly).
