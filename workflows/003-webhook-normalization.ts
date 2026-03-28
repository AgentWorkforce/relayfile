/**
 * 003-webhook-normalization.ts
 *
 * Adds a NormalizedWebhook converter layer that turns provider-specific payloads into SDK events.
 * Bridges the legacy webhook input model to the new adapter-facing webhook contract.
 *
 * Run: agent-relay run workflows/003-webhook-normalization.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const SDK_PACKAGE = `${SDK_REPO}/packages/relayfile-sdk`;
const SDK_SRC = `${SDK_PACKAGE}/src`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('webhook-normalization')
  .description('Add NormalizedWebhook converters and legacy bridge helpers')
  .pattern('dag')
  .channel('wf-relayfile-webhook-normalization')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans webhook normalization boundaries' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements normalization helpers and bridges' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews normalized webhook flow and tests' })

  .step('read-webhook-context', {
    type: 'deterministic',
    command: `cat ${SDK_SRC}/provider.ts && cat ${SDK_SRC}/nango.ts && cat ${SDK_SRC}/composio.ts`,
    captureOutput: true,
  })

  .step('plan-normalization', {
    agent: 'architect',
    dependsOn: ['read-webhook-context'],
    task: `Read ${SPEC} and the webhook helpers below.

{{steps.read-webhook-context.output}}

Plan:
- one canonical NormalizedWebhook converter module
- legacy WebhookInput to NormalizedWebhook bridge
- Nango and Composio helper updates
- error handling for malformed payloads
Keep output under 50 lines. End with PLAN_NORMALIZATION_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_NORMALIZATION_COMPLETE' },
    timeout: 120_000,
  })

  .step('write-normalizer', {
    agent: 'builder',
    dependsOn: ['plan-normalization'],
    task: `Create ${SDK_SRC}/normalized-webhook.ts.

Implement:
- normalizeWebhook(raw, provider)
- fromWebhookInput(input)
- toIngestWebhookInput(event, path)
- typed guards for provider, objectType, objectId, eventType
Keep the module generic so adapters and providers can both consume it.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-bridge-updates', {
    agent: 'builder',
    dependsOn: ['write-normalizer'],
    task: `Update ${SDK_SRC}/provider.ts, ${SDK_SRC}/nango.ts, ${SDK_SRC}/composio.ts, and ${SDK_SRC}/index.ts.

Wire the new normalizer into existing helper surfaces.
Expose bridge helpers without breaking current exports.
Make malformed webhook failures explicit and typed.
Match the NormalizedWebhook shape in ${SPEC}.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-tests', {
    agent: 'builder',
    dependsOn: ['write-bridge-updates'],
    task: `Write ${SDK_SRC}/webhook-normalization.test.ts.

Cover:
- legacy WebhookInput conversion
- provider-specific raw payload normalization
- missing fields and malformed payload errors
- toIngestWebhookInput preserves provider, event type, and data
- exports from src/index.ts are valid`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-tests'],
    command: `test -f ${SDK_SRC}/normalized-webhook.ts && test -f ${SDK_SRC}/webhook-normalization.test.ts && echo "webhook normalization artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review webhook normalization work in ${SDK_SRC}.

Check:
- normalized-webhook.ts, provider.ts, nango.ts, composio.ts, webhook-normalization.test.ts
- bridges align with ${SPEC}
- malformed input handling is deterministic
- legacy helpers remain coherent
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Webhook normalization:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
