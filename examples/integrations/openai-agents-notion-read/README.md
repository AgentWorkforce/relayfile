# OpenAI Agents × Notion read (via Relayfile)

OpenAI Agents SDK equivalent of `vercel-ai-sdk-notion-read/`. Same Relayfile
bootstrap, same tool surface, same smoke verifier — only the agent-framework
wrapper differs.

## What this proves

Same as the Vercel AI SDK sibling: workspace bootstrap → least-privilege
path-scoped read → `listTree` + `readFile` against `/notion` exposed as
agent tools.

## Run

Same credential resolution as every other integration example — env override,
or `agent-relay cloud login` for the local cred file. See
`vercel-ai-sdk-notion-read/README.md` for the full breakdown.

```bash
# Smoke (no LLM key needed)
npm install
CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke

# Agent (needs OpenAI key)
OPENAI_API_KEY=sk-… CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
```
