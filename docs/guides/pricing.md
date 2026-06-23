# Pricing

Relayfile is priced on events — the normalized webhook and sync events flowing through your workspace each month. This maps naturally to how teams actually use it: more integrations and more active providers generate more events.

## Plans

| | Free | Starter | Team | Enterprise |
|---|---|---|---|---|
| **Price** | $0 | $49/mo | $199/mo | Custom |
| **Events/mo** | 10,000 | 100,000 | 500,000 | Unlimited |
| **Workspaces** | 1 | 1 | 5 | Unlimited |
| **Providers** | 2 | 5 | Unlimited | Unlimited |
| **Writeback** | — | ✓ | ✓ | ✓ |
| **Background supervisor** | — | ✓ | ✓ | ✓ |
| **Per-agent ACLs** | — | — | ✓ | ✓ |
| **Bring your own connections** | — | — | — | ✓ |
| **SLA + dedicated support** | — | — | — | ✓ |

## What counts as an event

An event is any normalized file event delivered to your workspace: a webhook arriving from a connected provider, a record created or updated during an incremental sync, or a digest write. Reads — agents reading files from a mounted workspace — do not count.

## Free tier

The free tier is enough to build and test a reactive agent locally. 10,000 events per month covers moderate usage across one or two providers. Any team running agents in production will want Starter or above.

## Bring your own connections (Enterprise)

If you already have provider connections managed through Nango, Composio, or Pipedream, Enterprise lets you bring those connections directly. Relayfile acts as the VFS and event layer on top of your existing OAuth infrastructure — no duplicate auth setup, no migration required.

## Overage

On Starter and Team, usage beyond the monthly event limit is billed at **$0.50 per 1,000 events**. You will receive an email before your workspace is throttled. Enterprise plans have no overage — contact us to discuss volume pricing.

## Hosted vs. self-hosted

The plans above apply to hosted Agent Relay. If you self-host the relayfile server, there is no per-event charge — you run the infrastructure and pay only for the compute and storage you provision. Self-hosting requires running [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters) and [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers) alongside the core server.

## Questions

[contact@agentrelay.com](mailto:contact@agentrelay.com)
