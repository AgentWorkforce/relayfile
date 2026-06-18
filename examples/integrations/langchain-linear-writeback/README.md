# LangChain × Linear writeback (via Relayfile)

LangChain (via `@langchain/langgraph` `createReactAgent`) creates / updates /
deletes Linear labels through Relayfile. Same Relayfile bootstrap, schema
discipline, and comprehensive op-status proof as the Vercel AI SDK and OpenAI
Agents siblings. See `vercel-ai-sdk-linear-writeback/README.md` for the full
contract details (draft semantics, why externalId pathing, adapter-doc issue).

## Run

```bash
npm install
# Smoke (no LLM key — comprehensive provider-side proof)
CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
# Agent
ANTHROPIC_API_KEY=sk-ant-… CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
```
