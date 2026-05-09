# Relayfile Pricing

## Positioning

Relayfile is not a better S3, a smarter webhook router, or a cheaper Nango. It is the **coordination layer** — the shared workspace where humans and agents read and write the same records at the same time, with conflict detection, real-time fan-out, forks, ACLs, and semantic metadata.

That distinction drives every pricing decision:

- **Mirage** is open-source and free. Competing on storage or caching is unsustainable.
- **Composio** has driven tool-call routing to near-zero ($0.15–$0.30/1K calls). Competing on routing price is a race to the bottom.
- **Nango** charges $500/month for sync infrastructure. Relayfile sits on top of Nango and charges the same amount for a qualitatively different layer.
- **MCP** is a free protocol. Relayfile's value is what happens after the tool call: persistent shared state, conflict detection, and real-time awareness.

The integrations where relayfile's value is highest are multi-writer collaboration tools — Linear, Jira, GitHub, Notion — where humans and agents are concurrently editing the same records. Storage backends (S3, GCS, R2) have softer concurrency problems and are lower priority.

---

## Billing Unit: Events

**One event = one webhook ingested from a provider, or one file write via the API.**

This is the right unit because:
- Seats don't map to AI agents — agents aren't humans, aren't persistent, and can't be counted like people
- Per-agent pricing is hard to enforce — agents are ephemeral processes that start and stop
- Events scale proportionally with coordination work done and grow with the value relayfile delivers

**Fan-out is always free.** Broadcasting one file change to 20 connected agents counts as one event, not 20. Charging for fan-out would penalize multi-agent usage — exactly the use case relayfile is built for.

Bulk writes count as one event per file written. Fork commits count as one event per file in the fork.

---

## Standard Plans

### Free — $0/month

For prototyping and individual developers evaluating the platform.

| Resource | Limit |
|---|---|
| Workspaces | 1 |
| Integrations | 3 |
| Events/month | 25K |
| Concurrent agent connections | 5 |
| Event history | 24 hours |
| Forks | ✗ |
| ACLs | ✗ |
| Support | Community |

The free tier is real enough to prototype with but not to run in production. Its purpose is conversion, not revenue.

---

### Starter — $79/month

For small teams shipping their first agent product.

| Resource | Limit |
|---|---|
| Workspaces | 2 |
| Integrations | 8 |
| Events/month | 500K |
| Overage | $0.25/1K events |
| Concurrent agent connections | Unlimited |
| Event history | 7 days |
| Forks | ✓ |
| ACLs | ✗ |
| Support | Email |

**Rationale:** Composio Starter is $29/month for 200K tool calls — pure routing with no coordination layer. Relayfile provides real-time fan-out, forks, and a persistent workspace that justifies a clear premium. $49 was considered but doesn't cover Nango Starter infrastructure costs (~$20–30/month for 8 integrations) at meaningful margin. $79 yields ~40% gross margin and positions relayfile above tool-call commoditization without being out of reach for a small team.

---

### Growth — $499/month

For teams running agent products in production.

| Resource | Limit |
|---|---|
| Workspaces | 10 |
| Integrations | All (24+) |
| Events/month | 5M |
| Overage | $0.15/1K events |
| Concurrent agent connections | Unlimited |
| Event history | 30 days |
| Forks | ✓ |
| ACLs | ✓ |
| Audit logs | ✓ |
| Support | Priority (Slack) |

**Rationale:** The Nango cost problem forced this number up from the initial $299 estimate. A Growth-tier customer using all integrations incurs ~$150–200/month in Nango infrastructure costs underneath. At $299 the margin is negative. At $499 it's ~60% gross margin — viable for a production service with support obligations. $499 also anchors neatly against Nango's own Growth plan ($500): relayfile costs the same as Nango but adds the entire coordination layer Nango doesn't have. Composio Professional is $229; relayfile at $499 is clearly premium but justified by multi-agent coordination, real-time fan-out, forks, and ACLs that Composio doesn't provide.

---

### Enterprise — Custom

For organizations with compliance requirements, large workloads, or deployment constraints.

| Resource | Limit |
|---|---|
| Workspaces | Unlimited |
| Integrations | Unlimited |
| Events/month | Negotiated |
| Concurrent agent connections | Unlimited |
| Event history | Custom retention |
| Forks + ACLs + Audit logs | ✓ |
| SSO / SAML | ✓ |
| On-premise deployment | ✓ |
| SLA | 99.9% uptime guarantee |
| Support | Dedicated |

At Enterprise scale the build-it-yourself cost (3–6 weeks engineering, $24K–$48K loaded cost plus ongoing maintenance) makes self-hosting economically irrational for most organizations. The SLA and on-premise option are the unlock for regulated industries.

---

## Platform Plans

Customers arriving via Nango, Composio, or Pipedream have already paid for adjacent infrastructure. Charging them the full Growth rate ($499) on top of their existing spend creates sticker shock that kills conversion. Platform plans are priced to complement what the customer already pays, serve as distribution via each platform's marketplace, and reflect lower infrastructure costs where the customer's existing subscription handles part of the stack.

---

### Nango Plan — $299/month

For teams already running Nango who need the coordination layer on top.

Nango handles OAuth, token refresh, and scheduled sync. Relayfile connects to the customer's existing Nango instance and adds the real-time workspace, multi-agent coordination, and semantic layer on top. The customer keeps their Nango subscription; relayfile only processes events and manages state.

