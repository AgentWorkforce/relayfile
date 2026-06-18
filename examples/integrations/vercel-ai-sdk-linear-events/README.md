# Vercel AI SDK × Linear *events* — reactive agent

The pattern per-provider MCPs structurally cannot do: **the agent reacts to a
Linear webhook in real time.** No polling, no `getEvents` loop, no agent-initiated
call. Linear pushes → Cloud syncs → Relayfile emits a file event → your agent
wakes and acts.

```ts
import { connect, tools, type ChangeEvent } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/linear/**"] });

rf.onEvent(["/linear/**"], async (event: ChangeEvent) => {
  const file = await rf.read(event.resource.path);
  // hand to your agent loop:
  const { text } = await generateText({
    model: anthropic("claude-sonnet-4-6"),
    tools: tools.vercel(rf, { readPaths: ["/linear"] }),
    prompt: `Something changed at ${event.resource.path}: ${file.content.slice(0, 4000)}`,
  });
  console.log(text);
});
```

That's the entire wiring. Multiple agents can subscribe to the same workspace;
each gets the event independently — the substrate fans out to all of them.

## Quickstart

```bash
agent-relay cloud login
cd examples/integrations/vercel-ai-sdk-linear-events
npm install
CLOUD_WORKSPACE_ID=<your-app-uuid> npm run smoke
```

Then for the long-lived reactive agent:

```bash
ANTHROPIC_API_KEY=sk-ant-… npm run dev
```

Now **edit a Linear issue or label in the Linear UI** — your agent reacts in
seconds, no polling, no manual trigger.

## What the smoke proves

| | |
|---|---|
| Bootstrap | `connect()` → workspace identity |
| Subscription | `rf.onEvent(["/linear/labels/**"], handler)` |
| Trigger | `rf.writeback.create("/linear/labels", payload)` writes a draft and creates a real Linear label |
| Delivery | Handler fires within the wait window (default 30s) — proves the event bus is live |
| Cleanup | Canonical + draft both removed before exit |

The smoke uses a self-trigger (writeback create) so it works without any
external Linear webhook setup. In production you'd let Linear's own webhook
fire the events — same handler, no smoke seam.

## Why this isn't just a polling example

> "We should not show polling examples because everyone can do that."

Exactly. `examples/04-realtime-events/` already shows the raw-SDK `getEvents`
polling path — that's the lowest-common-denominator approach you'd write
yourself. This example uses the SDK's push channel (`subscribe` → WebSocket)
through the `@relayfile/agents` thin `onEvent` wrapper, so the agent code is
just three lines and there's no polling loop in the example at all.

## Test coverage honesty

The smoke proves event **delivery** end-to-end — a writeback create fires a
real `file.created` event delivered over the WebSocket within seconds. The
**reconnect-on-close + token-refresh + exponential-backoff** path is
correct-by-inspection but isn't deterministically exercisable in a smoke
(the WS token TTL is ~24h, far longer than a smoke run). The exponential
backoff (`baseDelayMs: 1000` → `maxDelayMs: 30000`, reset to base after
~10s of stable connection) is the safety belt for that untested path:
sustained gateway flap won't hammer the connection.

## See also

- [`packages/agents/README.md`](../../../packages/agents/README.md) — package overview, including "When to use vs. a provider MCP" and the event-driven structural advantage.
- The other six examples (`vercel-ai-sdk-*`, `openai-agents-*`, `langchain-*`) cover the read + writeback halves of the loop; this one closes it with reactive event handling.
