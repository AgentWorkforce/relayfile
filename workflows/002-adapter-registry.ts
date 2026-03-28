/**
 * 002-adapter-registry.ts
 *
 * Adds an AdapterRegistry and RelayFileClient hooks for registerAdapter() and routeWebhook().
 * Provides the SDK runtime entrypoint that binds normalized events to installed adapters.
 *
 * Run: agent-relay run workflows/002-adapter-registry.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('adapter-registry')
  .description('Add adapter registration and webhook routing to RelayFileClient')
  .pattern('dag')
  .channel('wf-relayfile-adapter-registry')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans adapter registry behavior and client API' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements registry and RelayFileClient hooks' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews registration and routing flow' })

  .step('read-client', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/client.ts`,
    captureOutput: true,
  })

  .step('plan-registry', {
    agent: 'architect',
    dependsOn: ['read-client'],
    task: `Read ${SPEC} and the current client below.

{{steps.read-client.output}}

Plan:
- AdapterRegistry class and storage shape
- RelayFileClient.registerAdapter(adapter)
- RelayFileClient.routeWebhook(workspaceId, event)
- error behavior for unknown providers
Keep output under 50 lines. End with PLAN_REGISTRY_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_REGISTRY_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-registry', {
    agent: 'builder',
    dependsOn: ['plan-registry'],
    task: `Create ${SDK_SRC}/registry.ts.

Implement:
- AdapterRegistry class
- registerAdapter(), unregisterAdapter(), getAdapter()
- routeWebhook(workspaceId, event) delegating by event.provider
- typed errors for missing adapters or duplicate names
Use the contracts from provider.ts and adapter.ts.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-client-hooks', {
    agent: 'builder',
    dependsOn: ['write-registry'],
    task: `Update ${SDK_SRC}/client.ts and ${SDK_SRC}/index.ts.

Add:
- a registry instance owned by RelayFileClient
- public registerAdapter() and routeWebhook() methods
- exports for AdapterRegistry
- light integration with existing ingest webhook inputs where useful
Keep the current client API backward compatible.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-client-hooks'],
    task: `Write ${SDK_SRC}/registry.test.ts.

Cover:
- registerAdapter stores adapters by name
- duplicate names throw
- routeWebhook dispatches to the correct adapter
- unknown providers return a clear error
- RelayFileClient forwards to the registry methods`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/registry.ts && test -f ${SDK_SRC}/registry.test.ts && echo "adapter registry artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review adapter registry work in ${SDK_SRC}.

Check:
- registry.ts, client.ts, index.ts, registry.test.ts
- registerAdapter() and routeWebhook() API design
- dispatch rules match ${SPEC}
- duplicate and unknown adapter cases are tested
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Adapter registry:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
