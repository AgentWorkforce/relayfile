# LangChain × Notion read (via Relayfile)

LangChain (via `@langchain/langgraph` `createReactAgent`) reads workspace
Notion content via Relayfile. Same bootstrap + smoke as the Vercel AI SDK and
OpenAI Agents siblings; only the agent wrapper differs.

## Run

```bash
npm install
# Smoke (no LLM key)
CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
# Agent
ANTHROPIC_API_KEY=sk-ant-… CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
```

See `vercel-ai-sdk-notion-read/README.md` for the full credential resolution
options and what each smoke check proves.
