/**
 * 058-emitted-shape-canonical-conformance-proof.ts
 *
 * Define and implement canonical schema conformance against captured emitted shapes
 * from real producer paths rather than hand-authored inline payloads.
 *
 * Run: agent-relay run workflows/058-emitted-shape-canonical-conformance-proof.ts
 */
import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('058-emitted-shape-canonical-conformance-proof')
    .description('Define and implement the next bounded relayfile proof: canonical schema conformance against captured emitted shapes from real producer paths rather than hand-authored inline payloads.')
    .pattern('supervisor')
    .channel('wf-058-emitted-shape-canonical-conformance-proof')
    .maxConcurrency(4)
    .timeout(10_800_000)
    .agent('lead-claude', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Defines the exact bounded emitted-shape conformance slice and its acceptance gates.',
      retries: 1,
    })
    .agent('impl-codex', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements the emitted-shape conformance proof and validation loop.',
      retries: 1,
    })
    .agent('review-codex', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'reviewer',
      role: 'Reviews whether the emitted-shape proof actually closes the evidence gap.',
      retries: 1,
    })
    .step('read-context', {
      type: 'deterministic',
      command: [
        'echo "---OWNERSHIP BOUNDARY---"',
        'sed -n "1,260p" docs/canonical-file-schema-ownership-boundary.md || true',
        'echo "" && echo "---FIRST PROOF BOUNDARY---"',
        'sed -n "1,260p" docs/first-canonical-schema-proof-boundary.md || true',
        'echo "" && echo "---FIRST PROOF REVIEW---"',
        'sed -n "1,320p" docs/first-canonical-schema-proof-review-verdict.md || true',
        'echo "" && echo "---REMEDIATION REVIEW---"',
        'sed -n "1,320p" docs/first-canonical-schema-proof-remediation-review-verdict.md || true',
        'echo "" && echo "---CURRENT SCHEMA TESTS---"',
        'sed -n "1,260p" internal/schema/validate_test.go || true',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('define-boundary', {
      agent: 'lead-claude',
      dependsOn: ['read-context'],
      task: `Define the exact emitted-shape conformance proof needed to turn the current relayfile schema work into a real first canonical schema proof.\n\n{{steps.read-context.output}}\n\nRequirements:\n1. keep the proof bounded to one provider and one file type unless the context proves otherwise\n2. explicitly close the evidence gap identified in the remediation review: use captured emitted fixtures or direct producer transforms, not hand-authored inline payloads\n3. distinguish producer provenance clearly: real adapter-emitted shape, real CLI-derived mapped shape, or both\n4. require exact files, tests, provenance notes, and deterministic verification gates\n5. keep the slice mergeable and honest; if a true producer fixture cannot be obtained locally, the boundary must say so and define the strongest acceptable alternative without pretending it is final\n\nWrite:\n- docs/emitted-shape-canonical-conformance-boundary.md\n- docs/emitted-shape-canonical-conformance-checklist.md\n- docs/emitted-shape-canonical-conformance-plan.md\n\nEnd with RELAYFILE_EMITTED_SHAPE_CONFORMANCE_BOUNDARY_READY.`,
      verification: { type: 'file_exists', value: 'docs/emitted-shape-canonical-conformance-boundary.md' },
    })
    .step('implement-proof', {
      agent: 'impl-codex',
      dependsOn: ['define-boundary'],
      task: `Implement the emitted-shape canonical conformance proof.\n\nRead:\n- docs/emitted-shape-canonical-conformance-boundary.md\n- docs/emitted-shape-canonical-conformance-checklist.md\n- docs/emitted-shape-canonical-conformance-plan.md\n\nRequirements:\n1. close the evidentiary gap without widening scope unnecessarily\n2. use captured or directly produced fixtures from the real transformation path instead of reconstructing canonical payloads inline\n3. add deterministic tests and any supporting fixtures/provenance notes required by the boundary\n4. preserve the ownership rule: core relayfile owns the canonical file schema, producers conform to it\n5. use the 80-to-100 discipline and stop short of fake certainty\n\nEnd with RELAYFILE_EMITTED_SHAPE_CONFORMANCE_IMPLEMENTATION_READY.`,
      verification: { type: 'exit_code' },
    })
    .step('validate-proof', {
      type: 'deterministic',
      dependsOn: ['implement-proof'],
      command: [
        'go test ./internal/schema/... 2>&1 || true',
        'go build ./... 2>&1 || true',
        'git diff --stat -- internal/schema schemas docs 2>&1 || true',
      ].join(' && '),
      captureOutput: true,
      failOnError: false,
    })
    .step('review-proof', {
      agent: 'review-codex',
      dependsOn: ['validate-proof'],
      task: `Review the emitted-shape canonical conformance proof.\n\nRead:\n- docs/emitted-shape-canonical-conformance-boundary.md\n- docs/emitted-shape-canonical-conformance-checklist.md\n- docs/emitted-shape-canonical-conformance-plan.md\n- changed files\n- validation output:\n{{steps.validate-proof.output}}\n\nWrite:\n- docs/emitted-shape-canonical-conformance-review-verdict.md\n\nAssess:\n1. did the implementation actually replace synthetic inline evidence with emitted-shape evidence?\n2. is provenance explicit and believable?\n3. is this now strong enough to treat the underlying relayfile canonical schema proof as authoritative?\n4. what exact next step remains if not?\n\nEnd with RELAYFILE_EMITTED_SHAPE_CONFORMANCE_REVIEW_COMPLETE.`,
      verification: { type: 'file_exists', value: 'docs/emitted-shape-canonical-conformance-review-verdict.md' },
    })
    .step('verify-artifacts', {
      type: 'deterministic',
      dependsOn: ['review-proof'],
      command: [
        'test -f docs/emitted-shape-canonical-conformance-boundary.md',
        'test -f docs/emitted-shape-canonical-conformance-checklist.md',
        'test -f docs/emitted-shape-canonical-conformance-plan.md',
        'test -f docs/emitted-shape-canonical-conformance-review-verdict.md',
        'grep -q "RELAYFILE_EMITTED_SHAPE_CONFORMANCE_REVIEW_COMPLETE" docs/emitted-shape-canonical-conformance-review-verdict.md',
        'echo "RELAYFILE_EMITTED_SHAPE_CONFORMANCE_VERIFIED"',
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
