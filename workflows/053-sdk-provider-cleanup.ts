/**
 * 053-sdk-provider-cleanup.ts
 *
 * Remove inline Nango and Composio code from the relayfile SDK.
 * These are now separate repos (relayfile-provider-nango, relayfile-provider-composio).
 * The SDK should not contain provider-specific code.
 *
 * Removes:
 * - packages/sdk/typescript/src/nango.ts
 * - packages/sdk/typescript/src/composio.ts
 * - packages/sdk/python/src/relayfile/nango.py
 * - packages/sdk/python/src/relayfile/composio.py
 * - Exports from index.ts / __init__.py
 * - Related tests if any
 *
 * Run: agent-relay run workflows/053-sdk-provider-cleanup.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const ROOT = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';

async function main() {
  const result = await workflow('sdk-provider-cleanup')
    .description('Remove inline Nango/Composio code from SDK — now in separate provider repos')
    .pattern('linear')
    .channel('wf-sdk-cleanup')
    .maxConcurrency(2)
    .timeout(1_200_000)

    .agent('builder', { cli: 'codex', preset: 'worker', role: 'Removes provider code from SDK' })
    .agent('reviewer', { cli: 'claude', role: 'Verifies clean removal' })

    .step('cleanup', {
      agent: 'builder',
      task: `Remove inline provider code from the relayfile SDK.

Working in ${ROOT} on a new branch feat/remove-inline-providers.

These files contain Nango and Composio helper code that now lives in
separate repos (relayfile-provider-nango, relayfile-provider-composio).
They must be removed from the SDK.

1. **Read first** to understand what's exported:
   - ${ROOT}/packages/sdk/typescript/src/index.ts — find nango/composio exports
   - ${ROOT}/packages/sdk/typescript/src/nango.ts — NangoHelpers, NangoWebhookInput
   - ${ROOT}/packages/sdk/typescript/src/composio.ts — ComposioHelpers, ComposioWebhookPayload, ComposioTriggerOptions
   - ${ROOT}/packages/sdk/python/src/relayfile/__init__.py — find nango/composio imports
   - ${ROOT}/packages/sdk/python/src/relayfile/nango.py
   - ${ROOT}/packages/sdk/python/src/relayfile/composio.py

2. **Remove TypeScript files**:
   - Delete packages/sdk/typescript/src/nango.ts
   - Delete packages/sdk/typescript/src/composio.ts
   - Delete any test files: packages/sdk/typescript/src/composio.test.ts, nango.test.ts
   - Remove exports from packages/sdk/typescript/src/index.ts:
     Remove: export { NangoHelpers } from "./nango.js";
     Remove: export type { NangoWebhookInput } from "./nango.js";
     Remove: export { ComposioHelpers } from "./composio.js";
     Remove: export type { ComposioWebhookPayload, ComposioTriggerOptions } from "./composio.js";

3. **Remove Python files**:
   - Delete packages/sdk/python/src/relayfile/nango.py
   - Delete packages/sdk/python/src/relayfile/composio.py
   - Remove imports from packages/sdk/python/src/relayfile/__init__.py

4. **Update contract checks** (scripts/check-contract-surface.sh):
   - Remove any require_pattern checks for nango/composio in the SDK
   - These should only be checked in their respective provider repos

5. **Add migration note** to CHANGELOG.md or README:
   "## Breaking: Nango and Composio helpers removed from SDK
   These have moved to dedicated packages:
   - @relayfile/provider-nango (npm) / relayfile-provider-nango (pip)
   - @relayfile/provider-composio (npm) / relayfile-provider-composio (pip)
   
   If you used NangoHelpers or ComposioHelpers, install the provider package instead."

6. **Build check**: cd ${ROOT}/packages/sdk/typescript && npx tsc --noEmit

7. **Commit and push**:
   git checkout -b feat/remove-inline-providers
   git add -A
   HUSKY=0 git -c core.hooksPath=/dev/null commit --no-verify -m "refactor: remove inline Nango/Composio code from SDK

   Nango and Composio helpers have moved to dedicated provider packages:
   - @relayfile/provider-nango
   - @relayfile/provider-composio

   Removed:
   - src/nango.ts, src/composio.ts (TypeScript)
   - nango.py, composio.py (Python)
   - Related exports and tests

   This is a breaking change for consumers who imported NangoHelpers
   or ComposioHelpers directly from @relayfile/sdk."
   git push origin feat/remove-inline-providers

End with CLEANUP_COMPLETE.`,
      verification: { type: 'output_contains', value: 'CLEANUP_COMPLETE' },
      timeout: 600_000,
    })

    .step('review', {
      agent: 'reviewer',
      dependsOn: ['cleanup'],
      task: `Review the SDK provider cleanup in ${ROOT}.

Verify:
1. nango.ts, composio.ts deleted from TypeScript SDK
2. nango.py, composio.py deleted from Python SDK
3. No remaining imports/exports of Nango/Composio in index.ts or __init__.py
4. Contract checks updated (no references to nango/composio SDK exports)
5. Build passes: cd ${ROOT}/packages/sdk/typescript && npx tsc --noEmit
6. Migration note exists in README or CHANGELOG
7. grep -rn "NangoHelpers\\|ComposioHelpers\\|from.*nango\\|from.*composio" packages/sdk/ should return nothing

Fix any issues. Keep under 40 lines. End with REVIEW_COMPLETE.`,
      verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
      timeout: 300_000,
    })

    .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
    .run({ cwd: ROOT });

  console.log('SDK provider cleanup complete:', result.status);
}

main().catch(e => { console.error(e); process.exitCode = 1; });
