# onWrite Trigger SDK

**Status:** Proposed
**Affects:** `packages/sdk/typescript`, `packages/sdk/python`, `packages/core` (types)
**Depends on:** existing WebSocket event stream (`RelayFileClient.connectWebSocket`), file-observer event feed
**Pairs with:** `sdk-setup-client.md`, `relayfile-v1-spec.md`

---

## Problem

Today, a customer who wants to react to a relayfile change must:
1. Open a `RelayFileClient.connectWebSocket()` connection,
2. Subscribe to the raw event stream,
3. Filter events by path manually,
4. Handle reconnection, backoff, and dispatch errors themselves.

This is several dozen lines of glue code per consumer, and there is no canonical surface — every team writes their own wrapper. The customer-facing pitch ("trigger an agent when a file changes") doesn't have a one-liner SDK call.

It also means the most differentiated half of the Relayfile pitch — *event-driven, not just synced* — has no developer surface. Composio is reactive (no triggers). Pipedream has triggers but only via GUI. Nango has webhooks but you still write the handler service. The thing that puts Relayfile architecturally above all three is a customer-facing primitive that doesn't currently exist as one.

---

## Goal

A developer can subscribe to changes on a path pattern in two lines of code, with no infrastructure to host, no webhook URL to register, no reconnection logic to write.

```ts
import { onWrite } from '@relayfile/sdk'

onWrite('/notion/pages/calls/*/transcript', async (event) => {
  const callId = event.path.split('/')[4]
  await spawnFollowupAgents(callId)
})
```

The same shape in Python:

```python
from relayfile import on_write

@on_write('/notion/pages/calls/*/transcript')
async def handle(event):
    call_id = event.path.split('/')[4]
    await spawn_followup_agents(call_id)
```

That snippet — and only that snippet — is the public surface for the v1.

---

## Non-Goals (v1)

- **Outbound webhook subscription registry on the server.** v1 uses the existing WebSocket event stream and assumes a long-running consumer process. Serverless / Lambda-shaped consumers are a v2 concern.
- **Replay / dead-letter queue.** v1 logs handler errors via `client.recordHandlerError`; durable replay is v2.
- **Cross-workspace subscriptions.** v1 scopes to one workspace per `onWrite` registration (the workspace bound to the SDK client).
- **Event deduplication / exactly-once semantics.** v1 is at-least-once. Handler should be idempotent.
- **GUI for subscription management.** v1 is code-only. file-observer can list active WS subscriptions but not configure them.

---

## API surface

### TypeScript

```ts
// packages/sdk/typescript/src/onWrite.ts

import type { RelayFileClient } from './client'
import type { WriteEvent } from '@relayfile/core'

export type OnWriteHandler = (event: WriteEvent) => void | Promise<void>

export interface OnWriteOptions {
  /** Client to subscribe through. Defaults to the module-level singleton. */
  client?: RelayFileClient
  /** Operations to subscribe to. Defaults to ['create', 'update']. */
  operations?: Array<WriteEvent['operation']>
  /** AbortSignal to stop the subscription. */
  signal?: AbortSignal
}

/**
 * Subscribe to write events on a path pattern. Returns an unsubscribe function.
 *
 * Pattern syntax (glob-like, single-segment wildcards only):
 *   /notion/pages/calls/* /transcript     (* matches one path segment)
 *   /linear/issues/**                     (** matches zero or more trailing segments — including the collection root)
 *   /github/repos/acme/api/pulls/*        (* matches one segment)
 *
 * Multiple wildcards are supported. No regex.
 */
export function onWrite(
  pattern: string,
  handler: OnWriteHandler,
  options?: OnWriteOptions,
): () => void
```

