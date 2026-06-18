# Vercel AI SDK × Notion read (via Relayfile)

A runnable Vercel AI SDK agent that reads the workspace's Notion content through Relayfile —
no Notion API client, no OAuth flow in the example, no provider-specific glue. The agent calls
Relayfile tools; Relayfile resolves Notion content from the synced workspace.

## What this proves

| | |
|---|---|
| Bootstrap | Cloud token → `RelayfileSetup.fromCloudTokens` → `joinWorkspace` → `client()` |
| ID handling | App-UUID in, `rw_` ID out — example uses `workspace.workspaceId` for every downstream call (the relayfile#306 plumbing) |
| Least-privilege | Requests `relayfile:fs:read:/notion/**` only — verified preserved in surface map (no scope upgrade) |
| Tool surface | `listTree` + `readFile` against `/notion` via `tools.vercel(rf, { readPaths: ["/notion"] })`. `queryFiles` is intentionally omitted — Notion sync doesn't yet emit semantic metadata (cloud#2275), so a `queryFiles({provider:"notion"})` tool would always return 0 and burn the agent's tool budget. |

## Run

One-time setup:

```bash
agent-relay cloud login            # writes ~/.agentworkforce/relay/cloud-auth.json
export CLOUD_WORKSPACE_ID=<your-app-uuid>   # from https://agentrelay.com/cloud
npm install
```

Then either:

```bash
npm run smoke           # deterministic, no LLM key needed
ANTHROPIC_API_KEY=sk-ant-… npm run dev   # full agent with Vercel AI SDK
```

For CI / non-interactive runs, swap `agent-relay cloud login` for the env
overrides: `CLOUD_API_URL`, `CLOUD_API_ACCESS_TOKEN`, `CLOUD_API_REFRESH_TOKEN`
(optional), `CLOUD_WORKSPACE_ID`.

### Smoke (no LLM, deterministic)

```bash
npm install
npm run smoke
```

Hits Relayfile directly with three checks. Exit 0 = pass. Useful for CI or for "does my
workspace have Notion seeded?" Without an LLM key.

### Agent (needs Anthropic API key)

```bash
export ANTHROPIC_API_KEY=sk-ant-…
npm run dev
```

The agent will explore `/notion`, build a summary, and print tool-call traces.

## Expected smoke output (excerpt)

```
── bootstrap evidence ──
  cloudWorkspaceId : 50587328-441d-4acb-b8f3-dbe1b3c5de99
  workspaceId      : rw_7ccfea89
  credSource       : /Users/you/.agentworkforce/relay/cloud-auth.json

── listTree /notion depth=2 returns ≥1 entry ──
  result   : ✅ PASS
  evidence : { "path": "/notion", "entryCount": 5, "firstThree": [ … ] }

…

── summary ──
  3 / 3 passed
```

## What's intentionally minimal

- **No webhook ingestion.** Notion data is assumed already synced into the workspace. Phase-2
  example projects will demonstrate the inbound webhook path separately.
- **No writeback.** Notion is read-only in Phase 1. The Linear sibling example
  (`vercel-ai-sdk-linear-writeback/`) proves the schema-driven writeback path.

## Surface map

Every SDK call in this example is verified against `docs/integrations/SDK-SURFACE.md`.
If you edit and a call breaks, check the divergence table at Section 0 of that doc first.

## Why this vs. a provider MCP?

Honest answer in [packages/agents/README.md](../../../packages/agents/README.md#when-to-use-this-vs-a-provider-mcp): the MCP wins for one-shot single-provider work; `@relayfile/agents` is structurally better as integrations and agents compound.
