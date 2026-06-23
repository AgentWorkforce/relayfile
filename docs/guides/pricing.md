# Pricing

Relayfile is priced on two dimensions: **events** and **storage**.

Events are the normalized webhooks and sync updates flowing into your workspace. Storage is the live integration data — every issue, page, PR, message, deal, and contact kept current and queryable as files. Both scale with how deeply your agents rely on your connected providers.

## Plans

| | Free | Starter | Team | Enterprise |
|---|---|---|---|---|
| **Price** | $0 | $49/mo | $199/mo | Custom |
| **Events/mo** | 10,000 | 100,000 | 500,000 | Unlimited |
| **Storage** | 1 GB | 10 GB | 50 GB | Custom |
| **Workspaces** | 1 | 1 | 5 | Unlimited |
| **Providers** | 2 | 5 | Unlimited | Unlimited |
| **Writeback** | — | ✓ | ✓ | ✓ |
| **Background supervisor** | — | ✓ | ✓ | ✓ |
| **Per-agent ACLs** | — | — | ✓ | ✓ |
| **Bring your own connections** | — | — | — | ✓ |
| **SLA + dedicated support** | — | — | — | ✓ |

## What counts as an event

An event is any normalized file event delivered to your workspace: a webhook arriving from a connected provider, or a record created or updated during an incremental sync. Reads — agents reading or mounting files — do not count.

## What counts as storage

Storage is the total size of your workspace file tree: all provider records kept live and queryable across your connected integrations. A typical team workspace with Linear, Notion, GitHub, and Slack connected uses 2–8 GB depending on history depth and record volume.

## Free tier

Enough to build and test a reactive agent with one or two providers. Any team running agents in production will want Starter or above.

## Overage

On Starter and Team, usage beyond plan limits is billed at:

- **Events:** $0.50 per 1,000 events
- **Storage:** $0.25 per GB per month

You will receive an email before your workspace is throttled. Enterprise plans have no overage — contact us to discuss volume.

## Bring your own connections (Enterprise)

If your team already has provider connections managed through Nango, Composio, or Pipedream, Enterprise lets you bring those directly. Relayfile acts as the VFS and event layer on top of your existing OAuth infrastructure — no duplicate auth setup, no migration required.

## Hosted vs. self-hosted

The plans above apply to hosted Agent Relay. If you self-host the relayfile server, there is no per-event or storage charge — you run the infrastructure and pay only for the compute and storage you provision. Self-hosting requires running [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters) and [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers) alongside the core server.

## Questions

[contact@agentrelay.com](mailto:contact@agentrelay.com)
