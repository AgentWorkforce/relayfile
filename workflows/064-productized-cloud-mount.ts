/**
 * 064-productized-cloud-mount.ts
 *
 * Close the remaining product gaps for the Relayfile "just type relayfile"
 * experience: Cloud login, many integrations, background synced mount, durable
 * token refresh, agent-facing VFS skill, writeback safety, and E2E proof.
 *
 * The default product posture is a local synced mirror, not a kernel FUSE mount.
 * FUSE remains an optional mode. The shipped happy path should feel like a
 * local folder attached to remote integrations while preserving low context for
 * agents and clear observability for humans.
 *
 * Run from the relayfile repo root:
 *   agent-relay run workflows/064-productized-cloud-mount.ts
 */
import path from 'node:path';
import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

const RELAYFILE_ROOT = process.cwd();
const CLOUD_ROOT = path.resolve(RELAYFILE_ROOT, '../cloud');
const RELAYFILE_BRANCH = 'codex/productized-cloud-mount';
const CLOUD_BRANCH = 'codex/productized-relayfile-integrations';

async function main() {
  const result = await workflow('064-productized-cloud-mount')
    .description(
      'Productize Relayfile Cloud onboarding so a user can run relayfile, connect one or more Nango-backed integrations, and leave agents with a durable low-context synced VFS mount.',
    )
    .pattern('dag')
    .channel('wf-064-productized-cloud-mount')
    .maxConcurrency(5)
    .timeout(14_400_000)

    .agent('product-architect', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Defines the product acceptance contract, chooses v1 tradeoffs, and keeps the workflow focused on the low-friction user vision.',
      retries: 1,
    })
    .agent('cli-daemon-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'worker',
      role: 'Implements CLI setup refinements, background daemon/service lifecycle, local config, workspace token refresh, and status commands in relayfile.',
      retries: 2,
    })
    .agent('mount-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'worker',
      role: 'Hardens the local synced mirror: conflict handling, stale markers, status files, writeback visibility, and optional FUSE boundaries.',
      retries: 2,
    })
    .agent('cloud-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'worker',
      role: 'Implements cloud-side provider catalog, Nango config-key resolution, initial sync readiness, multi-integration connection, and refresh/rejoin support in ../cloud.',
      retries: 2,
    })
    .agent('agent-skill-docs', {
      cli: 'claude',
      model: ClaudeModels.SONNET,
      preset: 'worker',
      role: 'Writes the agent-facing Relayfile VFS skill and concise human docs for setup, path conventions, status, writeback, and safe editing.',
      retries: 1,
    })
    .agent('e2e-proof', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'worker',
      role: 'Builds deterministic mocks and packaged E2E proof for setup, multi-provider connect, daemon sync, refresh, writeback, and recovery.',
      retries: 2,
    })
    .agent('reviewer', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'reviewer',
      role: 'Reviews the final implementation against the product contract, evidence, security boundaries, and user experience.',
      retries: 1,
    })

    .step('preflight', {
      type: 'deterministic',
      command: [
        'set -e',
        `test -d ${shell(RELAYFILE_ROOT)}`,
        `test -d ${shell(CLOUD_ROOT)}`,
        `echo "relayfile root: ${RELAYFILE_ROOT}"`,
        `echo "cloud root: ${CLOUD_ROOT}"`,
        '',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'RELAYFILE_CURRENT=$(git rev-parse --abbrev-ref HEAD)',
        'echo "relayfile branch: $RELAYFILE_CURRENT"',
        'if [ "$RELAYFILE_CURRENT" = "main" ]; then',
        `  git checkout -b ${shell(RELAYFILE_BRANCH)}`,
        `  echo "created relayfile branch ${RELAYFILE_BRANCH}"`,
        'else',
        '  echo "using existing relayfile branch $RELAYFILE_CURRENT"',
        'fi',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: relayfile staging area is dirty; unstage before running this workflow"',
        '  git diff --cached --stat',
        '  exit 1',
        'fi',
        'node --version',
        'npm --version',
        'go version',
        '',
        `cd ${shell(CLOUD_ROOT)}`,
        'CLOUD_CURRENT=$(git rev-parse --abbrev-ref HEAD)',
        'echo "cloud branch: $CLOUD_CURRENT"',
        'if [ "$CLOUD_CURRENT" = "main" ]; then',
        `  git checkout -b ${shell(CLOUD_BRANCH)}`,
        `  echo "created cloud branch ${CLOUD_BRANCH}"`,
        'else',
        '  echo "using existing cloud branch $CLOUD_CURRENT"',
        'fi',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: cloud staging area is dirty; unstage before running this workflow"',
        '  git diff --cached --stat',
        '  exit 1',
        'fi',
        'node --version',
        'npm --version',
        'echo PREFLIGHT_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('collect-relayfile-context', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'echo "=== guided CLI and config ==="',
        'sed -n "1,720p" cmd/relayfile-cli/main.go',
        'sed -n "1,260p" cmd/relayfile-cli/main_test.go',
        'echo',
        'echo "=== standalone mount command ==="',
        'sed -n "1,320p" cmd/relayfile-mount/main.go',
        'sed -n "1,220p" cmd/relayfile-mount/fuse_mount.go 2>/dev/null || true',
        'echo',
        'echo "=== mount sync internals ==="',
        'sed -n "1,520p" internal/mountsync/syncer.go',
        'sed -n "1,320p" internal/mountsync/watcher.go',
        'sed -n "1,260p" internal/mountsync/http_client_test.go',
        'echo',
        'echo "=== setup SDK and docs ==="',
        'sed -n "1,680p" packages/sdk/typescript/src/setup.ts',
        'sed -n "1,220p" packages/sdk/typescript/src/cloud-login.ts',
        'sed -n "1,160p" packages/sdk/typescript/src/setup-types.ts',
        'sed -n "1,180p" README.md',
        'sed -n "1,180p" docs/cli-design.md',
        'echo',
        'echo "=== workflow inventory ==="',
        'find workflows -maxdepth 1 -type f | sort',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('collect-cloud-context', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'echo "=== cloud routes and provider registry ==="',
        'find packages -path "*api/v1/workspaces*" -type f | sort | sed -n "1,260p"',
        'rg -n "cli/login|connect-session|connectionId|provider|Nango|provider_config|configKey|integration|initial sync|sync_queued|syncing|ready" packages tests 2>/dev/null | head -320 || true',
        'echo',
        'echo "=== likely setup route files ==="',
        'sed -n "1,260p" packages/web/app/api/v1/cli/login/route.ts 2>/dev/null || true',
        'sed -n "1,260p" packages/web/app/api/v1/workspaces/route.ts 2>/dev/null || true',
        'sed -n "1,260p" "packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts" 2>/dev/null || true',
        'sed -n "1,260p" "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts" 2>/dev/null || true',
        'sed -n "1,260p" "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts" 2>/dev/null || true',
        'echo',
        'echo "=== package scripts ==="',
        'cat package.json',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('define-product-contract', {
      agent: 'product-architect',
      dependsOn: ['collect-relayfile-context', 'collect-cloud-context'],
      task: [
        'Define the v1 product contract before implementation. Write docs/productized-cloud-mount-contract.md in the relayfile repo.',
        '',
        'Important product premise:',
        '- The default local attachment is a synced mirror, not a kernel FUSE mount.',
        '- This is acceptable for agents because they see ordinary files, but the product must be honest about sync state, conflicts, refresh, and writeback.',
        '- FUSE can remain optional and separately hardened.',
        '',
        'Relayfile context:',
        '{{steps.collect-relayfile-context.output}}',
        '',
        'Cloud context:',
        '{{steps.collect-cloud-context.output}}',
        '',
        'The contract must include:',
        '1. Exact first-run UX for `relayfile` and `relayfile setup`.',
        '2. Multi-integration UX for adding providers after first setup.',
        '3. Synced mirror limitations and user-visible mitigations.',
        '4. Required daemon/background behavior and platform assumptions.',
        '5. Token refresh and workspace rejoin semantics for long-lived mounts.',
        '6. Cloud provider catalog and Nango config-key source of truth.',
        '7. Initial sync/readiness semantics after OAuth.',
        '8. Writeback safety, schema validation, conflicts, and dead-letter visibility.',
        '9. Agent skill contract: mount discovery, path conventions, status, and safe edits.',
        '10. E2E acceptance tests with realistic mocked Cloud/Nango/Relayfile services.',
        '',
        'End the document with PRODUCTIZED_CLOUD_MOUNT_CONTRACT_READY.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/productized-cloud-mount-contract.md' },
    })

    .step('implement-cli-daemon-refresh', {
      agent: 'cli-daemon-impl',
      dependsOn: ['define-product-contract'],
      task: [
        'Implement the CLI/daemon/token-refresh slice in relayfile.',
        '',
        'Read docs/productized-cloud-mount-contract.md first.',
        '',
        'Own these areas:',
        '- cmd/relayfile-cli/**',
        '- packages/cli/** when npm wrapper changes are needed',
        '- docs/environment-variables.md and CLI docs for commands/env vars',
        '- tests directly covering CLI setup, connect, daemon, token refresh, and status UX',
        '',
        'Requirements:',
        '1. Keep `relayfile` as the low-friction guided setup entrypoint.',
        '2. Add or refine commands for background lifecycle: start/status/stop/logs or equivalent.',
        '3. Support adding multiple integrations after first setup without recreating the workspace.',
        '4. Persist Cloud tokens and refresh/rejoin workspace JWTs for long-lived mounts.',
        '5. Ensure the foreground synced mirror remains available for CI and simple use.',
        '6. Do not touch cloud repo files; coordinate through the documented contract.',
        '',
        'Run targeted tests after edits and end with CLI_DAEMON_REFRESH_READY plus the exact commands run.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    .step('implement-mount-safety-status', {
      agent: 'mount-impl',
      dependsOn: ['define-product-contract'],
      task: [
        'Implement the synced-mirror safety/status slice in relayfile.',
        '',
        'Read docs/productized-cloud-mount-contract.md first.',
        '',
        'Own these areas:',
        '- internal/mountsync/**',
        '- cmd/relayfile-mount/**',
        '- internal/mountfuse/** only for optional-mode boundaries',
        '- mount-related docs/tests/evidence',
        '',
        'Requirements:',
        '1. Make sync state visible locally, preferably under a reserved `.relayfile/` status area or equivalent CLI status command.',
        '2. Make stale/offline/conflict/writeback-pending states explicit and machine-readable.',
        '3. Preserve revision conflict safety; never silently clobber remote or local edits.',
        '4. Keep large/binary files and deleted files behavior documented and tested.',
        '5. Keep FUSE optional; do not make product success depend on kernel-level mount availability.',
        '',
        'Run targeted mountsync/mount tests after edits and end with MOUNT_SAFETY_STATUS_READY plus the exact commands run.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    .step('implement-cloud-provider-readiness', {
      agent: 'cloud-impl',
      dependsOn: ['define-product-contract'],
      task: [
        'Implement the Cloud/Nango/provider-readiness slice in ../cloud.',
        '',
        'Read relayfile docs/productized-cloud-mount-contract.md first.',
        '',
        'Own these areas in the cloud repo:',
        '- Cloud CLI login/token refresh endpoints used by relayfile setup',
        '- workspace create/join/rejoin behavior for long-lived mounts',
        '- provider catalog endpoint/source of truth for display name and Nango config key',
        '- connect-session/status routes',
        '- initial sync enqueue/readiness state after OAuth',
        '- tests for all of the above',
        '',
        'Requirements:',
        '1. The relayfile CLI must not hard-code Nango config keys.',
        '2. Status must distinguish OAuth connected, initial sync queued/running/complete/failed, and writeback health.',
        '3. Multi-provider connections must be idempotent and resumable.',
        '4. Relayfile workspace JWT refresh/rejoin must be explicit enough for long-lived local daemons.',
        '',
        'Run targeted cloud tests after edits and end with CLOUD_PROVIDER_READINESS_READY plus the exact commands run.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    .step('write-agent-skill-and-docs', {
      agent: 'agent-skill-docs',
      dependsOn: ['define-product-contract'],
      task: [
        'Write the agent-facing skill and human docs for the productized Relayfile VFS experience.',
        '',
        'Read docs/productized-cloud-mount-contract.md first.',
        '',
        'Own these areas:',
        '- docs/guides/**',
        '- docs/cli-design.md',
        '- README.md',
        '- a proposed skill file or skill draft under docs/ that can later be installed into .agents/skills',
        '',
        'Requirements:',
        '1. Keep the agent contract tiny: discover mount path, inspect status, read/write files, handle conflicts/writeback failures.',
        '2. Document the synced-mirror drawbacks honestly without making the product feel scary.',
        '3. Include path conventions for provider-backed files and reserved Relayfile metadata/status paths.',
        '4. Include guidance for low-context agents: prefer file operations over provider APIs.',
        '5. Include human commands for setup, connect another integration, status, stop/start daemon, and troubleshooting.',
        '',
        'End with AGENT_RELAYFILE_VFS_DOCS_READY.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    .step('build-product-e2e-proof', {
      agent: 'e2e-proof',
      dependsOn: [
        'implement-cli-daemon-refresh',
        'implement-mount-safety-status',
        'implement-cloud-provider-readiness',
        'write-agent-skill-and-docs',
      ],
      task: [
        'Build the deterministic end-to-end proof for the productized Relayfile setup.',
        '',
        'Read docs/productized-cloud-mount-contract.md and all changed files in relayfile and ../cloud.',
        '',
        'Requirements:',
        '1. Use realistic local mock servers for Cloud, Relayfile VFS, and Nango/webhook/writeback behavior where real services are not available.',
        '2. Prove first-run setup with at least two integrations.',
        '3. Prove daemon/synced mirror startup, remote-to-local sync, local-to-remote writeback, conflict visibility, and status reporting.',
        '4. Prove token refresh or workspace rejoin for a long-lived mount.',
        '5. Prove recovery after process restart using persisted config.',
        '6. Save final evidence under docs/evidence/productized-cloud-mount-final-evidence.md.',
        '7. Include the literal marker PRODUCTIZED_CLOUD_MOUNT_E2E_READY in that evidence file.',
        '',
        'Run the E2E proof until green and end with PRODUCTIZED_CLOUD_MOUNT_E2E_READY.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/evidence/productized-cloud-mount-final-evidence.md' },
    })

    .step('verify-relayfile', {
      type: 'deterministic',
      dependsOn: ['build-product-e2e-proof'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'go test ./...',
        'npm run typecheck',
        'test -f docs/productized-cloud-mount-contract.md',
        'test -f docs/evidence/productized-cloud-mount-final-evidence.md',
        'grep -q PRODUCTIZED_CLOUD_MOUNT_CONTRACT_READY docs/productized-cloud-mount-contract.md',
        'grep -q PRODUCTIZED_CLOUD_MOUNT_E2E_READY docs/evidence/productized-cloud-mount-final-evidence.md',
        'echo RELAYFILE_PRODUCTIZED_MOUNT_VERIFY_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('verify-cloud', {
      type: 'deterministic',
      dependsOn: ['build-product-e2e-proof'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npm test',
        'npm run typecheck',
        'echo CLOUD_PRODUCTIZED_MOUNT_VERIFY_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('review-productized-mount', {
      agent: 'reviewer',
      dependsOn: ['verify-relayfile', 'verify-cloud'],
      task: [
        'Review the final productized Relayfile Cloud mount implementation.',
        '',
        'Read:',
        '- relayfile docs/productized-cloud-mount-contract.md',
        '- relayfile docs/evidence/productized-cloud-mount-final-evidence.md',
        '- relayfile changed files',
        '- cloud changed files',
        '- verification output:',
        '{{steps.verify-relayfile.output}}',
        '{{steps.verify-cloud.output}}',
        '',
        'Write docs/productized-cloud-mount-review-verdict.md in the relayfile repo.',
        '',
        'Assess:',
        '1. Does the implementation satisfy the original product vision?',
        '2. Are synced-mirror drawbacks mitigated and visible?',
        '3. Is token refresh/rejoin trustworthy for long-lived mounts?',
        '4. Is Cloud/Nango provider readiness enough for many integrations?',
        '5. Is writeback safe enough for agents?',
        '6. Is the E2E proof truly end to end?',
        '7. What residual risks remain before public launch?',
        '',
        'End with PRODUCTIZED_CLOUD_MOUNT_REVIEW_COMPLETE.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/productized-cloud-mount-review-verdict.md' },
    })

    .step('final-artifact-gate', {
      type: 'deterministic',
      dependsOn: ['review-productized-mount'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'test -f docs/productized-cloud-mount-contract.md',
        'test -f docs/evidence/productized-cloud-mount-final-evidence.md',
        'test -f docs/productized-cloud-mount-review-verdict.md',
        'grep -q PRODUCTIZED_CLOUD_MOUNT_REVIEW_COMPLETE docs/productized-cloud-mount-review-verdict.md',
        'git diff --stat',
        'echo PRODUCTIZED_CLOUD_MOUNT_WORKFLOW_COMPLETE',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .run({ cwd: RELAYFILE_ROOT });

  console.log(result.status);
}

function shell(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
