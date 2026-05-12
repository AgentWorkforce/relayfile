# Proactive Runtime Contract

This document captures the `relayfile` surfaces that the proactive runtime expects across milestones. M1 only adds contract markers and typed SDK stubs. Data-trigger delivery, change-log reads, and adapter implementations land in M2+.

## M1 Scope

M1 establishes stable public names so the gateway and `@agent-relay/*` packages can compile against the final surface now and replace the stubbed implementations later without renaming APIs.

The `ChangeEvent` contract below is intentionally aligned with the proactive runtime's spec §3.2 `RelayfileChangeEvent` envelope so M2 can swap the local definition for a one-line type alias instead of a breaking rename.

The TypeScript SDK exposes the required M1 stubs and reserves the replay helper names that the gateway will consume later:

- `subscribe(globs, onChange)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.
- `getResourceAtEvent(eventId)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.
- `listChangesSince(isoTimestamp)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.
- `listLastNChanges(limit)` exists in the public client and throws `M2_NOT_IMPLEMENTED`.

## DLQ Path Scheme

The proactive runtime gateway writes final-failure events into the workspace filesystem at:

```text
/_dlq/<event-id>.json
```

This path scheme is the cross-repo contract between the gateway and `relayfile`.

Requirements:

- The path is treated like a normal workspace file path for write, read, list, and delete semantics.
- The gateway owns writes to `/_dlq/**`; agents and tooling may inspect, replay, or purge these files later.
- Workspace identity lives in the JSON payload and surrounding runtime metadata rather than in an extra path segment.

## `subscribe(globs[], onChange)` Contract

`relayfile` will expose:

```ts
subscribe(
  globs: string[],
  onChange: (event: ChangeEvent) => void,
  options?: {
    coalesce?: "none" | "fire-once";
    pathScope?: string[];
    drainMs?: number;
  },
): Subscription
```

M1 behavior:

- The method is present on the public SDK.
- The method throws `M2_NOT_IMPLEMENTED`.

M2 behavior:

- The client registers one or more path globs for change notifications.
- Delivery is multiplexed over WebSocket transport.
- Each logical occurrence has a stable `event.id` so downstream runtime dedup can key on `(workspace, agentId, event.id)`.
- The shape aligns with spec §3.2 `RelayfileChangeEvent` so the M2 event SDK can alias it directly.
- The optional `options` bag reserves the coalesce hint, future path-scoped subscriptions, and drain semantics without forcing a later method rename.

### `ChangeEvent` shape

The `ChangeEvent` contract is notification-first. It is not the raw provider payload and it is not the older filesystem event feed shape.

```ts
type ChangeEvent = {
  id: string;
  workspace: string;
  type: "relayfile.changed";
  occurredAt: string;
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
  expand: <L extends ExpansionLevel = "summary">(level?: L) => Promise<Expansion<L>>;
  digest?: string;
};
```

Notes:

- `id` must be stable across retries and redelivery for the same logical change. The canonical format is `<provider>:<workspace>:<resource-id>:<occurrence-id>`, where `occurrence-id` is the provider event id or a deterministic digest of the normalized change when no upstream id exists.
- `resource.path` points at the canonical normalized payload inside relayfile.
- `summary` stays small and routing-friendly; large payload fields remain in VFS until explicitly fetched.
- `digest` is the hash of the current canonical resource state when available.
- `expand("full")` resolves through `getResourceAtEvent(event.id)` in M2. M1 publishes the type shape and stub so downstream packages can compile now.
- `agentId` is optional on the notification envelope and carries the producer-side agent identity when the source runtime can preserve it.
- Retry `attempt` is owned by the proactive-runtime gateway, not relayfile. Relayfile emits stable logical change events; the gateway wraps those events with delivery-attempt metadata when it redelivers them.

### Transport entry point

M1 also reserves the transport bootstrap surface:

```ts
type ReplayOptions =
  | { replayOnStart?: "none" }
  | { replayOnStart: `since:${string}` }
  | { replayOnStart: `last:${number}` };

type ChangeStreamConnectionOptions = ReplayOptions & {
  workspaceId: string;
};

open(options: ChangeStreamConnectionOptions): ChangeStreamConnection
```

The M1 SDK method throws `M2_NOT_IMPLEMENTED`, but its presence prevents
downstream packages from inventing incompatible transport-bootstrap shapes
before M2 lands.

### `Subscription` shape

The SDK exports:

```ts
type Subscription = {
  unsubscribe(): Promise<void>;
};
```

M2 will return an active subscription handle that unregisters the watch when `unsubscribe()` is called and resolves after in-flight handlers have drained or `options.drainMs` elapses.

## Change-Log Retention

The proactive runtime depends on a replayable change log so the gateway and SDK can resolve `event.id` back to the resource that produced it.

Contract:

- Default retention is at least 7 days on the Growth tier.
- Retention is configurable for higher tiers and self-hosted deployments.
- The retained log must be sufficient to support replay-on-start, `getResourceAtEvent(eventId)`, and future diff expansion.
- The retained log must also back `listChangesSince(iso)` and `listLastNChanges(n)`, which M1 reserves as public SDK names for `replayOnStart: "since:<iso>"` and `replayOnStart: "last:<n>"`.

M1 only documents this requirement. Storage implementation lands in M2.

## Path-Scoped Subscriptions

The proactive runtime will use path-scoped subscriptions for multi-agent ACLs.

Contract:

- Subscription delivery is filtered against the caller's effective path scope before a change event is emitted.
- Paths outside the caller's scope are silently omitted rather than partially redacted.
- The scope model follows workspace and path-scoped token rules introduced in M5.

M1 only documents the contract. Enforcement lands with the M5 auth work.

## Coalesce And Replay Hooks

The proactive runtime reserves two relayfile-facing hooks for post-M1 fan-in behavior:

- `listChangesSince(isoTimestamp)` for replaying retained events from an ISO cursor.
- `listLastNChanges(limit)` for replaying the last N retained events on startup.

These are M1 SDK stubs that throw `M2_NOT_IMPLEMENTED`. The names are locked now so the gateway and runtime can compile against the final replay contract without a later rename.

Relayfile also needs to preserve a stable `resource.path` on every `ChangeEvent`. Runtime-side coalescing (`options.coalesceMs`) keys on the normalized path plus workspace and therefore depends on that path being durable and consistent across retries.

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

This is the read path the gateway and SDK call from proactive runtime `event.expand("full")` for relayfile-backed events. M1 keeps the type signature stable now so M2 can replace the stub with the real change-log lookup in one line.

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

The SDK exports `EventSummary` as the canonical type alias for
`ChangeEventSummary` so adapter packages can import the shape instead of
re-declaring it.

### Summary Builder Registration

The proactive runtime gateway exposes a shared registration seam for provider builders:

```ts
registerSummaryBuilder(provider: string, builder: SummaryBuilder): void
```

Registration contract:

- The gateway boot path installs builtin builders for `internal` and `relayfile`.
- Provider packages register their own `buildSummary()` implementation during startup before change events are processed.
- Registration is keyed by the top-level `resource.provider` namespace carried on the event envelope.
- Builders may return partial summaries; the gateway applies the canonical sanitization pass afterward.

This keeps the runtime-side event envelope stable while allowing provider adapters to evolve their own summary extraction logic behind a single hook.

### Tier-1 provider summary fields

M1 only locks the contract and the Tier-1 field-mapping expectations. Tier-1 implementations land in M2 for:

- Linear
- GitHub
- Slack
- Notion
- Jira

Tier-2 providers remain out of scope for M1 and will follow after the Tier-1 runtime path is wired.

| Provider | title | status | priority | labels | fieldsChanged | thread |
|---|---|---|---|---|---|---|
| Linear | `issue.title` | `state.name` | priority label | `labels[].name` | diff vs `previousData` | comments |
| GitHub | `pull_request.title` or `issue.title` | `state` | — | `labels[].name` | from changes object | review comments + comments |
| Slack | first 80 chars of text | — | — | — | — | thread replies |
| Notion | page title or database title | — | — | tags | property changes | comments |
| Jira | `summary` | `status.name` | `priority.name` | `labels` | from changelog | comments |

## Milestone Mapping

- M1: publish contract markers and stub SDK methods.
- M2: implement subscribe delivery, change-log lookup, `getResourceAtEvent`, and Tier-1 provider `buildSummary()` functions.
- M5: enforce path-scoped subscriptions with scoped tokens.
