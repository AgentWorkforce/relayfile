# `@relayfile/agents`

Thin framework adapters for [Relayfile](https://relayfile.dev). One `connect()` call gives
your agent path-scoped read tools, a proven writeback lifecycle, and SDK escape hatches —
working identically against the Vercel AI SDK, OpenAI Agents SDK, and LangChain.

```ts
import { connect, tools } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/notion/**"] });

// Vercel AI SDK:
const { text } = await generateText({
  model: anthropic("claude-sonnet-4-6"),
  tools: tools.vercel(rf, { readPaths: ["/notion"] }),
  prompt: "Summarise the Notion workspace.",
});

// OpenAI Agents:
const agent = new Agent({ name: "x", tools: tools.openai(rf, { readPaths: ["/notion"] }), ... });

// LangChain:
const agent = createReactAgent({ tools: tools.langchain(rf, { readPaths: ["/notion"] }), ... });
```

## When to use this vs. a provider MCP

Honest answer up front:

- **1 provider × 1 agent × one-shot action → use the provider MCP.** A dedicated Linear or
  Notion MCP is simpler, more direct, and can model provider-specific operations richer than
  our generic schema-validate-and-write does. `@relayfile/agents` adds a Relayfile dependency
  for no real gain in that case.
- **N providers and/or N agents → Relayfile is the substrate, and the gap widens with each.**
  It's a different category of tool — a shared workspace, not a per-provider client. The two
  are complementary, not competing: a real agent harness can use both, Relayfile for the
  stateful multi-provider substrate and an MCP for typed one-shot actions where that fits.

### Where the value compounds

| Dimension | Per-provider MCPs | `@relayfile/agents` |
|---|---|---|
| Tool surface as you add providers | **O(N) tool schemas** the agent must learn and juggle | **O(1) interface** — `listTree` / `readFile` / `writeback.*` work identically across every integration |
| Cross-provider composition | Bespoke glue per pair of providers | Free: `grep -r`, `cp`, `cat` semantics across `/linear`, `/notion`, `/github`, … |
| Auth setup | N per-provider OAuth dances per machine | One `agent-relay cloud login` per machine; per-app permissions live in Cloud |
| Multi-agent coordination | Point-to-point, stateless, pull-only | Shared, ACL'd, versioned filesystem; agents see each other's writes in real time |
| Concurrency safety | None — agents clobber | `baseRevision` + `RevisionConflictError` ([example](../../examples/integrations/vercel-ai-sdk-linear-writeback/src/smoke.ts)) |
| Least-privilege per agent | Per-MCP-server auth, not workspace-scoped | Path-scoped scopes: agent A `relayfile:fs:read:/notion/**`, agent B `relayfile:fs:write:/linear/labels/**` |
| Event-driven loops | Request/response only | Webhook → file event → many agents subscribe via WebSocket |
| Provider rate limits / retry / dead-letter | Every agent races the provider | Centralized in Relayfile's writeback queue + op-status |
| Workspace state | Ephemeral per session | Durable — a new agent joining mid-flight is oriented by `_index.json` / digests |

### The webhook / `onEvent` axis — where this really pays off

The single biggest structural difference vs. per-provider MCPs is that Relayfile is
**event-driven, not request/response.** Every provider webhook becomes a workspace file event
that any number of agents can subscribe to in real time:

```
Linear webhook → Cloud sync → /linear/issues/X.json revision++ → WebSocket event
                                                                      ↓
                                                       agent A reacts, writes a comment
                                                       agent B reacts, files a Slack ping
                                                       agent C reacts, opens a PR
```

This is the substrate that makes **proactive agents** possible — agents that aren't waiting for
a user prompt but reacting to provider state changes (a new Linear issue, a Notion page edit,
a GitHub PR opened). MCPs have no concept of this: they're pull-only.

The SDK exposes `connectWebSocket({ onEvent })` and a glob-based `subscribe()` API for
filtering. See `examples/04-realtime-events/` for the raw-SDK pattern. A framework-wrapped
"agent reacts to webhook" example is on the Phase-2 roadmap — the wiring is identical to the
read/write examples in this PR plus a long-lived event loop.

### Operational deploy story

In a deployed agent fleet you don't want every container running N OAuth dances per provider.
Cloud centralizes that; the fleet just needs one workspace token. New providers come online
without redeploying agents.

## API

```ts
const rf = await connect({ workspaceId?, scopes?, agentName? });
// → { client, workspaceId, cloudWorkspaceId, credSource, writeback }

rf.client                      // raw RelayFileClient escape hatch
rf.workspaceId                 // the rw_ id from /join — use for any direct SDK calls
rf.writeback.create(root, payload, opts?)
rf.writeback.readCanonical(canonicalPath, opts?)
rf.writeback.update(canonicalPath, baseRevision, patch, opts?)
rf.writeback.delete(canonicalPath, baseRevision, opts?)
rf.writeback.deleteDraft(draftPath)     // local-only Relayfile cleanup

tools.vercel(rf, { readPaths? })
tools.openai(rf, { readPaths? })
tools.langchain(rf, { readPaths? })
```

`@relayfile/agents` also re-exports `RelayFileApiError`, `RevisionConflictError`, and the SDK
type surface — examples import everything from this package and never reach into `@relayfile/sdk`
directly, which keeps the dual-package hazard from coming back.

## Credentials

Two sources, in this order:

1. **Env overrides** (CI / non-interactive): `CLOUD_API_URL`, `CLOUD_API_ACCESS_TOKEN`,
   `CLOUD_API_REFRESH_TOKEN` (optional), `CLOUD_WORKSPACE_ID`.
2. **`~/.agentworkforce/relay/cloud-auth.json`** — written by `agent-relay cloud login`.

That's it. No legacy probing.

## See also

Six runnable example projects in [`examples/integrations/`](../../examples/integrations/) —
Vercel AI SDK / OpenAI Agents / LangChain × Notion read + Linear writeback. Each is
self-contained (`npm install && npm run smoke`) and proves the full lifecycle against
production Relayfile.