| Resource | Limit |
|---|---|
| Workspaces | 5 |
| Integrations | Bring-your-own Nango (all connections the customer already has) |
| Events/month | 3M |
| Overage | $0.15/1K events |
| Forks + ACLs | ✓ |
| Audit logs | ✓ |
| Support | Priority |

**Rationale:** The customer is already paying Nango $50–$500/month. Another $499 on top makes the combined bill $549–$999, which is a hard sell. At $299 the combined spend is $349–$799 — reasonable for a production agent team. Relayfile's infrastructure costs are genuinely lower here because Nango handles all provider API calls; relayfile only processes the resulting events and manages workspace state. ~65% gross margin at this price.

**Pitch:** "You're already paying Nango for the sync layer. Relayfile makes that data usable by agents in real-time with no extra integration work."

**Distribution:** Nango integration directory, Nango partner page.

---

### Composio Plan — $199/month

For teams using Composio for tool calls who need persistent shared state between agents.

Composio handles tool execution and agent-to-API calls. Relayfile provides the persistent workspace that agents read from and write to between tool calls — the shared memory that makes multi-agent workflows coherent over time.

| Resource | Limit |
|---|---|
| Workspaces | 3 |
| Integrations | 10 (Composio handles the tool call layer) |
| Events/month | 1M |
| Overage | $0.20/1K events |
| Forks | ✓ |
| ACLs | ✗ (upgrade to Growth) |
| Support | Email |

**Rationale:** Composio Professional is $229/month. Anchoring relayfile at $199 positions both as roughly equal weight in the stack, which reflects the actual split in responsibilities: Composio executes, relayfile remembers. Combined spend of ~$428/month for a full multi-agent stack — tool execution plus persistent coordination workspace — is a coherent value story for a team that's hit the coordination wall.

**Pitch:** "Composio gives your agents hands. Relayfile gives them shared memory."

**Distribution:** Composio marketplace, Composio integration directory.

---

### Pipedream Plan — $149/month

For teams using Pipedream workflows who need a shared state layer between parallel workflows and agents.

Pipedream handles event-driven workflow automation. Relayfile provides the shared workspace that multiple parallel Pipedream workflows read from and write to consistently — solving the coordination problem when workflows running in parallel need coherent shared state.

| Resource | Limit |
|---|---|
| Workspaces | 2 |
| Integrations | 6 (Pipedream handles event triggers and HTTP actions) |
| Events/month | 500K |
| Overage | $0.25/1K events |
| Forks | ✓ |
| ACLs | ✗ |
| Support | Email |

**Rationale:** Pipedream's customer base skews toward developers and automation engineers who are cost-sensitive. Pipedream's own paid plans start around $29–$49/month; a $499 relayfile add-on would be a non-starter. $149 is the floor at which relayfile's infrastructure costs are covered for the event volume included. Combined spend of ~$200/month is accessible for a developer running production workflows.

**Pitch:** "Pipedream orchestrates your workflows. Relayfile is the shared filesystem they all read and write consistently."

**Distribution:** Pipedream marketplace, Pipedream integration directory.

---

## Overage Rates Summary

| Plan | Included events/month | Overage per 1K events |
|---|---|---|
| Free | 25K | Upgrade required |
| Starter | 500K | $0.25 |
| Growth | 5M | $0.15 |
| Nango Plan | 3M | $0.15 |
| Composio Plan | 1M | $0.20 |
| Pipedream Plan | 500K | $0.25 |
| Enterprise | Negotiated | Negotiated |

Overage rates decrease with plan tier — higher-paying customers get cheaper overages as a volume reward and to avoid penalizing successful deployments.

---

## Gross Margin Model

| Plan | Price | Est. Nango/infra cost | Est. gross margin |
|---|---|---|---|
| Free | $0 | ~$0 (within Nango free limits) | — |
| Starter | $79 | ~$20–30 | ~60% |
| Growth | $499 | ~$150–200 | ~60–70% |
| Nango Plan | $299 | ~$50–80 (relayfile infra only; customer pays Nango) | ~70–75% |
| Composio Plan | $199 | ~$40–60 | ~70% |
| Pipedream Plan | $149 | ~$30–40 | ~75% |
| Enterprise | custom | negotiated | 75%+ |

Platform plans have higher gross margins than standard plans because the customer's existing platform subscription covers the expensive provider API layer. Relayfile only runs the coordination infrastructure.

---

## Competitive Anchoring

| | Composio Pro | Nango Growth | Relayfile Growth |
|---|---|---|---|
| Price | $229/month | $500/month | $499/month |
| Purpose | Tool call routing | OAuth + sync infrastructure | Coordination layer + real-time workspace |
| Multi-agent conflict detection | ✗ | ✗ | ✓ |
| Real-time WebSocket fan-out | ✗ | ✗ | ✓ |
| Forks + ACLs | ✗ | ✗ | ✓ |
| Semantic metadata + relations | ✗ | ✗ | ✓ |
| Persistent shared workspace | ✗ | ✗ | ✓ |

Relayfile Growth ($499) costs roughly the same as Nango Growth ($500) but delivers the coordination layer neither Nango nor Composio have. For a team already using Nango, the Nango Plan ($299) is the right entry point — they keep Nango for what it's good at and add relayfile for what they're missing.
