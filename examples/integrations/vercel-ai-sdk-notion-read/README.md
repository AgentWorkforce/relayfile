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
| Tool surface | `listTree` / `readFile` / `queryFiles` against `/notion` via the Vercel AI SDK `tool()` wrapper |

## Run

You need:

- **A Relayfile workspace with Notion connected.** This workspace's app-UUID goes in `CLOUD_WORKSPACE_ID`.
- **Cloud credentials**, either as env vars (CI-safe) or from a local file written by `agent-relay cloud login`.

### Option A — pre-minted relayfile token (most CI-clean, no cloud hop)

```bash
export RELAYFILE_BASE_URL="https://api.relayfile.dev"
export RELAYFILE_WORKSPACE_ID="rw_…"
export RELAYFILE_TOKEN="ey…"      # JWT with relayfile:fs:read:/notion/** scope
```

Skips the cloud control plane entirely. Useful when a token has already been
minted by some other process (e.g. CI).

### Option B — cloud control plane (env)

```bash
export CLOUD_API_URL="https://agentrelay.com/cloud"
export CLOUD_API_ACCESS_TOKEN="cld_at_…"
export CLOUD_API_REFRESH_TOKEN="cld_rt_…"   # optional but recommended
export CLOUD_WORKSPACE_ID="<your-app-uuid>"
```

If you omit `CLOUD_API_REFRESH_TOKEN`, the bootstrap pins the access token
as far-future so the SDK uses it as-is without trying to roll it.

### Option C — local cred file (operator convenience)

Run `agent-relay cloud login` once. The example will discover credentials at
`~/.agentworkforce/relay/cloud-auth.json` (or `~/.cloud/credentials.json` /
`~/.relayfile/cloud-credentials.json` as fallbacks). Set just
`CLOUD_WORKSPACE_ID`. If the cred file is stale the bootstrap warns and the
SDK refreshes automatically via the stored refresh token.

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
