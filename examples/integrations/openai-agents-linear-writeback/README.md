# OpenAI Agents × Linear writeback (via Relayfile)

OpenAI Agents SDK creates / updates / deletes Linear labels through
Relayfile — schema-validated, with comprehensive op-status proof
(`writeback.provider=linear` → op `succeeded` → real Linear UUID in
`providerResult.externalId`).

## Quickstart

```bash
agent-relay cloud login
cd examples/integrations/openai-agents-linear-writeback
npm install
CLOUD_WORKSPACE_ID=<your-app-uuid> npm run smoke
```

Then for the interactive agent:

```bash
OPENAI_API_KEY=sk-… npm run dev
```

For CI: set `CLOUD_API_ACCESS_TOKEN` (and optionally `CLOUD_API_REFRESH_TOKEN`,
`CLOUD_API_URL`).

## Important

Every smoke run creates ONE real Linear label and deletes it. All test labels
are prefixed `relayfile-writeback-test` so you can identify orphans if a run
is interrupted. See `vercel-ai-sdk-linear-writeback/README.md` for the
contract details (draft semantics, externalId path discipline, the
adapter-doc drift filed as relayfile-adapters#213).
