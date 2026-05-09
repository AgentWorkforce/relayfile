# Relayfile Pricing

## Principles

- Charge on **events** (webhook ingestions + file state changes) — the best proxy for coordination work done
- Integrations are capped per tier to reflect real Nango infrastructure costs underneath
- Platform plans (Nango, Composio, Pipedream) serve customers who arrive via those ecosystems with pricing tuned to each
- Free tier is real enough to prototype with; not real enough to run in production

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

**Why $79:** Positioned above Composio Starter ($29) — we're not a tool call router, we're a coordination layer. Margins work at this price covering Nango Starter costs for 8 integrations.

---

### Growth — $499/month

For teams running agent products in production with real workloads.

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

**Why $499:** Covers Nango Growth costs (~$200/month for the integration infrastructure) at reasonable margin. Below Nango's own Growth plan ($500) only because relayfile is the coordination layer on top, not a replacement for Nango. Matches what a Composio Professional ($229) + Nango Starter ($50) customer would pay combined — and relayfile gives them both in one.

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
| SLA | 99.9% uptime |
| Support | Dedicated |

---

## Platform Plans

For customers who arrive via Nango, Composio, or Pipedream ecosystems. These plans are listed in each platform's marketplace or integration directory and priced to complement what the customer is already paying.

---

### Nango Plan — $299/month

For teams already running Nango who need the coordination layer on top.

Nango handles OAuth, token refresh, and scheduled sync. Relayfile connects to the customer's existing Nango instance and provides the real-time workspace, multi-agent coordination, and semantic layer on top. The customer keeps their Nango subscription separately.

| Resource | Limit |
|---|---|
| Workspaces | 5 |
| Integrations | Bring-your-own Nango (all connections the customer already has) |
| Events/month | 3M |
| Overage | $0.15/1K events |
| Forks + ACLs | ✓ |
| Audit logs | ✓ |
| Support | Priority |

**Positioning:** "You're already paying Nango for the sync layer. Relayfile makes that data usable by agents in real-time with no extra integration work." The customer's Nango connection count and sync quota come from their own Nango plan; relayfile only adds the coordination and fan-out layer.

**Listed on:** Nango integration directory, Nango partner page.

**Why $299 not $499:** The customer is already paying Nango ($50–$500/month). Asking for another $499 on top would make the total cost prohibitive. At $299 the combined spend is $349–$799, which is reasonable for a production agent team. Relayfile's infrastructure costs are lower here because Nango handles all the provider API calls; relayfile only processes events and manages state.

---

### Composio Plan — $199/month

For teams using Composio for tool calls who need persistent shared state between agents.

Composio handles tool execution and agent-to-API calls. Relayfile provides the persistent workspace that agents read from and write to between tool calls — the shared memory layer that makes multi-agent workflows coherent over time.

| Resource | Limit |
|---|---|
| Workspaces | 3 |
| Integrations | 10 (Composio handles the tool call layer) |
| Events/month | 1M |
| Overage | $0.20/1K events |
| Forks | ✓ |
| ACLs | ✗ (Growth add-on) |
| Support | Email |

**Positioning:** "Composio gives your agents hands. Relayfile gives them shared memory." A Composio Professional customer ($229/month) adding relayfile at $199/month pays $428/month total for a complete multi-agent stack: tool execution + persistent coordination workspace.

**Listed on:** Composio marketplace, Composio integration directory.

**Why $199:** Composio's own Pro plan is $229. Customers comparing total spend will anchor to that. At $199 relayfile feels like roughly equal weight in the stack, which reflects the actual split in responsibilities.

---

### Pipedream Plan — $149/month

For teams using Pipedream workflows who need a shared state layer between workflow steps and between agents running in parallel.

Pipedream handles event-driven workflow automation. Relayfile provides the shared workspace that multiple Pipedream workflows (and the agents they spawn) read from and write to — solving the coordination problem when parallel workflows need consistent shared state.

| Resource | Limit |
|---|---|
| Workspaces | 2 |
| Integrations | 6 (Pipedream handles event triggers and HTTP actions) |
| Events/month | 500K |
| Overage | $0.25/1K events |
| Forks | ✓ |
| ACLs | ✗ |
| Support | Email |

**Positioning:** "Pipedream orchestrates your workflows. Relayfile is the shared filesystem they all read and write consistently." Pipedream's own paid plans start around $29–$49/month; relayfile at $149 is a meaningful add-on but justified for teams hitting the coordination wall with parallel workflows.

**Listed on:** Pipedream marketplace, Pipedream integration directory.

**Why $149:** Pipedream's customer base skews toward developers and automation engineers who are cost-sensitive. $149 is the floor at which relayfile's infrastructure costs are covered for the event volume included. A Pipedream paid customer adding relayfile spends ~$200/month combined — reasonable for a production workflow infrastructure.

---

## Overage Rates Summary

| Plan | Included events | Overage per 1K |
|---|---|---|
| Free | 25K | Not available — upgrade required |
| Starter | 500K | $0.25 |
| Growth | 5M | $0.15 |
| Nango Plan | 3M | $0.15 |
| Composio Plan | 1M | $0.20 |
| Pipedream Plan | 500K | $0.25 |
| Enterprise | Negotiated | Negotiated |

---

## What Counts as an Event

One event = one of the following:

- A webhook received from a provider (Linear issue updated, Slack message posted, etc.)
- A file write via the REST API (`PUT /fs/file`)
- A bulk write operation counts as one event per file written
- A fork commit counts as one event per file in the fork

WebSocket fan-out to agents does **not** count as events — broadcasting one file change to 20 agents is still one event. Charging for fan-out would penalize multi-agent usage, which is the core value proposition.

---

## Competitive Anchoring

| | Composio Pro | Nango Growth | Relayfile Growth |
|---|---|---|---|
| Price | $229/month | $500/month | $499/month |
| What you get | Tool call routing | OAuth + sync infrastructure | Coordination layer + real-time workspace |
| Multi-agent coordination | ✗ | ✗ | ✓ |
| Real-time fan-out | ✗ | ✗ | ✓ |
| Forks + ACLs | ✗ | ✗ | ✓ |
| Semantic metadata + relations | ✗ | ✗ | ✓ |
| Builds on Nango | — | Is Nango | Optional |

Relayfile Growth at $499 costs roughly the same as Nango Growth ($500) but delivers the coordination layer they don't have. For a team already using Nango, the Nango Plan at $299 is the right entry point.
