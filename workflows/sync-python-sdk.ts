/**
 * sync-python-sdk.ts
 *
 * Analyze gaps between TypeScript and Python SDKs,
 * then bring the Python SDK to parity.
 *
 * Run: agent-relay run workflows/sync-python-sdk.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const ROOT = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const TS_SDK = `${ROOT}/packages/sdk/typescript/src`;
const PY_SDK = `${ROOT}/packages/sdk/python/src/relayfile`;

async function main() {
const result = await workflow('sync-python-sdk')
  .description('Bring Python SDK to parity with TypeScript SDK')
  .pattern('dag')
  .channel('wf-sync-python-sdk')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('analyst', { cli: 'claude', role: 'Analyzes gaps and designs sync plan' })
  .agent('types-builder', { cli: 'codex', preset: 'worker', role: 'Adds missing types to Python SDK' })
  .agent('client-builder', { cli: 'codex', preset: 'worker', role: 'Adds missing client methods' })
  .agent('provider-builder', { cli: 'codex', preset: 'worker', role: 'Adds missing provider/composio code' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews Python SDK changes' })

  .step('analyze-gaps', {
    agent: 'analyst',
    task: `Compare the TypeScript SDK at ${TS_SDK}/ with the Python SDK at ${PY_SDK}/.

Read both SDKs completely and identify ALL gaps where Python is behind TypeScript:

1. Missing types in types.py vs types.ts (e.g. BulkWriteFile, BulkWriteInput, BulkWriteResponse, ExportFormat, ExportOptions, ExportJsonResponse)
2. Missing client methods (bulkWrite, exportWorkspace)
3. Missing provider abstractions (IntegrationProvider abstract class, WebhookInput, computeCanonicalPath — TS has provider.ts, Python has nango.py but no abstract provider)
4. Missing composio integration (TS has composio.ts, Python has nothing)
5. Any API differences in existing methods (parameter names, return types)
6. Test coverage gaps

For each gap, specify: what file to edit, what to add, and what TS source to reference.

Keep output under 80 lines. End with ANALYSIS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'ANALYSIS_COMPLETE' },
    timeout: 300_000,
  })

  .step('sync-types', {
    agent: 'types-builder',
    dependsOn: ['analyze-gaps'],
    task: `Add missing types to the Python SDK at ${PY_SDK}/types.py.

Gap analysis: {{steps.analyze-gaps.output}}

Reference TypeScript types at ${TS_SDK}/types.ts.
Add all missing dataclasses to match the TS SDK exactly:
- BulkWriteFile, BulkWriteInput, BulkWriteResponse
- ExportFormat, ExportOptions, ExportJsonResponse
- Any other missing types identified in the analysis

Use Python dataclasses with proper type hints. Follow the existing style in types.py.
Run: cd ${ROOT} && python -m pytest packages/sdk/python/tests/ -x 2>/dev/null || echo "Tests checked"
End with TYPES_SYNC_COMPLETE.`,
    verification: { type: 'output_contains', value: 'TYPES_SYNC_COMPLETE' },
    timeout: 600_000,
  })

  .step('sync-client', {
    agent: 'client-builder',
    dependsOn: ['analyze-gaps'],
    task: `Add missing client methods to the Python SDK at ${PY_SDK}/client.py.

Gap analysis: {{steps.analyze-gaps.output}}

Reference TypeScript client at ${TS_SDK}/client.ts.
Add all missing methods to both RelayFileClient (sync) and AsyncRelayFileClient (async):
- bulk_write() — matching TS bulkWrite()
- export_workspace() — matching TS exportWorkspace()
- Any other missing methods identified in the analysis

Follow the existing Python client style (httpx, retry logic, error handling).
Run: cd ${ROOT} && python -m pytest packages/sdk/python/tests/ -x 2>/dev/null || echo "Tests checked"
End with CLIENT_SYNC_COMPLETE.`,
    verification: { type: 'output_contains', value: 'CLIENT_SYNC_COMPLETE' },
    timeout: 600_000,
  })

  .step('sync-provider', {
    agent: 'provider-builder',
    dependsOn: ['analyze-gaps'],
    task: `Add missing provider abstractions and composio integration to the Python SDK.

Gap analysis: {{steps.analyze-gaps.output}}

1. Create ${PY_SDK}/provider.py — matching TS ${TS_SDK}/provider.ts:
   - WebhookInput dataclass
   - ListProviderFilesOptions, WatchProviderEventsOptions dataclasses
   - compute_canonical_path() function
   - IntegrationProvider abstract base class with ingest_webhook(), get_provider_files(), watch_provider_events()

2. Create ${PY_SDK}/composio.py — matching TS ${TS_SDK}/composio.ts:
   - ComposioProvider class extending IntegrationProvider
   - Composio webhook normalization
   - Composio-specific types

3. Update ${PY_SDK}/__init__.py to export new modules

Follow the existing Python style. Use abc.ABC for abstract classes.
Run: cd ${ROOT} && python -m pytest packages/sdk/python/tests/ -x 2>/dev/null || echo "Tests checked"
End with PROVIDER_SYNC_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PROVIDER_SYNC_COMPLETE' },
    timeout: 600_000,
  })

  .step('review-and-test', {
    agent: 'reviewer',
    dependsOn: ['sync-types', 'sync-client', 'sync-provider'],
    task: `Review the Python SDK sync at ${PY_SDK}/.

Check:
1. All types from TS types.ts exist in Python types.py
2. All client methods from TS client.ts exist in Python client.py (sync + async)
3. provider.py has the abstract IntegrationProvider matching TS
4. composio.py exists and matches TS composio.ts
5. __init__.py exports everything
6. Code follows existing Python style
7. Tests pass: cd ${ROOT} && python -m pytest packages/sdk/python/tests/ -x

Fix any issues. Also create a SYNC_STATUS.md in ${ROOT}/packages/sdk/python/ documenting:
- What was synced
- Remaining differences (if any)
- How to keep SDKs in sync going forward

Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 300_000,
  })

 .step('commit', {
    agent: 'types-builder',
    dependsOn: ['review-and-test'],
    task: `In ${ROOT}:
1. git checkout -b feat/python-sdk-sync
2. git add packages/sdk/python/
3. git commit -m "feat: sync Python SDK to TypeScript SDK parity

Added missing types: BulkWriteFile, BulkWriteInput, BulkWriteResponse,
ExportFormat, ExportOptions, ExportJsonResponse

Added missing client methods: bulk_write(), export_workspace()

Added provider abstractions: IntegrationProvider ABC, WebhookInput,
compute_canonical_path(), ListProviderFilesOptions, WatchProviderEventsOptions

Added composio integration: ComposioProvider class

Updated __init__.py exports"
4. git push origin feat/python-sdk-sync

Report commit hash. End with COMMIT_COMPLETE.`,
    verification: { type: 'output_contains', value: 'COMMIT_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Python SDK sync complete:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
