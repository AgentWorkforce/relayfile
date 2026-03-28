/**
 * 009-plugin-config-schema.ts
 *
 * Adds JSON Schema support for adapter and provider configuration contracts.
 * Standardizes how plugins publish defaults, validation rules, and machine-readable config metadata.
 *
 * Run: agent-relay run workflows/009-plugin-config-schema.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/sdk/typescript`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('plugin-config-schema')
  .description('Add JSON Schema utilities for adapter and provider configuration')
  .pattern('dag')
  .channel('wf-relayfile-plugin-config-schema')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans machine-readable config schema support' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements schema types, helpers, and tests' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews schema expressiveness and stability' })

  .step('read-config-context', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/types.ts`,
    captureOutput: true,
  })

  .step('plan-schemas', {
    agent: 'architect',
    dependsOn: ['read-config-context'],
    task: `Read ${SPEC} and the SDK type context below.

{{steps.read-config-context.output}}

Plan:
- adapter config schema shape
- provider config schema shape
- helper types for defaults and examples
- exports and validation boundaries
Keep output under 50 lines. End with PLAN_SCHEMAS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_SCHEMAS_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-schema-module', {
    agent: 'builder',
    dependsOn: ['plan-schemas'],
    task: `Create ${SDK_SRC}/config-schema.ts.

Implement:
- JsonSchema type helpers for plugin configs
- AdapterConfigSchema and ProviderConfigSchema interfaces
- defineAdapterConfigSchema() and defineProviderConfigSchema()
- support for defaults, examples, required, descriptions, and version metadata`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-schema-exports', {
    agent: 'builder',
    dependsOn: ['write-schema-module'],
    task: `Update ${SDK_SRC}/index.ts and any supporting type modules.

Export:
- config schema helpers and interfaces
- light validation helpers if needed
- optional bridge functions for plugin loader or registry integration
Keep the public SDK surface coherent and typed.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-schema-exports'],
    task: `Write ${SDK_SRC}/config-schema.test.ts.

Cover:
- adapter and provider schemas can be declared
- required fields and defaults are preserved
- invalid schema definitions fail clearly
- exports from src/index.ts are correct
- schema metadata stays JSON serializable`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/config-schema.ts && test -f ${SDK_SRC}/config-schema.test.ts && echo "plugin config schema artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review config schema work in ${SDK_SRC}.

Check:
- config-schema.ts, index.ts, config-schema.test.ts
- schema helpers are adapter/provider agnostic
- defaults and required keys survive serialization
- exported API matches the plugin goals in ${SPEC}
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Plugin config schema:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