Behavior contract:
- **Returns synchronously** with an unsubscribe function. The actual WS connection is established lazily on the first registration if the client doesn't already have one.
- **Connection is shared** across all `onWrite` calls on the same client — one socket, many registrations. The dispatcher fans events out to matching handlers in registration order.
- **Handler errors do not crash** the dispatcher. They are caught, logged via `client.recordHandlerError({ pattern, path, error })`, and the next handler runs.
- **Backpressure**: handlers run sequentially per registration (no per-handler queueing in v1). If a handler is slow, subsequent events for that pattern queue in memory. v2 adds a configurable concurrency limit and bounded queue.
- **Reconnection**: on WS disconnect, the SDK reconnects with exponential backoff (capped at 30s). On reconnect, the SDK resubscribes; missed events during the gap are *not* replayed in v1 (the server's existing replay-on-resume is the upper bound). Document this clearly.
- **Filtering**: pattern matching happens client-side in v1. v2 will push the pattern to the server so the WS only receives matching events.

### Python

Decorator + non-decorator forms:

```python
# packages/sdk/python/relayfile/on_write.py
from typing import Callable, Awaitable, Iterable, Optional
from .types import WriteEvent
from .client import RelayFileClient

OnWriteHandler = Callable[[WriteEvent], Awaitable[None]]

def on_write(
    pattern: str,
    handler: Optional[OnWriteHandler] = None,
    *,
    client: Optional[RelayFileClient] = None,
    operations: Iterable[str] = ('create', 'update'),
):
    """Subscribe to write events on a path pattern.

    Usable as decorator or function:

        @on_write('/notion/pages/calls/*/transcript')
        async def handle(event): ...

        on_write('/linear/issues/*', handle)
    """
    ...
```

Implementation mirrors the TS version: shared WS, lazy connection, client-side glob matching, error isolation, reconnect with backoff.

---

## Path-pattern matching

Single grammar, both languages:

| Token | Matches |
|---|---|
| Literal segment (`calls`) | Exactly that segment |
| `*` | Exactly one path segment, any characters |
| `**` | Zero or more path segments (greedy) |
| Trailing `/` | Optional in pattern; ignored in path |

**Examples:**

| Pattern | Path | Match? |
|---|---|---|
| `/notion/pages/calls/*/transcript` | `/notion/pages/calls/2026-05-08/transcript` | ✅ |
| `/notion/pages/calls/*/transcript` | `/notion/pages/calls/2026-05-08/notes/transcript` | ❌ (`*` is single-segment) |
| `/linear/issues/**` | `/linear/issues/PROJ-441/comments/c-1` | ✅ |
| `/linear/issues/**` | `/linear/issues` | ✅ (`**` includes the collection root) |
| `/github/repos/*/*/pulls/*` | `/github/repos/acme/api/pulls/42` | ✅ |
| `/hubspot/deals/*/stage` | `/hubspot/deals/4471/stage` | ✅ |

`**` is convenient but expensive for client-side filtering — document that v2 server-side filtering will short-circuit `**` patterns more efficiently.

No regex. No alternation (`{a,b}`). No character classes. If a customer needs them, they filter inside the handler.

Reference impl: a small recursive matcher (~30 LOC) that walks segments. Library options (`micromatch`, `picomatch`, Python `fnmatch`) are tempting but bring weight; this rule set is simple enough to own.

---

## Event payload

Add to `@relayfile/core` (or wherever `WriteEvent` lives today — confirm against existing WS message schema and reuse):

```ts
export interface WriteEvent {
  /** Workspace the change happened in. */
  workspaceId: string
  /** Canonical VFS path. */
  path: string
  /** What happened. */
  operation: 'create' | 'update' | 'delete'
  /** New revision id. */
  revision: string
  /** Previous revision id (null on create). */
  previousRevision: string | null
  /** ISO 8601 timestamp. */
  timestamp: string
  /** Where the write originated. */
  source: 'webhook' | 'agent' | 'sync' | 'api' | 'cli'
  /**
   * Parsed contents for json/yaml files when small enough (<256 KB).
   * For larger files or non-parseable formats, undefined — handler must
   * `client.readFile(path)` to fetch.
   */
  value?: unknown
  /** Identity that performed the write (agent name, user id, sync provider). */
  actor?: { type: 'agent' | 'user' | 'system'; id: string }
}
```

Reusing the existing internal event schema (whatever WS emits today) is preferred — only add fields that aren't already there. Audit `packages/file-observer` and `packages/core` first.

---

## Error model

Three failure modes, each with explicit handling:

1. **Handler throws.** Caught by the dispatcher. Recorded via `client.recordHandlerError({ pattern, path, error, retryable: false })`. Other handlers for the same event still run. The handler is *not* automatically retried (idempotency is the customer's responsibility).

2. **WS disconnect.** Reconnect with exponential backoff (1s → 2s → 4s → 8s → 16s → 30s, then steady at 30s). Emit a `client.on('reconnecting', ...)` event so file-observer-style consumers can show connection state. Resubscribe on connect.

3. **Pattern is invalid.** Throw synchronously at registration time, not on first event. Validate up-front (no `**` in middle of pattern unless trailing, no empty segments, no leading non-`/`).

---

## Concurrency

v1 — sequential per pattern, parallel across patterns. If two handlers are registered on the same pattern they run in registration order, sequentially.

```ts
onWrite('/x/*', a)   // runs first
onWrite('/x/*', b)   // runs after a completes for the same event
onWrite('/y/*', c)   // runs in parallel with the /x/* chain
```

Rationale: most customers register one handler per pattern; sequential within a pattern preserves observable order; cross-pattern parallelism gives concurrency where it's safe.

v2 may add `{ concurrency: number }` per registration.

---

## Observability

Each registered handler is reported up to file-observer (or any future dashboard) via the existing client telemetry channel:

- `subscription.registered` — pattern, registered-at
- `subscription.event-dispatched` — pattern, path, latency-from-write
- `subscription.handler-error` — pattern, path, error message, error type
- `subscription.reconnect` — gap duration, missed-event count (if knowable)

These should reuse whatever telemetry the WebSocket layer already emits — don't add a new channel if the existing event feed can carry them.

---

## Implementation plan

### Phase 1 — TypeScript SDK (~2 days)

1. Confirm the existing `WriteEvent` shape on the WS — reuse, don't reinvent.
2. Implement `pathMatches(pattern, path)` with the grammar above. Unit tests covering the table in the §Path-pattern matching section.
3. Implement `onWrite()` with lazy connection, shared dispatcher, reconnect, error isolation.
4. Add `client.recordHandlerError()` if it doesn't exist (likely just a logger call).
5. Tests:
   - Glob matching unit tests
   - Dispatcher fan-out (multiple registrations, multiple events)
   - Reconnect behavior (with mock WS)
   - Handler error doesn't break dispatcher
6. Export from `@relayfile/sdk` index.
7. Add a 30-line example to `packages/sdk/typescript/examples/onWrite.ts`.

### Phase 2 — Python SDK (~1 day)

Mirror Phase 1. Reuse the same matcher rules (port the implementation; same test cases).

### Phase 3 — Docs + examples (~0.5 day)

- Update `docs/sdk-improvements.md` with `onWrite` entry.
- Add a recipe to `docs/guides/` — "Trigger an agent when a customer call lands in Notion."
- Update the homepage hello-world to use `onWrite` instead of polling.

### Phase 4 (post-v1) — server-side outbound webhooks

For serverless consumers. Design separately. Out of scope here.

---

## Acceptance

A consumer can paste this into a Node script, run it, and receive events:

```ts
import { RelayFileClient, onWrite } from '@relayfile/sdk'

const client = new RelayFileClient({ token: process.env.RELAYFILE_TOKEN! })

onWrite('/notion/pages/calls/*/transcript', async (event) => {
  console.log('call transcript saved:', event.path, 'rev', event.revision)
}, { client })

// Process stays alive on the WS subscription.
```

Manually save a Notion page that syncs to that path; the handler logs the event within ~2s of the WS receive (no retry storms, no missed reconnects after a `kill -STOP`/`kill -CONT` cycle, no crash on a thrown handler).

---

## Open questions

1. **Reuse existing WS event types or define new ones?** Audit `packages/core` and `packages/sdk/typescript` for the current shape. If the WS already emits a structured `FileEvent`, alias `WriteEvent` to it (or extend). Don't proliferate types.
2. **Should `delete` be in default operations?** Default I propose is `['create', 'update']`. Delete-as-trigger has different semantics (file no longer readable from the handler) and is rarer. Make it opt-in.
3. **Bound or unbounded handler queue per pattern?** v1 unbounded with a documented memory caveat. v2 add a configurable cap. Decide before merge.
4. **`onWrite` vs `onChange`?** "write" matches the operation taxonomy in the WS schema today; "change" is friendlier for non-engineer readers. Pick one and stick. Recommendation: `onWrite` (matches `relayfile:fs:write:*` scopes in relayauth — symmetry).
5. **Where does the example live?** `packages/sdk/typescript/examples/` exists for `sdk-setup-client.md`. Same place.

---

## Risk

The biggest risk is that this becomes the most-demoed primitive in the SDK and we under-spec the failure modes. Customers will copy the two-line example and put it in a Lambda where it can't hold a WS open, and then complain. The mitigation is two-fold: (1) Phase 4 (outbound webhooks) needs to be on the public roadmap from day one of `onWrite` shipping, and (2) the docs lead with "this is for long-running consumers; for serverless, use [forthcoming webhook subscriptions]."

A secondary risk: if `**` patterns are common, client-side filtering of high-volume workspaces (millions of events/day) will saturate the WS bandwidth. Track WS bytes-received per consumer in file-observer telemetry; if any consumer exceeds X MB/min, recommend they tighten their pattern or wait for v2 server-side filtering.
