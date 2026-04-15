import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('054-relayfile-cli-e2e-and-cli-dependency-proof')
    .description('Implement and fully validate a bounded relayfile CLI slice that can install/use required external CLIs and prove end-to-end behavior under the 80-to-100 workflow discipline.')
    .pattern('supervisor')
    .channel('wf-054-relayfile-cli-e2e-and-cli-dependency-proof')
    .maxConcurrency(4)
    .timeout(10_800_000)

    .agent('lead-claude', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Defines the bounded relayfile CLI proof slice and the exact end-to-end validation bar.',
      retries: 1,
    })
    .agent('impl-codex', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements the relayfile CLI proof slice and drives the 80-to-100 validation loop.',
      retries: 1,
    })
    .agent('review-codex', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'reviewer',
      role: 'Reviews boundedness, CLI dependency handling, and whether the E2E proof is genuinely sufficient.',
      retries: 1,
    })

    .step('inspect-relayfile-cli-context', {
      type: 'deterministic',
      command: [
        'echo "---CLI DESIGN---"',
        'sed -n "1,320p" docs/cli-design.md',
        'echo "" && echo "---PACKAGE---"',
        'cat package.json',
        'echo "" && echo "---WORKFLOWS---"',
        'find workflows -maxdepth 1 -type f | sort | sed -n "1,220p"',
        'echo "" && echo "---SCRIPTS---"',
        'find scripts -maxdepth 1 -type f | sort | sed -n "1,220p"',
        'echo "" && echo "---CLI PACKAGE FILES---"',
        'find packages/cli -maxdepth 3 -type f | sort | sed -n "1,260p"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .step('define-relayfile-cli-proof-boundary', {
      agent: 'lead-claude',
      dependsOn: ['inspect-relayfile-cli-context'],
      task: `Define the exact bounded relayfile CLI proof slice and the required 80-to-100 validation bar.\n\n{{steps.inspect-relayfile-cli-context.output}}\n\nRequirements:\n1. assume we can install the CLIs needed for the proof if they are missing\n2. define the smallest bounded slice that proves real relayfile CLI value end to end\n3. include explicit deterministic validation steps inside the workflow, not just docs\n4. make clear what external CLI(s) are required and how the workflow should verify/install them\n5. keep the slice mergeable and truthful\n\nWrite:\n- docs/cli-proof-boundary.md\n- docs/cli-proof-no-regression-checklist.md\n- docs/cli-proof-plan.md\n\nEnd with RELAYFILE_CLI_PROOF_BOUNDARY_READY.`,
      verification: { type: 'file_exists', value: 'docs/cli-proof-boundary.md' },
    })

    .step('implement-relayfile-cli-proof', {
      agent: 'impl-codex',
      dependsOn: ['define-relayfile-cli-proof-boundary'],
      task: `Implement the bounded relayfile CLI proof slice.\n\nRead:\n- docs/cli-proof-boundary.md\n- docs/cli-proof-no-regression-checklist.md\n- docs/cli-proof-plan.md\n\nRequirements:\n1. follow the relay 80-to-100 discipline: implementation, deterministic validation, fix, rerun until green\n2. install or verify required CLI dependencies if needed for the proof\n3. add tests/scripts/docs needed for a real end-to-end proof\n4. do not stop at compile-only confidence\n5. keep the slice bounded and mergeable\n\nEnd with RELAYFILE_CLI_PROOF_IMPLEMENTATION_READY.`,
      verification: { type: 'exit_code' },
    })

    .step('validate-relayfile-cli-proof', {
      type: 'deterministic',
      dependsOn: ['implement-relayfile-cli-proof'],
      command: [
        'npm test 2>&1 || true',
        'npm run build 2>&1 || true',
        'find docs -maxdepth 1 -type f | sort | grep "cli-proof" || true',
      ].join(' && '),
      captureOutput: true,
      failOnError: false,
    })

    .step('review-relayfile-cli-proof', {
      agent: 'review-codex',
      dependsOn: ['validate-relayfile-cli-proof'],
      task: `Review the relayfile CLI proof slice.\n\nRead:\n- docs/cli-proof-boundary.md\n- docs/cli-proof-no-regression-checklist.md\n- docs/cli-proof-plan.md\n- changed files\n- validation output:\n{{steps.validate-relayfile-cli-proof.output}}\n\nWrite:\n- docs/cli-proof-review-verdict.md\n\nAssess:\n1. did the slice genuinely clear the 80-to-100 bar?\n2. were external CLI dependencies handled honestly and robustly?\n3. is the proof end-to-end enough to trust the slice?\n4. what is the next relayfile CLI slice after this?\n\nEnd with RELAYFILE_CLI_PROOF_REVIEW_COMPLETE.`,
      verification: { type: 'file_exists', value: 'docs/cli-proof-review-verdict.md' },
    })

    .step('verify-relayfile-cli-proof-artifacts', {
      type: 'deterministic',
      dependsOn: ['review-relayfile-cli-proof'],
      command: [
        'test -f docs/cli-proof-boundary.md',
        'test -f docs/cli-proof-no-regression-checklist.md',
        'test -f docs/cli-proof-plan.md',
        'test -f docs/cli-proof-review-verdict.md',
        'grep -q "RELAYFILE_CLI_PROOF_REVIEW_COMPLETE" docs/cli-proof-review-verdict.md',
        'echo "RELAYFILE_CLI_PROOF_VERIFIED"',
      ].join(' && '),
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
