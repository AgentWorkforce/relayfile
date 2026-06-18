# OpenAI Agents × Linear writeback (via Relayfile)

OpenAI Agents SDK equivalent of `vercel-ai-sdk-linear-writeback/`. Same Relayfile
bootstrap, same schema discipline, same comprehensive op-status proof
(`writeback.provider=linear` → op `succeeded` → real Linear UUID in
`providerResult.externalId`), same draft-receipt cleanup. Only the agent
wrapper differs.

See `vercel-ai-sdk-linear-writeback/README.md` for the full contract details
(draft semantics, why we use the externalId path, what the smoke proves,
filed adapter-doc issue link).

## Run

```bash
# Smoke (no LLM key, comprehensive provider-side proof)
npm install
CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke

# Agent (needs OpenAI key)
OPENAI_API_KEY=sk-… CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
```
