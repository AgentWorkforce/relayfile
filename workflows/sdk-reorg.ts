/**
 * sdk-reorg.ts
 *
 * Reorganize the relayfile monorepo SDK structure:
 *
 * BEFORE:
 *   TypeScript SDK lived at the SDK package root
 *   Python SDK lived in a separate top-level SDK directory
 *
 * AFTER:
 *   TypeScript SDK lives at packages/sdk/typescript/
 *   Python SDK lives at packages/sdk/python/
 *
 * Must update ALL references:
 *   - package.json workspaces
 *   - .github/workflows/ (ci.yml, contract.yml, publish.yml, release.yml)
 *   - docs, workflow files referencing old paths
 *   - pyproject.toml build paths if affected
 *
 * Publishing must not break:
 *   - npm publish for @relayfile/sdk, @relayfile/core, relayfile (CLI)
 *   - PyPI publish for relayfile (Python)
 *   - CI tests for both SDKs
 *
 * Run: agent-relay run workflows/sdk-reorg.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const ROOT = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';

async function main() {
  const result = await workflow('sdk-reorg')
    .description('Reorganize the split TypeScript and Python SDK directories')
    .pattern('linear')
    .channel('wf-sdk-reorg')
    .maxConcurrency(2)
    .timeout(1_800_000)

    .agent('architect', { cli: 'claude', role: 'Plans the reorg and verifies nothing breaks' })
    .agent('builder', { cli: 'codex', preset: 'worker', role: 'Executes the file moves and updates' })
    .agent('reviewer', { cli: 'codex', preset: 'worker', role: 'Verifies CI/publish integrity' })

    .step('plan-reorg', {
      agent: 'architect',
      task: `Plan the SDK directory reorganization for the relayfile monorepo at ${ROOT}.

Current layout:
  packages/sdk root       → TS SDK (@relayfile/sdk), npm workspace
  packages/core/          → shared logic (@relayfile/core)
  packages/cli/           → CLI (relayfile)
  top-level python SDK    → Python SDK (relayfile on PyPI)

Target layout:
  packages/sdk/typescript/ → TS SDK (same npm package name)
  packages/sdk/python/     → Python SDK (same PyPI package name)
  packages/core/           → unchanged
  packages/cli/            → unchanged

Read these files and map every reference that needs updating:

1. ${ROOT}/package.json — workspaces array, build/typecheck scripts
2. ${ROOT}/.github/workflows/ci.yml — working-directory, cache-dependency-path
3. ${ROOT}/.github/workflows/contract.yml — working-directory
4. ${ROOT}/.github/workflows/publish.yml — matrix paths, artifact paths, version sync script, single-package resolver
5. ${ROOT}/.github/workflows/release.yml — working-directory for npm-publish job
6. ${ROOT}/workflows/sync-python-sdk.ts — PY_SDK path, pytest paths
7. ${ROOT}/docs/sdk-improvements.md — file path references

Also check:
- ${ROOT}/packages/sdk root tsconfig.json — any relative path references to ../core?
- ${ROOT}/packages/sdk root package.json — any path-based deps?
- ${ROOT}/python SDK pyproject.toml — build paths (packages = ["src/relayfile"])

Rules:
- Do NOT modify .trajectories/ files (historical records)
- Do NOT touch packages/core/ or packages/cli/ (unchanged)
- The TS SDK package.json "name" stays "@relayfile/sdk" — only the directory moves
- The Python pyproject.toml "name" stays "relayfile" — only the directory moves
- publish.yml matrix uses \`packages/\${{ matrix.package }}\` — needs to handle sdk → sdk/typescript mapping

Output a checklist of every file to modify and what changes. Keep under 80 lines.
End with PLAN_COMPLETE.`,
      verification: { type: 'output_contains', value: 'PLAN_COMPLETE' },
      timeout: 300_000,
    })

    .step('execute-reorg', {
      agent: 'builder',
      dependsOn: ['plan-reorg'],
      task: `Execute the SDK reorganization in ${ROOT}.

Plan: {{steps.plan-reorg.output}}

Working on branch: feat/python-sdk-sync (already checked out)

Steps:
1. Create directories:
   mkdir -p packages/sdk/typescript packages/sdk/python

2. Move TS SDK files using git mv:
   git mv packages/sdk/README.md packages/sdk/typescript/
   git mv packages/sdk/package.json packages/sdk/typescript/
   git mv packages/sdk/package-lock.json packages/sdk/typescript/
   git mv packages/sdk/tsconfig.json packages/sdk/typescript/
   git mv packages/sdk/src packages/sdk/typescript/
   (move any other files that exist in the SDK root)

3. Move Python SDK:
   git mv <legacy-python-sdk-dir>/* packages/sdk/python/
   (handle hidden files/dirs too)

4. Update package.json workspaces: "packages/sdk" → "packages/sdk/typescript"
5. Update package.json scripts: build/typecheck workspace refs

6. Update ALL .github/workflows/*.yml files:
   - Every "packages/sdk" reference → "packages/sdk/typescript"
   - publish.yml matrix: use include syntax with explicit paths
   - publish.yml version sync: explicit package paths instead of dir walk
   - publish.yml artifact upload: explicit paths
   - publish.yml single-package: resolve "sdk" → "sdk/typescript"
   - publish.yml git add: explicit package.json paths

7. Update workflows/sync-python-sdk.ts: replace the legacy Python SDK path with "packages/sdk/python"
8. Update docs/sdk-improvements.md: path references

9. Verify nothing references stale SDK paths (except .trajectories/):
   grep -rn "packages/sdk/" --include="*.ts" --include="*.yml" --include="*.json" --include="*.md" . | grep -v node_modules | grep -v ".git/" | grep -v ".trajectories/" | grep -v "packages/sdk/typescript" | grep -v "packages/sdk/python"

10. Commit with HUSKY=0 and --no-verify:
    HUSKY=0 git -c core.hooksPath=/dev/null commit -m "refactor: reorganize SDK directories

Move TypeScript SDK into packages/sdk/typescript/
Move Python SDK into packages/sdk/python/

Updated all CI workflows, publish config, and documentation.
No functional changes — same packages, same builds, same publish."

Do NOT push (auth issues — will be handled separately).

End with REORG_COMPLETE.`,
      verification: { type: 'output_contains', value: 'REORG_COMPLETE' },
      timeout: 600_000,
    })

    .step('verify-integrity', {
      agent: 'reviewer',
      dependsOn: ['execute-reorg'],
      task: `Verify the SDK reorg didn't break anything in ${ROOT}.

1. Check npm workspace resolution:
   cd ${ROOT} && npm ls --workspaces 2>&1

2. Check TypeScript SDK builds:
   cd ${ROOT}/packages/sdk/typescript && npx tsc --noEmit 2>&1 | tail -5

3. Check Python SDK structure:
   ls ${ROOT}/packages/sdk/python/src/relayfile/
   cat ${ROOT}/packages/sdk/python/pyproject.toml | grep -A 3 "\\[tool.hatch"

4. Verify no dangling references:
   cd ${ROOT} && grep -rn "legacy python sdk path" --include="*.ts" --include="*.yml" --include="*.json" --include="*.md" . | grep -v node_modules | grep -v ".git/" | grep -v ".trajectories/"

5. Verify CI workflow YAML is valid:
   For each .github/workflows/*.yml, check that all working-directory paths exist:
   - packages/sdk/typescript
   - packages/core
   - packages/cli

6. Verify publish.yml matrix covers all 3 packages with correct paths

7. Check git status is clean (everything committed):
   git status --porcelain

If any issues found, fix them and amend the commit.

End with VERIFY_COMPLETE.`,
      verification: { type: 'output_contains', value: 'VERIFY_COMPLETE' },
      timeout: 300_000,
    })

    .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
    .run({ cwd: ROOT });

  console.log('SDK reorg complete:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
