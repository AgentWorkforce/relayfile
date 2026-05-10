# Trajectory: Define relayfile pricing strategy: tiers, platform plans, and rationale

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** May 9, 2026 at 08:55 AM
> **Completed:** May 9, 2026 at 08:57 AM

---

## Summary

Defined relayfile pricing: Free/$0, Starter/$79, Growth/$499, Enterprise/custom standard tiers; plus Nango/$299, Composio/$199, Pipedream/$149 platform plans. Billing unit is events (not seats/agents). Fan-out is free. Rationale for every number grounded in competitor pricing (Nango $500, Composio $229/$29) and Nango infrastructure cost model.

**Approach:** Standard approach

---

## Key Decisions

### Charge on events (webhook ingestions + file writes), not seats or agent connections
- **Chose:** Charge on events (webhook ingestions + file writes), not seats or agent connections
- **Reasoning:** Seats don't map to AI agents (agents aren't humans, aren't persistent). Per-agent pricing is hard to count — agents are ephemeral. Events are the best proxy for coordination work done and scale proportionally with value delivered. Fan-out to agent WebSocket connections is explicitly excluded from event counting: broadcasting one file change to 20 agents is one event, not 20, because penalizing multi-agent usage would undermine the core value proposition.

### Starter at $79/month (not $49)
- **Chose:** Starter at $79/month (not $49)
- **Reasoning:** Composio Starter is $29/month for 200K tool calls — pure routing, no coordination. Relayfile provides a qualitatively different layer (real-time fan-out, forks, persistent workspace) that justifies a premium. $49 was the initial estimate but doesn't cover Nango Starter infrastructure costs (~$20-30/month for 8 integrations) at meaningful margin. $79 leaves ~40% gross margin while positioning clearly above tool-call commoditization.

### Growth at $499/month (bumped from $299)
- **Chose:** Growth at $499/month (bumped from $299)
- **Reasoning:** The Nango cost problem: at $299/month you cannot cover a Nango Growth plan ($500/month) for a customer using all integrations. Nango Growth costs ~$150-200/month as a fraction of usage for a Growth-tier customer. $499 covers that at ~60% gross margin. $499 also matches Nango's own Growth price ($500) which is a useful anchor — relayfile costs the same as Nango but adds the coordination layer Nango doesn't have. Composio Professional is $229; relayfile at $499 is clearly premium but justified by the multi-agent coordination, real-time fan-out, forks, and ACLs that Composio doesn't provide.

### Create platform plans (Nango $299, Composio $199, Pipedream $149) rather than a single pricing ladder
- **Chose:** Create platform plans (Nango $299, Composio $199, Pipedream $149) rather than a single pricing ladder
- **Reasoning:** Customers arriving via Nango, Composio, or Pipedream have already paid for adjacent infrastructure. Charging them full Growth ($499) on top of their existing spend creates sticker shock that kills conversion. Platform plans are priced to complement what the customer already pays: Nango plan assumes customer's Nango subscription handles provider OAuth and sync — relayfile only adds coordination and fan-out, so infrastructure costs are lower, justifying $299. Composio plan anchors to Composio Pro ($229) — 'Composio gives agents hands, relayfile gives them shared memory' — combined spend ~$428 for a full multi-agent stack. Pipedream plan targets cost-sensitive developers; $149 is the floor covering event infrastructure. These plans also serve as distribution: listed on each platform's marketplace they generate leads from customers who are already agent-infrastructure buyers.

### Position relayfile as coordination layer, not storage/sync/routing competitor
- **Chose:** Position relayfile as coordination layer, not storage/sync/routing competitor
- **Reasoning:** Mirage is open-source and free — competing on storage/sync would require underpricing or matching free, which is unsustainable. Composio has driven tool-call routing to near-zero ($0.15-0.30/1K calls). The defensible position is the coordination layer: multi-agent concurrent writes with conflict detection (If-Match), forks for proposals, real-time WebSocket fan-out, ACLs per agent, and semantic metadata with cross-provider queries. None of MCP, Mirage, Nango, Composio, or Pipedream provide this. The pricing must reflect this positioning: we're not a better S3 or a smarter webhook router, we're the shared workspace where humans and agents work together on the same records at the same time. Integration value-add is highest for multi-writer collaboration tools (Linear, Jira, GitHub, Notion) — not storage backends — because that's where the coordination problem is hardest and relayfile's value is clearest.

---

## Chapters

### 1. Work
*Agent: default*

- Charge on events (webhook ingestions + file writes), not seats or agent connections: Charge on events (webhook ingestions + file writes), not seats or agent connections
- Starter at $79/month (not $49): Starter at $79/month (not $49)
- Growth at $499/month (bumped from $299): Growth at $499/month (bumped from $299)
- Create platform plans (Nango $299, Composio $199, Pipedream $149) rather than a single pricing ladder: Create platform plans (Nango $299, Composio $199, Pipedream $149) rather than a single pricing ladder
- Position relayfile as coordination layer, not storage/sync/routing competitor: Position relayfile as coordination layer, not storage/sync/routing competitor
- Pricing strategy fully defined across standard tiers and platform plans. Key insight: the Nango cost problem forced Growth from $299 to $499, which accidentally improved competitive positioning (now matches Nango Growth price while adding the coordination layer). Platform plans solve distribution AND conversion — priced to complement existing spend rather than compete with it.
