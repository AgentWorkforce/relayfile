/**
 * 001-adapter-plugin-types.ts
 *
 * Adds ConnectionProvider, NormalizedWebhook, and IntegrationAdapter contracts to @relayfile/sdk.
 * Establishes the shared plugin surface for providers and adapters across the relayfile ecosystem.
 *
 * Run: agent-relay run workflows/001-adapter-plugin-types.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('adapter-plugin-types')
  .description('Add provider and adapter plugin contracts to @relayfile/sdk')
  .pattern('dag')
  .channel('wf-relayfile-adapter-plugin-types')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans SDK plugin contract changes' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements provider and adapter contract types' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews plugin contract coverage and exports' })

  .step('read-current-provider', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/provider.ts`,
    captureOutput: true,
  })

  .step('plan-contracts', {
    agent: 'architect',
    dependsOn: ['read-current-provider'],
    task: `Read ${SPEC} and the current provider contract below.

{{steps.read-current-provider.output}}

Plan these SDK additions:
- ConnectionProvider + ProxyRequest/ProxyResponse
- NormalizedWebhook
- IntegrationAdapter + IngestResult/SyncResult
- export strategy from src/index.ts
Keep output under 50 lines. End with PLAN_CONTRACTS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_CONTRACTS_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-provider-contracts', {
    agent: 'builder',
    dependsOn: ['plan-contracts'],
    task: `Update ${SDK_SRC}/provider.ts.

Based on: {{steps.plan-contracts.output}}

Add:
- ConnectionProvider interface
- ProxyRequest and ProxyResponse types
- NormalizedWebhook interface
- shared provider helper types needed by adapters
Keep IntegrationProvider working or mark it as legacy-compatible.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-adapter-base', {
    agent: 'builder',
    dependsOn: ['write-provider-contracts'],
    task: `Create ${SDK_SRC}/adapter.ts and update ${SDK_SRC}/index.ts.

Implement:
- abstract class IntegrationAdapter
- IngestResult, SyncOptions, SyncResult types
- optional writeBack, sync, supportedEvents hooks
- exports from src/index.ts for the new plugin surface
Match the naming and shapes in ${SPEC}.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-contract-tests', {
    agent: 'builder',
    dependsOn: ['write-adapter-base'],
    task: `Write ${SDK_SRC}/plugin-interface.test.ts.

Cover:
- ConnectionProvider mock satisfies the contract
- IntegrationAdapter subclass compiles and runs
- IngestResult and SyncResult shapes are stable
- new exports are available from src/index.ts
Use node:test or the repo's current test style.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-contract-tests'],
    command: `test -f ${SDK_SRC}/adapter.ts && test -f ${SDK_SRC}/plugin-interface.test.ts && echo "plugin type artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review the Phase 1 SDK contract work in ${SDK_SRC}.

Check:
- provider.ts, adapter.ts, index.ts, plugin-interface.test.ts
- names and signatures match ${SPEC}
- legacy IntegrationProvider remains coherent
- exports are complete and non-circular
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Adapter plugin types:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
