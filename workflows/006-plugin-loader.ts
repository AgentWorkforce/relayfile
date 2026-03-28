/**
 * 006-plugin-loader.ts
 *
 * Adds discovery for installed @relayfile/adapter-* packages from node_modules.
 * Lets the SDK locate, validate, and register plugin entrypoints without manual wiring.
 *
 * Run: agent-relay run workflows/006-plugin-loader.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('plugin-loader')
  .description('Add node_modules discovery and loading for @relayfile/adapter-* plugins')
  .pattern('dag')
  .channel('wf-relayfile-plugin-loader')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans plugin discovery and loading rules' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements plugin loader and registration hooks' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews discovery safety and test coverage' })

  .step('read-sdk-index', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/index.ts`,
    captureOutput: true,
  })

  .step('plan-loader', {
    agent: 'architect',
    dependsOn: ['read-sdk-index'],
    task: `Read ${SPEC} and the current SDK exports below.

{{steps.read-sdk-index.output}}

Plan:
- discovery of @relayfile/adapter-* packages
- safe loading via package.json exports or default export
- interaction with AdapterRegistry
- clear failure modes and diagnostics
Keep output under 50 lines. End with PLAN_LOADER_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_LOADER_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-loader', {
    agent: 'builder',
    dependsOn: ['plan-loader'],
    task: `Create ${SDK_SRC}/plugin-loader.ts.

Implement:
- discoverInstalledAdapters(cwd?)
- loadAdapterModule(packageName)
- resolve adapter metadata: name, version, factory or class export
- deterministic filtering to @relayfile/adapter-* packages only
- typed loader result objects and diagnostics`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-sdk-hooks', {
    agent: 'builder',
    dependsOn: ['write-loader'],
    task: `Update ${SDK_SRC}/registry.ts and ${SDK_SRC}/index.ts.

Add:
- registry helper to bulk register discovered adapters
- exports for plugin-loader.ts
- optional integration point for RelayFileClient bootstrap
Do not require plugins at import time; load lazily.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-sdk-hooks'],
    task: `Write ${SDK_SRC}/plugin-loader.test.ts.

Cover:
- discovery of matching package names
- ignoring non-plugin packages
- loading modules with supported export shapes
- diagnostic errors for missing entrypoints
- registry bulk registration path`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/plugin-loader.ts && test -f ${SDK_SRC}/plugin-loader.test.ts && echo "plugin loader artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review plugin loader work in ${SDK_SRC}.

Check:
- plugin-loader.ts, registry.ts, index.ts, plugin-loader.test.ts
- discovery is restricted to @relayfile/adapter-* packages
- loading is lazy and diagnosable
- tests cover success and failure paths
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Plugin loader:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
