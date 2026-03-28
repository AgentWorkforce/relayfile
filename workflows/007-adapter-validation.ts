/**
 * 007-adapter-validation.ts
 *
 * Adds runtime contract validation for providers, adapters, and plugin registration inputs.
 * Prevents broken plugins from registering by checking shape, names, versions, and required hooks.
 *
 * Run: agent-relay run workflows/007-adapter-validation.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('adapter-validation')
  .description('Add runtime validation for plugin contracts and registration inputs')
  .pattern('dag')
  .channel('wf-relayfile-adapter-validation')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans runtime validation coverage for plugin contracts' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements validators, errors, and tests' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews validation rigor and error messages' })

  .step('read-contract-context', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/provider.ts && cat ${SDK_SRC}/index.ts`,
    captureOutput: true,
  })

  .step('plan-validation', {
    agent: 'architect',
    dependsOn: ['read-contract-context'],
    task: `Read ${SPEC} and the contract context below.

{{steps.read-contract-context.output}}

Plan:
- validate provider name and required methods
- validate adapter name, version, and required hooks
- validate registry inputs before registration
- error types and messages
Keep output under 50 lines. End with PLAN_VALIDATION_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_VALIDATION_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-validators', {
    agent: 'builder',
    dependsOn: ['plan-validation'],
    task: `Create ${SDK_SRC}/validation.ts.

Implement:
- validateConnectionProvider(provider)
- validateIntegrationAdapter(adapter)
- validateRegistrationInput(adapterOrFactory)
- helper assertions for string names, semver-ish versions, and required functions
- normalized validation result or thrown error strategy`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-errors-and-exports', {
    agent: 'builder',
    dependsOn: ['write-validators'],
    task: `Update ${SDK_SRC}/errors.ts and ${SDK_SRC}/index.ts.

Add:
- InvalidPluginContractError
- DuplicateAdapterError or equivalent if still missing
- exports for validation.ts and the new error types
- small registry integration hooks if needed`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-errors-and-exports'],
    task: `Write ${SDK_SRC}/validation.test.ts.

Cover:
- valid provider and adapter pass
- missing methods fail with clear errors
- empty names and invalid versions fail
- registration input checks catch duplicate or malformed plugins
- exports remain usable from src/index.ts`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/validation.ts && test -f ${SDK_SRC}/validation.test.ts && echo "adapter validation artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review validation work in ${SDK_SRC}.

Check:
- validation.ts, errors.ts, index.ts, validation.test.ts
- required hooks are actually enforced
- messages are actionable for plugin authors
- registry integration remains deterministic
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Adapter validation:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
