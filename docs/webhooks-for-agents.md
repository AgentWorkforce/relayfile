# Webhooks for Agents

## Positioning

RelayFile should be the webhook intelligence layer for agents.

We do not just receive third-party webhooks. We understand them, normalize them, document their quirks, make them replayable, and turn them into trustworthy agent context.

Hookdeck is the right shape for the edge gateway: it can provide public ingress URLs, buffering, replay, rate limiting, delivery logs, local development, and operational dashboards. That lets us avoid rebuilding generic webhook transport infrastructure. RelayFile and Cloud should own the semantic layer: verification, raw event journals, idempotency state, provider-specific webhook catalogs, privacy-aware storage rules, normalized file projections, and agent-facing summaries.

## Architecture

```text
Provider webhook
  -> Nango webhook endpoint
  -> Hookdeck edge gateway
  -> Cloud webhook ingress
  -> durable raw event journal
  -> provider-aware normalization workers
  -> RelayFile VFS updates
  -> Relaycast agent notifications
```

Nango remains useful for provider auth, connection attribution, provider-specific webhook URLs, and forwarded webhook signatures. Hookdeck absorbs webhook delivery risk before traffic reaches Cloud. Cloud verifies Nango signatures and journals each event before any business logic runs. RelayFile turns normalized events into files, revisions, relations, and replayable sync history. Relaycast can broadcast the meaningful parts to agent teams.

## What We Store

Each provider should have structured webhook intelligence, not just a link to docs:

- registration methods and required OAuth scopes
- source verification headers and signature algorithms
- provider retry behavior, timeout behavior, and ordering guarantees
- idempotency and delivery identifier headers
- event names and payload schema examples
- subscription lifecycle rules, including refresh or expiration requirements
- privacy/storage guidance for raw payloads and normalized fields
- replay behavior and safe reprocessing notes
- agent-facing interpretation rules

For Jira, that means capturing details such as `manage:jira-webhook`, supported event names, JQL filtering behavior, webhook URL restrictions, HMAC verification for admin webhooks, OAuth/dynamic webhook behavior, retry headers, delivery identifiers, and refresh requirements.

## Relaycast Angle

Relaycast should not be the raw webhook ingress path for third-party providers. It should be the agent communication surface for already-normalized events.

Good Relaycast messages are concise and actionable:

```text
jira.issue.updated
ACME-123 changed status from "In Review" to "Blocked".

Affected files:
- /jira/issues/ACME-123.json
- /jira/projects/ACME.json

Suggested agent action:
Review linked implementation notes and decide whether to update the release plan.
```

This gives agents a shared event stream without forcing them to parse raw provider payloads. The raw payload remains in the Cloud/RelayFile event journal for audit and replay; Relaycast carries the human- and agent-readable briefing.

## What Agents Have to Do Today

Cloudflare's agents documentation shows what receiving a webhook requires without relayfile:

1. Expose an HTTP endpoint with a public URL
2. Verify the provider's HMAC signature — different header name, algorithm, and format per provider
3. Return HTTP 200 within seconds to prevent the provider from retrying
4. Extract an entity identifier from the URL, headers, or JSON body to route the right agent instance
5. Persist the raw payload to a database
6. Deduplicate against already-processed event IDs
7. Parse the raw JSON and implement provider-specific logic to understand what changed
8. Broadcast relevant state changes to connected clients

This is per provider. GitHub, Stripe, Slack, Jira, Linear, and Notion all use different signature headers, different retry behaviors, different payload schemas, and different registration flows. Each provider requires its own endpoint, its own verification logic, and its own parsing code.

After all of that, the agent still only knows "the HTTP delivery arrived." It has a raw JSON blob. It still has to figure out what changed, which of its working files are affected, and what to do next.

Most teams building agent products implement this once, get it mostly right, and never revisit it. The result is a brittle custom pipeline that handles two or three providers, has no replay capability, and breaks silently when providers change their schemas.

## The Relayfile Difference: No Webhooks for the Agent

From the agent's perspective, relayfile eliminates webhook infrastructure entirely.

The agent does not expose endpoints. It does not verify signatures. It does not register with providers. It does not write deduplication logic or build routing tables.

The agent connects to its workspace. When something changes in an external system — a Jira issue is updated, a GitHub PR is merged, a Linear ticket is reassigned — the agent receives a structured notification through the workspace's existing connection. The affected files are already updated in the VFS. The event is already in the journal. The agent reads what changed and acts.

The entire provider-specific layer — endpoint registration, signature verification, retry handling, payload normalization, entity routing — is handled by the relayfile pipeline once, and never surfaces to the agent at all.

This is the correct split: the complexity lives in relayfile's provider catalog once, not in every agent team's codebase repeatedly.

## Product Thesis

Generic webhook tools answer, "Did the HTTP delivery arrive?"

Building your own agent webhook handler answers, "Did it arrive, and did I parse it correctly for this one provider?"

RelayFile answers, "What happened in the external system, what does it mean for this workspace, which files changed, and what should an agent safely do next?" — and the agent doesn't touch any of the infrastructure required to get there.
