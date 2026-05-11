# Proactive Runtime Contract

This document captures the `relayfile` surfaces that the proactive runtime expects across milestones. M1 only adds contract markers and typed SDK stubs. Data-trigger delivery, change-log reads, and adapter implementations land in M2+.

## M1 Scope

M1 establishes stable public names so the gateway and `@agent-relay/*` packages can compile against the final surface now and replace the stubbed implementations later without renaming APIs.

The TypeScript SDK exposes two M1 stubs:

- `subscribe(globs, onChange)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.
- `getResourceAtEvent(eventId)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.

## DLQ Path Scheme

The proactive runtime gateway writes final-failure events into the workspace filesystem at:

```text
/_dlq/<workspace>/<event-id>.json
```

This path scheme is the cross-repo contract between the gateway and `relayfile`.

Requirements:

- The path is treated like a normal workspace file path for write, read, list, and delete semantics.
- The gateway owns writes to `/_dlq/**`; agents and tooling may inspect, replay, or purge these files later.
- The `<workspace>` segment is part of the durable path so multi-workspace tooling can list DLQ contents without inferring origin from the JSON body.

## `subscribe(globs[], onChange)` Contract

`relayfile` will expose:

```ts
subscribe(globs: string[], onChange: (event: ChangeEvent) => void): Subscription
```

M1 behavior:

- The method is present on the public SDK.
- The method throws `M2_NOT_IMPLEMENTED`.

M2 behavior:

- The client registers one or more path globs for change notifications.
- Delivery is multiplexed over WebSocket transport.
- Each logical occurrence has a stable `event.id` so downstream runtime dedup can key on `(workspace, agentId, event.id)`.
- The shape aligns with the proactive runtime notification envelope so the M2 event SDK can alias it directly.

### `ChangeEvent` shape

The `ChangeEvent` contract is notification-first. It is not the raw provider payload and it is not the older filesystem event feed shape.

```ts
type ChangeEvent = {
  id: string;
  workspace: string;
  type: "relayfile.changed";
  occurredAt: string;
  attempt: number;
  resource: {
    path: string;
    kind: string;
    id: string;
    provider: string;
  };
  summary: {
    title?: string;
    status?: string;
    priority?: string;
    labels?: string[];
    actor?: { id: string; displayName?: string };
    fieldsChanged?: string[];
    tags?: string[];
  };
  digest?: string;
};
```

Notes:

- `id` must be stable across retries and redelivery for the same logical change.
- `resource.path` points at the canonical normalized payload inside relayfile.
- `summary` stays small and routing-friendly; large payload fields remain in VFS until explicitly fetched.
- `digest` is the hash of the current canonical resource state when available.

### `Subscription` shape

The SDK exports:

```ts
type Subscription = {
  unsubscribe(): void;
};
```

M2 will return an active subscription handle that unregisters the watch when `unsubscribe()` is called.

## Change-Log Retention

The proactive runtime depends on a replayable change log so the gateway and SDK can resolve `event.id` back to the resource that produced it.

Contract:

- Default retention is at least 7 days on the Growth tier.
- Retention is configurable for higher tiers and self-hosted deployments.
- The retained log must be sufficient to support replay-on-start, `getResourceAtEvent(eventId)`, and future diff expansion.

M1 only documents this requirement. Storage implementation lands in M2.

## Path-Scoped Subscriptions

The proactive runtime will use path-scoped subscriptions for multi-agent ACLs.

Contract:

- Subscription delivery is filtered against the caller's effective path scope before a change event is emitted.
- Paths outside the caller's scope are silently omitted rather than partially redacted.
- The scope model follows workspace and path-scoped token rules introduced in M5.

M1 only documents the contract. Enforcement lands with the M5 auth work.

## `getResourceAtEvent(eventId)` Contract

`relayfile` will expose:

```ts
getResourceAtEvent(eventId: string): Promise<{
  path: string;
  data: unknown;
  digest: string;
}>
```

M1 behavior:

- The method is present on the public SDK.
- The method throws `M2_NOT_IMPLEMENTED`.

M2 behavior:

- Resolve `eventId` through the retained change log.
- Read the canonical normalized payload stored at the corresponding resource path.
- Return the resource path, normalized payload, and canonical digest.

This is the read path used by proactive runtime `expand("full")` for relayfile-backed events.

## `buildSummary(payload)` Adapter Contract

Each `relayfile-adapters` provider package must export:

```ts
type EventSummary = {
  title?: string;
  status?: string;
  priority?: string;
  labels?: string[];
  actor?: { id: string; displayName?: string };
  fieldsChanged?: string[];
  tags?: string[];
};

function buildSummary(payload: unknown): EventSummary;
```

Contract requirements:

- Extract only lightweight routing fields.
- Strip PII: no full email addresses, phone numbers, or government identifiers.
- Cap `title` at 120 characters.
- Cap `tags` at 8 entries.
- Keep the serialized summary under roughly 1 KB.
- Populate `fieldsChanged` for `.updated`-style events when the provider exposes a previous-state diff.

M1 only documents this contract. Per-provider implementations land in M2.

### Per-provider summary fields

| Provider | title | status | priority | labels | fieldsChanged | thread |
|---|---|---|---|---|---|---|
| Linear | `issue.title` | `state.name` | priority label | `labels[].name` | diff vs `previousData` | comments |
| GitHub | `pull_request.title` or `issue.title` | `state` | — | `labels[].name` | from changes object | review comments + comments |
| Slack | first 80 chars of text | — | — | — | — | thread replies |
| Notion | page title or database title | — | — | tags | property changes | comments |
| Jira | `summary` | `status.name` | `priority.name` | `labels` | from changelog | comments |
| Salesforce | `Name` | `Status__c` | `Priority__c` | — | per sObject diff | Chatter feed |
| HubSpot | `dealname` or `firstname` | `dealstage` | priority | — | from property change metadata | engagements |
| Stripe | `description` | `status` | — | — | from `previous_attributes` | — |
| Asana | `task.name` | `task.completed` mapped to `done` or `open` | — | `task.tags` | from change action metadata | task stories |
| Zendesk | `ticket.subject` | `ticket.status` | `ticket.priority` | `ticket.tags` | from event comments or change payload | ticket comments |
| Shopify | `order.name` or `product.title` | `order.fulfillment_status` | — | tags | from webhook topic or diff data | — |
| Airtable | first text-cell value | — | — | — | from `changedFieldIds` | — |
| Mailgun | event type | event status | — | — | — | — |
| ClickUp | `task.name` | `task.status` | `task.priority` | `task.tags` | from `history_items` | task comments |
| Calendly | `event.name` | status | — | — | — | — |

## Milestone Mapping

- M1: publish contract markers and stub SDK methods.
- M2: implement subscribe delivery, change-log lookup, `getResourceAtEvent`, and Tier-1 provider `buildSummary()` functions.
- M5: enforce path-scoped subscriptions with scoped tokens.
