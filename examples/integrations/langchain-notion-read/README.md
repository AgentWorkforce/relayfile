# LangChain × Notion read (via Relayfile)

LangChain (via `@langchain/langgraph` `createReactAgent`) reads workspace
Notion content via Relayfile — `listTree` + `readFile` wired as LangChain
tools.

## Quickstart

```bash
agent-relay cloud login
cd examples/integrations/langchain-notion-read
npm install
CLOUD_WORKSPACE_ID=<your-app-uuid> npm run smoke
```

Then for the interactive agent:

```bash
ANTHROPIC_API_KEY=sk-ant-… npm run dev
```

For CI: set `CLOUD_API_ACCESS_TOKEN` (and optionally `CLOUD_API_REFRESH_TOKEN`,
`CLOUD_API_URL`).

## Why this vs. a provider MCP?

Honest answer in [packages/agents/README.md](../../../packages/agents/README.md#when-to-use-this-vs-a-provider-mcp): the MCP wins for one-shot single-provider work; `@relayfile/agents` is structurally better as integrations and agents compound.
