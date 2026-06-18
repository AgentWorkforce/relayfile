# LangChain × Linear writeback (via Relayfile)

LangChain (via `@langchain/langgraph` `createReactAgent`) creates / updates /
deletes Linear labels through Relayfile. Schema-validated, with comprehensive
op-status proof (`writeback.provider=linear` → op `succeeded` → real Linear
UUID in `providerResult.externalId`).

## Quickstart

```bash
agent-relay cloud login
cd examples/integrations/langchain-linear-writeback
npm install
CLOUD_WORKSPACE_ID=<your-app-uuid> npm run smoke
```

Then for the interactive agent:

```bash
ANTHROPIC_API_KEY=sk-ant-… npm run dev
```

For CI: set `CLOUD_API_ACCESS_TOKEN` (and optionally `CLOUD_API_REFRESH_TOKEN`,
`CLOUD_API_URL`).

## Important

Every smoke run creates ONE real Linear label and deletes it before exit.
Test labels are prefixed `relayfile-writeback-test`. See
`vercel-ai-sdk-linear-writeback/README.md` for the full contract details.

## Why this vs. a provider MCP?

Honest answer in [packages/agents/README.md](../../../packages/agents/README.md#when-to-use-this-vs-a-provider-mcp): the MCP wins for one-shot single-provider work; `@relayfile/agents` is structurally better as integrations and agents compound.
