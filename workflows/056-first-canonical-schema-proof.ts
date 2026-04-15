import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('056-first-canonical-schema-proof')
    .description('Implement the first canonical schema proof in core relayfile: one service, one file type, one schema, one validation utility, and conformance tests.')
    .pattern('supervisor')
    .channel('wf-056-first-canonical-schema-proof')
    .maxConcurrency(4)
    .timeout(10_800_000)
    .agent('lead-claude', { cli: 'claude', model: ClaudeModels.OPUS, preset: 'analyst', role: 'Defines the exact bounded canonical schema proof slice.', retries: 1 })
    .agent('impl-codex', { cli: 'codex', model: CodexModels.GPT_5_4, role: 'Implements the first canonical schema proof.', retries: 1 })
    .agent('review-codex', { cli: 'codex', model: CodexModels.GPT_5_4, preset: 'reviewer', role: 'Reviews the first canonical schema proof for boundedness and correctness.', retries: 1 })
    .step('read-context', { type: 'deterministic', command: ['echo "---OWNERSHIP BOUNDARY---"','sed -n "1,260p" docs/canonical-file-schema-ownership-boundary.md || true','echo "" && echo "---OWNERSHIP PROOF DIRECTION---"','sed -n "1,260p" docs/canonical-file-schema-ownership-proof-direction.md || true','echo "" && echo "---OWNERSHIP REVIEW---"','sed -n "1,260p" docs/canonical-file-schema-ownership-review-verdict.md || true'].join(' && '), captureOutput: true, failOnError: true })
    .step('define-boundary', { agent: 'lead-claude', dependsOn: ['read-context'], task: `Define the exact first canonical schema proof in core relayfile.\n\n{{steps.read-context.output}}\n\nWrite:\n- docs/first-canonical-schema-proof-boundary.md\n- docs/first-canonical-schema-proof-checklist.md\n- docs/first-canonical-schema-proof-plan.md\n\nRequirements:\n1. one service, one file type, one schema\n2. validation utility and conformance tests\n3. no repo-wide schema rollout yet\n4. keep the slice bounded and mergeable\n\nEnd with RELAYFILE_FIRST_CANONICAL_SCHEMA_PROOF_BOUNDARY_READY.`, verification: { type: 'file_exists', value: 'docs/first-canonical-schema-proof-boundary.md' } })
    .step('implement-proof', { agent: 'impl-codex', dependsOn: ['define-boundary'], task: `Implement the first canonical schema proof in core relayfile.\n\nRead:\n- docs/first-canonical-schema-proof-boundary.md\n- docs/first-canonical-schema-proof-checklist.md\n- docs/first-canonical-schema-proof-plan.md\n\nRequirements:\n1. add one schema and one validation/conformance proof\n2. keep the slice bounded\n3. add deterministic tests and validation\n4. use the 80-to-100 discipline\n\nEnd with RELAYFILE_FIRST_CANONICAL_SCHEMA_PROOF_IMPLEMENTATION_READY.`, verification: { type: 'exit_code' } })
    .step('validate-proof', { type: 'deterministic', dependsOn: ['implement-proof'], command: ['npm test 2>&1 || true','npm run build 2>&1 || true'].join(' && '), captureOutput: true, failOnError: false })
    .step('review-proof', { agent: 'review-codex', dependsOn: ['validate-proof'], task: `Review the first canonical schema proof.\n\nRead:\n- docs/first-canonical-schema-proof-boundary.md\n- docs/first-canonical-schema-proof-checklist.md\n- docs/first-canonical-schema-proof-plan.md\n- changed files\n- validation output:\n{{steps.validate-proof.output}}\n\nWrite:\n- docs/first-canonical-schema-proof-review-verdict.md\n\nAssess:\n1. is the proof credible and bounded?\n2. does it keep ownership in core relayfile?\n3. what follows next?\n\nEnd with RELAYFILE_FIRST_CANONICAL_SCHEMA_PROOF_REVIEW_COMPLETE.`, verification: { type: 'file_exists', value: 'docs/first-canonical-schema-proof-review-verdict.md' } })
    .step('verify-artifacts', { type: 'deterministic', dependsOn: ['review-proof'], command: ['test -f docs/first-canonical-schema-proof-boundary.md','test -f docs/first-canonical-schema-proof-checklist.md','test -f docs/first-canonical-schema-proof-plan.md','test -f docs/first-canonical-schema-proof-review-verdict.md','grep -q "RELAYFILE_FIRST_CANONICAL_SCHEMA_PROOF_REVIEW_COMPLETE" docs/first-canonical-schema-proof-review-verdict.md','echo "RELAYFILE_FIRST_CANONICAL_SCHEMA_PROOF_VERIFIED"'].join(' && '), captureOutput: true, failOnError: true })
    .run({ cwd: process.cwd() });
  console.log(result.status);
}
main().catch((error) => { console.error(error); process.exit(1); });
