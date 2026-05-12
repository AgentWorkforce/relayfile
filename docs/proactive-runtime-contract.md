# Proactive Runtime Contract

This document captures the `relayfile` surfaces the proactive runtime consumes in M2. The TypeScript SDK now exposes a live change stream, retained change lookups, and event-to-resource expansion hooks without renaming the M1 public surface.

The `ChangeEvent` contract below intentionally aligns with the proactive runtime's spec §3.2 `RelayfileChangeEvent` envelope so `relayfile`, `@agent-relay/events`, and the gateway can share one canonical notification shape.

The M2 TypeScript SDK behavior is:

- `subscribe(globs, onChange, options?)` opens a workspace-scoped change stream, multiplexed over one WebSocket transport per `(client, workspace, aclToken)` tuple.
- `open(options)` boots that same transport and optionally primes retained replay data into the local change cache.
- `getResourceAtEvent(eventId)` resolves the canonical payload for a retained event from the local cache first, then falls back to the retained change-log API.
- `listChangesSince(isoTimestamp)` and `listLastNChanges(limit)` return retained `ChangeEvent` envelopes and attach the same `expand()` helper as live events.

## DLQ Path Scheme

The proactive runtime gateway writes final-failure events into the workspace filesystem at:

```text
/_dlq/<workspace>/<event-id>.json
```

This path scheme is the cross-repo contract between the gateway and `relayfile`.

Requirements:

- The path is treated like a normal workspace file path for write, read, list, and delete semantics.
- The gateway owns writes to `/_dlq/**`; agents and tooling may inspect, replay, or purge these files later.
- The workspace path segment mirrors the gateway's write key so DLQ artifacts stay partitioned by workspace inside a shared mount.
- Workspace identity is also duplicated in the JSON payload and surrounding runtime metadata for downstream replay tooling.

## `subscribe(globs[], onChange)` Contract

`relayfile` exposes:

```ts
subscribe(
  globs: string[],
  onChange: (event: ChangeEvent) => void,
  options?: {
    coalesce?: "none" | "fire-once";
    coalesceMs?: number;
    pathScope?: string[];
    aclToken?: string;
    drainMs?: number;
  },
): Subscription
```

M2 behavior:

- The client registers one or more path globs for change notifications.
- Delivery is multiplexed over WebSocket transport, shared per `(client, workspace, aclToken)` tuple.
- Each logical occurrence has a stable `event.id` so downstream runtime dedup can key on `(workspace, agentId, event.id)`.
- The shape aligns with spec §3.2 `RelayfileChangeEvent` so the M2 event SDK can alias it directly.
- `options.aclToken` scopes the underlying watch to that token's visible paths. For M2, any valid workspace token is accepted; narrower path-scoped tokens land with the auth work.
- `options.pathScope` is an additional client-side path filter on top of the token's visibility.
- Rapid writes to the same path collapse into a single callback when coalescing is enabled. The default coalesce window is `200ms`, overridable with `options.coalesceMs`.
- `unsubscribe()` removes the subscription and waits for in-flight handlers to drain, bounded by `options.drainMs` when provided.

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
- `expand("summary")` resolves locally from the delivered envelope.
- `expand("full")` resolves through `getResourceAtEvent(event.id)`.
- `expand("diff")` and `expand("thread")` remain reserved for later milestones and currently raise `M2_NOT_IMPLEMENTED`.
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

The transport is now live in the TypeScript SDK. `open(options)` exists mainly
for callers that want to establish the shared watch transport early and/or seed
the local retained change cache before subscriptions attach.

### `Subscription` shape

The SDK exports:

```ts
type Subscription = {
  unsubscribe(): Promise<void>;
};
```

M2 returns an active subscription handle that unregisters the watch when `unsubscribe()` is called and resolves after in-flight handlers have drained or `options.drainMs` elapses.

## Change-Log Retention

The proactive runtime depends on a replayable change log so the gateway and SDK can resolve `event.id` back to the resource that produced it.

Contract:

- Default retention is at least 7 days on the Growth tier.
- Retention is configurable for higher tiers and self-hosted deployments.
- The retained log must be sufficient to support replay-on-start, `getResourceAtEvent(eventId)`, and future diff expansion.
- The retained log must also back `listChangesSince(iso)` and `listLastNChanges(n)`, which M1 reserves as public SDK names for `replayOnStart: "since:<iso>"` and `replayOnStart: "last:<n>"`.

The SDK assumes the backend retains enough change-log data to satisfy these APIs
for at least the configured retention window. The local SDK cache mirrors the
same contract opportunistically for already-delivered events, but durable
retention remains a backend responsibility.

For the local SDK mirror, `RelayFileClient` accepts:

```ts
new RelayFileClient({
  token,
  changeLog: {
    retentionMs: 7 * 24 * 60 * 60 * 1000,
    maxEntries: 10_000,
  },
});
```

Those settings apply per workspace inside the client process and control the
ring buffer used by live subscriptions, replay priming, and
`getResourceAtEvent(eventId)` cache hits. They do not replace the backend's
durable retained change log.

## Path-Scoped Subscriptions

The proactive runtime will use path-scoped subscriptions for multi-agent ACLs.

Contract:

- Subscription delivery is filtered against the caller's effective path scope before a change event is emitted.
- Paths outside the caller's scope are silently omitted rather than partially redacted.
- The scope model follows workspace and path-scoped token rules introduced in M5.

M2 wires the optional `aclToken` through the watch transport so server-side path
visibility can participate in filtering. Fine-grained path-scoped tokens land
with the M5 auth work.

## Coalesce And Replay Hooks

The proactive runtime reserves two relayfile-facing hooks for post-M1 fan-in behavior:

- `listChangesSince(isoTimestamp)` for replaying retained events from an ISO cursor.
- `listLastNChanges(limit)` for replaying the last N retained events on startup.

These are live M2 SDK methods. They normalize retained envelopes into the same
`ChangeEvent` objects the live stream emits and feed the local event/resource
cache used by `getResourceAtEvent(eventId)`.

Relayfile also needs to preserve a stable `resource.path` on every `ChangeEvent`. Runtime-side coalescing (`options.coalesceMs`) keys on the normalized path plus workspace and therefore depends on that path being durable and consistent across retries.

## `getResourceAtEvent(eventId)` Contract

`relayfile` exposes:

```ts
getResourceAtEvent(eventId: string): Promise<{
  path: string;
  data: unknown;
  digest: string;
}>
```

M2 behavior:

- Resolve `eventId` through the local delivered-event cache first.
- On a cold cache, resolve `eventId` through the retained change log.
- Read the canonical normalized payload stored at the corresponding resource path.
- Return the resource path, normalized payload, and canonical digest.

This is the read path the gateway and SDK call from proactive runtime
`event.expand("full")` for relayfile-backed events.

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
