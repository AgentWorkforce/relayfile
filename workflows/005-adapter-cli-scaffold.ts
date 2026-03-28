/**
 * 005-adapter-cli-scaffold.ts
 *
 * Scaffolds a create-relayfile-adapter CLI for bootstrapping provider and adapter packages.
 * Gives the ecosystem a repeatable generator for new plugins, config, tests, and workflow stubs.
 *
 * Run: agent-relay run workflows/005-adapter-cli-scaffold.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const SDK_REPO = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const WORKFLOWS_DIR = `${SDK_REPO}/workflows`;
const CLI_PACKAGE = `${SDK_REPO}/packages/create-relayfile-adapter`;
const GITHUB_ADAPTER_REPO = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile-adapter-github';
const SPEC = `${GITHUB_ADAPTER_REPO}/docs/adapter-spec.md`;

async function main() {
const result = await workflow('adapter-cli-scaffold')
  .description('Scaffold a create-relayfile-adapter CLI for new provider and adapter packages')
  .pattern('dag')
  .channel('wf-relayfile-adapter-cli-scaffold')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Plans the plugin scaffold CLI and templates' })
  .agent('builder', { cli: 'codex', preset: 'worker', role: 'Implements the CLI package and templates' })
  .agent('reviewer', { cli: 'claude', role: 'Reviews scaffold UX and generated outputs' })

  .step('read-package-layout', {
    type: 'deterministic',
    command: `cat ${SDK_REPO}/packages/relayfile-sdk/package.json`,
    captureOutput: true,
  })

  .step('plan-cli', {
    agent: 'architect',
    dependsOn: ['read-package-layout'],
    task: `Read ${SPEC} and the SDK package baseline below.

{{steps.read-package-layout.output}}

Plan:
- create-relayfile-adapter package layout
- CLI args for adapter vs provider scaffolds
- generated files and template variables
- workflow stub generation into new repos
Keep output under 50 lines. End with PLAN_CLI_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PLAN_CLI_COMPLETE' },
    timeout: 120_000,
  })

  .step('init-cli-package', {
    agent: 'builder',
    dependsOn: ['plan-cli'],
    task: `Create ${CLI_PACKAGE}/package.json, ${CLI_PACKAGE}/src, and ${CLI_PACKAGE}/bin.

Package requirements:
- name: create-relayfile-adapter
- bin entry: create-relayfile-adapter
- scripts: build, typecheck
- dependencies only if needed
- templates directory for adapter and provider starters`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-cli-entry', {
    agent: 'builder',
    dependsOn: ['init-cli-package'],
    task: `Write ${CLI_PACKAGE}/bin/create-relayfile-adapter.js and ${CLI_PACKAGE}/src/index.ts.

Implement:
- parse name, kind, provider, and target dir flags
- generate package metadata, src skeleton, tests, README, and workflow stub
- support --kind adapter and --kind provider
- print next steps after generation`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('write-templates-and-readme', {
    agent: 'builder',
    dependsOn: ['write-cli-entry'],
    task: `Create template files under ${CLI_PACKAGE}/templates and add ${CLI_PACKAGE}/README.md.

Templates should cover:
- adapter package skeleton
- provider package skeleton
- sample workflow file
- config schema and test placeholders
Document usage and generated structure in the README.`,
    verification: { type: 'exit_code' },
    timeout: 180_000,
  })

  .step('verify-artifacts', {
    type: 'deterministic',
    dependsOn: ['write-templates-and-readme'],
    command: `test -f ${CLI_PACKAGE}/package.json && test -f ${CLI_PACKAGE}/bin/create-relayfile-adapter.js && test -f ${CLI_PACKAGE}/src/index.ts && test -f ${CLI_PACKAGE}/README.md && echo "adapter CLI scaffold artifacts present"`,
    failOnError: true,
    captureOutput: true,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['verify-artifacts'],
    task: `Review the CLI scaffold package at ${CLI_PACKAGE}.

Check:
- package.json, bin/create-relayfile-adapter.js, src/index.ts
- template completeness for adapters and providers
- README and generated workflow stub coverage
- CLI flags align with ${SPEC}
Keep output under 50 lines. End with REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'REVIEW_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ cwd: process.cwd() });

console.log('Adapter CLI scaffold:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
