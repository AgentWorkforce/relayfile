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

## Product Thesis

Generic webhook tools answer, "Did the HTTP delivery arrive?"

RelayFile should answer, "What happened in the external system, what does it mean for this workspace, which files changed, and what should an agent safely do next?"

