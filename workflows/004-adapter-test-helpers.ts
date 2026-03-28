/**
 * 004-adapter-test-helpers.ts
 *
 * Adds reusable mock providers, mock adapters, and fixtures for plugin interface testing.
 * Gives SDK and adapter repos a stable harness for contract-level and E2E tests.
 *
 * Run: agent-relay run workflows/004-adapter-test-helpers.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/sdk/typescript`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('adapter-test-helpers')
  .description('Add shared mock providers, adapters, and fixtures for plugin testing')
  .pattern('dag')
  .channel('wf-relayfile-adapter-test-helpers')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans the shared plugin test harness' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements mocks, fixtures, and helper exports' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews helper ergonomics and coverage' })

  .step('read-test-baseline', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/client.test.ts && cat ${SDK_SRC}/composio.test.ts`,
    captureOutput: true,
  })

  .step('plan-helpers', {
    agent: 'architect',
    dependsOn: ['read-test-baseline'],
    task: `Read ${SPEC} and the current test style below.

{{steps.read-test-baseline.output}}

Plan:
- mock ConnectionProvider
- mock IntegrationAdapter
- canned webhook and proxy fixtures
- shared testing exports under src/testing
Keep output under 50 lines. End with PLAN_HELPERS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_HELPERS_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-mocks', {
    agent: 'builder',
    dependsOn: ['plan-helpers'],
    task: `Create ${SDK_SRC}/testing/mock-provider.ts and ${SDK_SRC}/testing/mock-adapter.ts.

Implement:
- createMockProvider(options)
- createMockAdapter(client, provider, overrides)
- sensible defaults for proxy, healthCheck, ingestWebhook, computePath, computeSemantics
- easy hooks for assertions in tests`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-fixtures-and-exports', {
    agent: 'builder',
    dependsOn: ['write-mocks'],
    task: `Create ${SDK_SRC}/testing/fixtures.ts and ${SDK_SRC}/testing/index.ts.

Add:
- normalized webhook fixtures
- proxy response fixtures
- ingest result fixtures
- re-exports from src/index.ts for the testing helpers
Keep names stable for other repos to consume.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-fixtures-and-exports'],
    task: `Write ${SDK_SRC}/adapter-test-helpers.test.ts.

Cover:
- mock provider records proxy calls
- mock adapter can ingest a normalized webhook
- fixtures are reusable and correctly shaped
- testing exports are available from src/index.ts
- overrides let callers customize behavior`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/testing/mock-provider.ts && test -f ${SDK_SRC}/testing/mock-adapter.ts && test -f ${SDK_SRC}/testing/fixtures.ts && echo "adapter test helper artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review test helper work in ${SDK_SRC}/testing.

Check:
- mock-provider.ts, mock-adapter.ts, fixtures.ts, testing/index.ts
- adapter-test-helpers.test.ts and index.ts exports
- helper API is reusable from other repos
- fixtures match ${SPEC} contracts
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Adapter test helpers:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
