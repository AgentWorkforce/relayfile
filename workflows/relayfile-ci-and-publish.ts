/**
 * relayfile-ci-and-publish.ts
 *
 * Sets up GitHub Actions CI/CD for relayfile:
 *   - CI: Go tests + TS SDK typecheck + E2E test on every PR
 *   - npm publish with provenance for the TS SDK (workflow_dispatch)
 *   - Go binary releases (cross-compile, GitHub Releases, checksums)
 *   - CF Workers deployment (wrangler deploy on main push)
 *
 * Models after relaycast's publish-npm.yml for provenance + versioning.
 *
 * Run: agent-relay run workflows/relayfile-ci-and-publish.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = process.env.RELAYFILE_PATH || '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const RELAYCAST = process.env.RELAYCAST_PATH || '/Users/khaliqgant/Projects/AgentWorkforce/relaycast';

async function main() {
const result = await workflow('relayfile-ci-and-publish')
  .description('Set up CI/CD: Go tests, npm publish with provenance, Go binary releases, CF Workers deploy')
  .pattern('dag')
  .channel('wf-relayfile-ci')
  .maxConcurrency(3)
  .timeout(3_600_000)

  .agent('ci-lead', {
    cli: 'claude',
    preset: 'lead',
    role: 'Design CI/CD pipeline, review workflows',
    cwd: RELAYFILE,
  })
  .agent('ci-implementer', {
    cli: 'codex',
    preset: 'worker',
    role: 'Write GitHub Actions workflow files',
    cwd: RELAYFILE,
  })
  .agent('sdk-implementer', {
    cli: 'codex',
    preset: 'worker',
    role: 'Prepare SDK package for npm publishing',
    cwd: RELAYFILE,
  })

  // ── Phase 1: Read reference patterns ───────────────────────────────

  .step('read-relaycast-ci', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/.github/workflows/ci.yml`,
    captureOutput: true,
  })

  .step('read-relaycast-publish', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/.github/workflows/publish-npm.yml`,
    captureOutput: true,
  })

  .step('read-relaycast-deploy', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/.github/workflows/deploy.yml`,
    captureOutput: true,
  })

  .step('read-relaycast-binary-release', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/.github/workflows/local-binary-release.yml`,
    captureOutput: true,
  })

  .step('read-sdk-package', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/packages/relayfile-sdk/package.json 2>/dev/null || echo no package.json""`,
    captureOutput: true,
  })

  .step('read-existing-github', {
    type: 'deterministic',
    command: `find ${RELAYFILE}/.github -type f 2>/dev/null | sort || echo "no .github dir"`,
    captureOutput: true,
  })

  // ── Phase 2: Design the pipeline ───────────────────────────────────

  .step('design-ci', {
    agent: 'ci-lead',
    dependsOn: [
      'read-relaycast-ci', 'read-relaycast-publish', 'read-relaycast-deploy',
      'read-relaycast-binary-release', 'read-existing-github',
    ],
    task: `Design the CI/CD pipeline for relayfile. We need 4 GitHub Actions workflows.

Reference — relaycast CI:
{{steps.read-relaycast-ci.output}}

Reference — relaycast npm publish (THIS IS THE KEY PATTERN for provenance):
{{steps.read-relaycast-publish.output}}

Reference — relaycast deploy:
{{steps.read-relaycast-deploy.output}}

Reference — relaycast binary release:
{{steps.read-relaycast-binary-release.output}}

Write a design doc at ${RELAYFILE}/docs/ci-cd-design.md covering:

1. **ci.yml** — runs on every PR and push to main:
   - Go tests: go test ./... (all packages)
   - Go build: go build ./cmd/relayfile ./cmd/relayfile-mount ./cmd/relayfile-cli
   - TS SDK: cd packages/relayfile-sdk && npm ci && npm run build && npx tsc --noEmit
   - E2E test: start Go server, run scripts/e2e.ts --ci
   - CF Workers typecheck: cd packages/server && npx tsc --noEmit (if it exists)

2. **publish-npm.yml** — workflow_dispatch (manual trigger like relaycast):
   - MUST use provenance: npm publish --access public --provenance
   - MUST have permissions: contents: write, id-token: write (OIDC for provenance)
   - Version bump: patch/minor/major/prerelease
   - Dry run option
   - NPM dist-tag: latest/next/beta/alpha
   - Build SDK, run tests, publish to npm as @relayfile/sdk
   - Create git tag + GitHub Release
   - CRITICAL: registry-url must be https://registry.npmjs.org" in setup-node
   - CRITICAL: NODE_AUTH_TOKEN secret must be set (npm automation token)

3. **release-binaries.yml** — triggered on tag push (v*):
   - Cross-compile Go binaries: darwin/linux x amd64/arm64
   - Three binaries: relayfile (server), relayfile-mount, relayfile-cli
   - Create checksums (sha256)
   - Upload to GitHub Release
   - Build + push Docker image to GHCR

4. **deploy-workers.yml** — triggered on push to main (if packages/server exists):
   - wrangler deploy for the CF Workers production server
   - D1 migrations: wrangler d1 migrations apply
   - Only runs if packages/server/ has changes

Write the complete design with all secrets/permissions needed.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 3: Implementation (parallel) ─────────────────────────────

  .step('implement-ci-workflow', {
    agent: 'ci-implementer',
    dependsOn: ['design-ci', 'read-relaycast-ci'],
    task: `Create the CI workflow.

Design:
{{steps.design-ci.output}}

Relaycast CI reference:
{{steps.read-relaycast-ci.output}}

Create ${RELAYFILE}/.github/workflows/ci.yml:

- Trigger: pull_request, push to main
- Jobs:
  1. go-test: setup Go 1.22, run go test ./...
  2. go-build: build all 3 binaries
  3. sdk-typecheck: setup Node 22, cd packages/relayfile-sdk, npm ci, npm run build, tsc --noEmit
  4. e2e: depends on go-build, start server, run e2e.ts --ci
  5. workers-typecheck (conditional): if packages/server exists, tsc --noEmit

Use matrix for Go version if desired. Cache go modules and npm.

Write to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-publish-workflow', {
    agent: 'ci-implementer',
    dependsOn: ['design-ci', 'read-relaycast-publish'],
    task: `Create the npm publish workflow with provenance.

Design:
{{steps.design-ci.output}}

Relaycast publish reference (follow this pattern closely):
{{steps.read-relaycast-publish.output}}

Create ${RELAYFILE}/.github/workflows/publish-npm.yml:

Key requirements:
- workflow_dispatch with inputs: version (patch/minor/major/prerelease), custom_version, dry_run, tag
- permissions: contents: write, id-token: write
- concurrency group to prevent parallel publishes
- Steps:
  1. Checkout
  2. Setup Node 22 with registry-url: https://registry.npmjs.org"
  3. Install deps: cd packages/relayfile-sdk && npm ci
  4. Version bump: npm version {type} --no-git-tag-version
  5. Build: npm run build
  6. Test: npx tsc --noEmit
  7. Dry run check (if dry_run)
  8. Publish: npm publish --access public --provenance --tag {tag} --ignore-scripts
  9. Commit version bump + create git tag
  10. Create GitHub Release

The NODE_AUTH_TOKEN env var is auto-set by setup-node when registry-url is configured.
The repo needs an NPM_TOKEN secret (automation token from npmjs.com).

env for publish step:
  NODE_AUTH_TOKEN: \${{ secrets.NPM_TOKEN }}

Write to disk. This is the most important workflow — provenance is non-negotiable.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-release-workflow', {
    agent: 'ci-implementer',
    dependsOn: ['design-ci', 'read-relaycast-binary-release'],
    task: `Create the Go binary release workflow.

Design:
{{steps.design-ci.output}}

Reference:
{{steps.read-relaycast-binary-release.output}}

Create ${RELAYFILE}/.github/workflows/release-binaries.yml:

- Trigger: push tags v*
- Matrix: os (linux, darwin) x arch (amd64, arm64)
- Steps:
  1. Checkout
  2. Setup Go 1.22
  3. Cross-compile 3 binaries:
     CGO_ENABLED=0 GOOS={os} GOARCH={arch} go build -o relayfile-{os}-{arch} ./cmd/relayfile
     CGO_ENABLED=0 GOOS={os} GOARCH={arch} go build -o relayfile-mount-{os}-{arch} ./cmd/relayfile-mount
     CGO_ENABLED=0 GOOS={os} GOARCH={arch} go build -o relayfile-cli-{os}-{arch} ./cmd/relayfile-cli
  4. Generate SHA256 checksums
  5. Upload to GitHub Release (use softprops/action-gh-release)

Also add a Docker build job:
  - Build from Dockerfile
  - Push to ghcr.io/agentworkforce/relayfile:latest and :v{version}

Write to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('prepare-sdk-package', {
    agent: 'sdk-implementer',
    dependsOn: ['design-ci', 'read-sdk-package'],
    task: `Prepare the SDK package.json for npm publishing with provenance.

Current package.json:
{{steps.read-sdk-package.output}}

Update ${RELAYFILE}/packages/relayfile-sdk/package.json:

{
  name": "@relayfile/sdk",
  "version": "0.1.0",
  "description": "TypeScript SDK for relayfile — real-time filesystem for humans and agents",
  "main": "dist/index.js",
  "module": "dist/index.js",
  "types": "dist/index.d.ts",
  "type": "module",
  "files": ["dist", "README.md", "LICENSE"],
  "scripts": {
    "build": "tsc",
    "typecheck": "tsc --noEmit",
    "prepublishOnly": "npm run build"
  },
  "publishConfig": {
    "access": "public",
    "provenance": true
  },
  "repository": {
    "type": "git",
    "url": "https://github.com/AgentWorkforce/relayfile",
    "directory": "packages/relayfile-sdk"
  },
  "license": "MIT",
  "keywords": ["relayfile", "filesystem", "sync", "agent", "collaboration"],
  "engines": { "node": ">=18" }
}

Also create/update ${RELAYFILE}/packages/relayfile-sdk/tsconfig.json:
{
  compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true,
    "outDir": "dist",
    "rootDir": "src",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true
  },
  "include": ["src"]
}

Create ${RELAYFILE}/packages/relayfile-sdk/README.md with:
- Package name + description
- Install: npm install @relayfile/sdk
- Quick example: create client, list tree, read/write file
- Link to full docs

Verify it builds: cd packages/relayfile-sdk && npm run build

Write all files to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 4: Verify ───────────────────────────────────────────────

  .step('verify-all', {
    type: 'deterministic',
    dependsOn: [
      'implement-ci-workflow', 'implement-publish-workflow',
      'implement-release-workflow', 'prepare-sdk-package',
    ],
    command: `cd ${RELAYFILE} && echo "=== GitHub Actions ===" && \
find .github/workflows -name "*.yml" | sort && \
echo "" && echo "=== Provenance check ===" && \
grep -c "provenance" .github/workflows/publish-npm.yml && \
grep -c "id-token: write" .github/workflows/publish-npm.yml && \
echo "" && echo "=== SDK builds ===" && \
cd packages/relayfile-sdk && npm install 2>&1 | tail -1 && npm run build 2>&1 | tail -3; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('fix-issues', {
    agent: 'ci-lead',
    dependsOn: ['verify-all'],
    task: `Review and fix any issues.

Results:
{{steps.verify-all.output}}

Verify:
1. All 4 workflow files exist
2. publish-npm.yml has provenance enabled (--provenance flag AND id-token: write permission)
3. SDK builds cleanly
4. Workflow YAML is valid (no syntax errors)

Fix any issues found.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nCI/CD workflow: ${result.status}`);
}

main().catch(console.error);
