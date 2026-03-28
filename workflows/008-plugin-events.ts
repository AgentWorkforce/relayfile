/**
 * 008-plugin-events.ts
 *
 * Adds lifecycle events for plugin registration, webhook ingestion, and adapter failures.
 * Exposes a stable event surface the SDK can use for telemetry, observability, and orchestration.
 *
 * Run: agent-relay run workflows/008-plugin-events.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/sdk/typescript`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('plugin-events')
  .description('Add plugin lifecycle events for registration, ingestion, and errors')
  .pattern('dag')
  .channel('wf-relayfile-plugin-events')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans plugin lifecycle event model' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements event emitter and registry hooks' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews event semantics and test coverage' })

  .step('read-event-context', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/client.ts`,
    captureOutput: true,
  })

  .step('plan-events', {
    agent: 'architect',
    dependsOn: ['read-event-context'],
    task: `Read ${SPEC} and the client context below.

{{steps.read-event-context.output}}

Plan:
- registered, ingested, and error event payloads
- event emitter API shape
- where registry and client emit events
- how listeners subscribe safely
Keep output under 50 lines. End with PLAN_EVENTS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_EVENTS_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-event-module', {
    agent: 'builder',
    dependsOn: ['plan-events'],
    task: `Create ${SDK_SRC}/plugin-events.ts.

Implement:
- PluginEventMap types
- createPluginEventEmitter() or class PluginEventEmitter
- typed on/off/emit helpers
- payloads for registered, ingested, and error lifecycle events
- minimal dependency footprint`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-registry-hooks', {
    agent: 'builder',
    dependsOn: ['write-event-module'],
    task: `Update ${SDK_SRC}/registry.ts, ${SDK_SRC}/client.ts, and ${SDK_SRC}/index.ts.

Emit:
- registered when an adapter is added
- ingested after routeWebhook succeeds
- error when routing or ingest fails
Export the event module and expose listener registration from the client or registry.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-registry-hooks'],
    task: `Write ${SDK_SRC}/plugin-events.test.ts.

Cover:
- listeners receive registered events
- listeners receive ingested events with result payloads
- error events include adapter name and failure details
- unsubscribe removes listeners
- registry/client hooks do not double-emit`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/plugin-events.ts && test -f ${SDK_SRC}/plugin-events.test.ts && echo "plugin event artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review plugin event work in ${SDK_SRC}.

Check:
- plugin-events.ts, registry.ts, client.ts, index.ts, plugin-events.test.ts
- payloads are typed and coherent
- events align with registered / ingested / error lifecycle needs
- listeners can subscribe and unsubscribe safely
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Plugin events:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
