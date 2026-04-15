import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('055-canonical-file-schema-ownership-boundary')
    .description('Define how core relayfile should formally own canonical file schemas and how adapters/relayfile-cli should conform to them.')
    .pattern('supervisor')
    .channel('wf-055-canonical-file-schema-ownership-boundary')
    .maxConcurrency(4)
    .timeout(8_400_000)
    .agent('lead-claude', { cli: 'claude', model: ClaudeModels.OPUS, preset: 'analyst', role: 'Defines canonical schema ownership in core relayfile.', retries: 1 })
    .agent('review-codex', { cli: 'codex', model: CodexModels.GPT_5_4, preset: 'reviewer', role: 'Reviews the core relayfile canonical-schema ownership boundary.', retries: 1 })
    .step('read-context', { type: 'deterministic', command: ['echo "---RELAYFILE README---"','sed -n "1,260p" README.md || true','echo "" && echo "---RELAYFILE-CLI CANONICAL BOUNDARY---"','sed -n "1,260p" ../relayfile-cli/docs/canonical-file-schema-boundary.md || true','echo "" && echo "---RELAYFILE-CLI BRIDGE BOUNDARY---"','sed -n "1,260p" ../relayfile-cli/docs/relayfile-bridge-boundary.md || true'].join(' && '), captureOutput: true, failOnError: true })
    .step('define-boundary', { agent: 'lead-claude', dependsOn: ['read-context'], task: `Define how core relayfile should own canonical file schemas.\n\n{{steps.read-context.output}}\n\nWrite:\n- docs/canonical-file-schema-ownership-boundary.md\n- docs/canonical-file-schema-ownership-proof-direction.md\n- docs/canonical-file-schema-ownership-review-verdict.md\n\nRequirements:\n1. make core relayfile the canonical schema authority\n2. clarify how adapters and relayfile-cli conform without owning the schemas\n3. keep the slice bounded and honest\n\nEnd with RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_BOUNDARY_READY.`, verification: { type: 'file_exists', value: 'docs/canonical-file-schema-ownership-boundary.md' } })
    .step('review-boundary', { agent: 'review-codex', dependsOn: ['define-boundary'], task: `Review the canonical schema ownership boundary.\n\nRead:\n- docs/canonical-file-schema-ownership-boundary.md\n- docs/canonical-file-schema-ownership-proof-direction.md\n- docs/canonical-file-schema-ownership-review-verdict.md\n\nAssess:\n1. is ownership clearly in core relayfile?\n2. are adapters and relayfile-cli placed correctly?\n3. is the next proof direction sensible?\n\nEnd with RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_REVIEW_COMPLETE.`, verification: { type: 'file_exists', value: 'docs/canonical-file-schema-ownership-review-verdict.md' } })
    .step('verify-artifacts', { type: 'deterministic', dependsOn: ['review-boundary'], command: ['test -f docs/canonical-file-schema-ownership-boundary.md','test -f docs/canonical-file-schema-ownership-proof-direction.md','test -f docs/canonical-file-schema-ownership-review-verdict.md','grep -q "RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_REVIEW_COMPLETE" docs/canonical-file-schema-ownership-review-verdict.md','echo "RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_VERIFIED"'].join(' && '), captureOutput: true, failOnError: true })
    .run({ cwd: process.cwd() });
  console.log(result.status);
}
main().catch((error) => { console.error(error); process.exit(1); });
