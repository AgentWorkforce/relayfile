/**
 * Tier-aware Relayfile feature verification.
 *
 * Run: agent-relay run workflows/verify-features.ts
 * Optional: RELAYFILE_FEATURE_LIVE_TIER=3|5|6 RELAYFILE_REQUIRE_LIVE=1
 */
import { workflow } from '@agent-relay/sdk/workflows';

async function main() {
  const result = await workflow('relayfile-verify-features')
    .description('Validate the authoritative catalog, deterministic local/packed tiers, and an explicitly selected live tier with honest result accounting.')
    .pattern('pipeline')
    .channel('wf-relayfile-verify-features')
    .maxConcurrency(1)
    .timeout(3_600_000)
    .step('catalog-contract', {
      type: 'deterministic',
      command: 'npm run features:validate && npm run features:test',
      captureOutput: true,
      failOnError: true,
    })
    .step('deterministic-tiers', {
      type: 'deterministic',
      dependsOn: ['catalog-contract'],
      command: 'npm run features:verify -- --tier deterministic',
      captureOutput: true,
      failOnError: true,
    })
    .step('explicit-live-tier', {
      type: 'deterministic',
      dependsOn: ['deterministic-tiers'],
      command: [
        'if [ -z "${RELAYFILE_FEATURE_LIVE_TIER:-}" ]; then',
        '  echo "SKIP live-tier: RELAYFILE_FEATURE_LIVE_TIER is absent"',
        'else',
        '  REQUIRE=""',
        '  if [ "${RELAYFILE_REQUIRE_LIVE:-0}" = "1" ]; then REQUIRE="--require-live"; fi',
        '  npm run features:verify -- --tier "$RELAYFILE_FEATURE_LIVE_TIER" $REQUIRE',
        'fi',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })
    .step('verify-evidence', {
      type: 'deterministic',
      dependsOn: ['explicit-live-tier'],
      command: 'test -s .workflow-artifacts/verify-features/latest.json && node -e "const r=require(\'./.workflow-artifacts/verify-features/latest.json\'); if(r.totals.FAIL) process.exit(1)"',
      captureOutput: true,
      failOnError: true,
    })
    .run({ cwd: process.cwd() });
  console.log(result.status);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
