# OpenAI Agents × Notion read (via Relayfile)

OpenAI Agents SDK reads workspace Notion content via Relayfile —
`listTree` + `readFile` against `/notion` exposed as agent tools.

## Quickstart

```bash
agent-relay cloud login           # writes ~/.agentworkforce/relay/cloud-auth.json
cd examples/integrations/openai-agents-notion-read
npm install
CLOUD_WORKSPACE_ID=<your-app-uuid> npm run smoke
```

Then for the interactive agent:

```bash
OPENAI_API_KEY=sk-… npm run dev
```

For CI / non-interactive: set `CLOUD_API_ACCESS_TOKEN`
(and optionally `CLOUD_API_REFRESH_TOKEN`, `CLOUD_API_URL`) instead of running
`agent-relay cloud login`.

## What this proves

Identical to the Vercel AI SDK sibling: workspace bootstrap → path-scoped
least-privilege read → `listTree` + `readFile` against `/notion` wired into
the framework's tool layer.
