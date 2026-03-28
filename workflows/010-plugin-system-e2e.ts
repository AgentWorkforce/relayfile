/**
 * 010-plugin-system-e2e.ts
 *
 * Adds an end-to-end SDK plugin test that registers a mock adapter, routes a webhook, and verifies file writes.
 * Proves the Phase 1 plugin contracts work together as a single relayfile SDK integration path.
 *
 * Run: agent-relay run workflows/010-plugin-system-e2e.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('plugin-system-e2e')
  .description('Add an end-to-end SDK test for registerAdapter plus routeWebhook flow')
  .pattern('dag')
  .channel('wf-relayfile-plugin-system-e2e')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans the Phase 1 plugin system E2E flow' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements mock system harness and E2E test' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews E2E realism and coverage' })

  .step('read-e2e-context', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/client.ts && cat ${SDK_SRC}/provider.ts && test -f ${SDK_SRC}/registry.ts && cat ${SDK_SRC}/registry.ts || true`,
    captureOutput: true,
  })

  .step('plan-e2e', {
    agent: 'architect',
    dependsOn: ['read-e2e-context'],
    task: `Read ${SPEC} and the current SDK context below.

{{steps.read-e2e-context.output}}

Plan the E2E flow:
- create mock provider and adapter
- register adapter on RelayFileClient
- route a normalized webhook
- verify file write side effects and emitted result
Keep output under 50 lines. End with PLAN_E2E_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_E2E_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-mock-system', {
    agent: 'builder',
    dependsOn: ['plan-e2e'],
    task: `Create ${SDK_SRC}/testing/mock-system.ts.

Implement:
- createMockRelayFileClient()
- in-memory file write capture helpers
- makeRegisteredMockPlugin() using the shared test helpers
- utilities to assert written paths and revisions
Keep it isolated from live network calls.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-e2e-test', {
    agent: 'builder',
    dependsOn: ['write-mock-system'],
    task: `Write ${SDK_SRC}/plugin-system.e2e.test.ts.

Cover:
- register mock adapter on the client
- route a mock normalized webhook through the registry
- adapter writes the expected file path
- IngestResult returns written paths and counts
- emitted lifecycle events and error path behavior`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-test-hooks', {
    agent: 'builder',
    dependsOn: ['write-e2e-test'],
    task: `Update ${SDK_PACKAGE}/package.json and ${SDK_SRC}/index.ts as needed.

Add:
- a test script or documented E2E test entrypoint if missing
- exports for the mock system only if they belong in testing surfaces
- any tiny helper wiring needed for the Phase 1 end-to-end path`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-test-hooks'],
    command: `test -f ${SDK_SRC}/testing/mock-system.ts && test -f ${SDK_SRC}/plugin-system.e2e.test.ts && echo "plugin system e2e artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review the Phase 1 E2E work in ${SDK_SRC}.

Check:
- testing/mock-system.ts, plugin-system.e2e.test.ts, package.json, index.ts
- flow covers registerAdapter -> routeWebhook -> file write verification
- test harness stays in-memory and deterministic
- result and event assertions are meaningful
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Plugin system E2E:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
