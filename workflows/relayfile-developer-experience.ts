/**
 * relayfile-developer-experience.ts
 *
 * Makes relayfile usable by humans — not just agents.
 * Adds workspace CLI, auth flow, installable mount daemon,
 * and user-facing documentation.
 *
 * After this workflow, a developer can:
 *   relayfile login
 *   relayfile workspace create my-project
 *   relayfile mount my-project ./src
 *   # colleague on another machine:
 *   relayfile mount my-project ./src
 *   # both see the same files, edits sync in real-time
 *
 * Run: agent-relay run workflows/relayfile-developer-experience.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = '/Users/khaliqgant/Projects/Agent Workforce/relayfile';

async function main() {
const result = await workflow('relayfile-developer-experience')
  .description('Make relayfile usable by humans — CLI, auth, install, docs')
  .pattern('dag')
  .channel('wf-relayfile-dx')
  .maxConcurrency(3)
  .timeout(3_600_000)

  .agent('dx-lead', {
    cli: 'claude',
    preset: 'lead',
    role: 'Design the developer experience, CLI UX, auth flow',
    cwd: RELAYFILE,
  })
  .agent('cli-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement the relayfile CLI in Go',
    cwd: RELAYFILE,
  })
  .agent('docs-writer', {
    cli: 'codex',
    preset: 'worker',
    role: 'Write user-facing documentation and guides',
    cwd: RELAYFILE,
  })
  .agent('install-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Build install scripts, Homebrew formula, GitHub releases',
    cwd: RELAYFILE,
  })

  // ── Phase 1: Read existing code ────────────────────────────────────

  .step('read-mount-cmd', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/cmd/relayfile-mount/main.go`,
    captureOutput: true,
  })

  .step('read-server-cmd', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/cmd/relayfile/main.go`,
    captureOutput: true,
  })

  .step('read-auth', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/httpapi/auth.go`,
    captureOutput: true,
  })

  .step('read-token-script', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/scripts/generate-dev-token.sh`,
    captureOutput: true,
  })

  .step('read-readme', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/README.md`,
    captureOutput: true,
  })

  .step('read-openapi', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/openapi/relayfile-v1.openapi.yaml | head -100`,
    captureOutput: true,
  })

  // ── Phase 2: Design the CLI ────────────────────────────────────────

  .step('design-cli', {
    agent: 'dx-lead',
    dependsOn: ['read-mount-cmd', 'read-auth', 'read-token-script'],
    task: `Design the relayfile CLI UX.

Current mount command:
{{steps.read-mount-cmd.output}}

Current auth:
{{steps.read-auth.output}}

Token generation:
{{steps.read-token-script.output}}

Write a design doc at ${RELAYFILE}/docs/cli-design.md:

**Commands:**

1. relayfile login [--server URL]
   - Opens browser to OAuth flow (or accepts API key)
   - Stores credentials at ~/.relayfile/credentials.json
   - Validates by calling GET /health on the server
   - Default server: https://relayfile-api.agentworkforce.workers.dev

2. relayfile workspace create <name>
   - Creates a new workspace
   - Returns workspace ID
   - Stores in ~/.relayfile/workspaces.json

3. relayfile workspace list
   - Lists all workspaces you have access to

4. relayfile workspace delete <name>
   - Deletes a workspace (with confirmation)

5. relayfile mount <workspace> [local-dir]
   - Mounts a workspace to a local directory (default: current dir)
   - Starts relayfile-mount daemon in foreground (Ctrl+C to stop)
   - Shows sync activity in real-time
   - Auto-detects server URL from ~/.relayfile/credentials.json

6. relayfile seed <workspace> [dir]
   - Bulk uploads a local directory to a workspace
   - Uses bulk API for speed
   - Shows progress bar

7. relayfile export <workspace> [--format tar|json|patch] [--output file]
   - Downloads workspace contents

8. relayfile status <workspace>
   - Shows workspace stats: file count, last activity, connected agents

**Config file: ~/.relayfile/credentials.json**
{
  "server": "https://relayfile-api.agentworkforce.workers.dev",
  "token": "eyJ...",
  "refreshToken": "...",
  "expiresAt": "..."
}

**Auth flow options:**
- Option A: API key (simple, for agents and CI)
- Option B: OAuth via browser (for humans, like gh auth login)
- Option C: Token from environment variable RELAYFILE_TOKEN (for CI/CD)

Start with A and C. OAuth can come later.

Write the complete design doc.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 3: Implementation (parallel) ─────────────────────────────

  .step('implement-cli', {
    agent: 'cli-dev',
    dependsOn: ['design-cli', 'read-mount-cmd', 'read-server-cmd'],
    task: `Implement the relayfile CLI as a unified Go binary.

Design:
{{steps.design-cli.output}}

Current mount command:
{{steps.read-mount-cmd.output}}

The relayfile binary should be a single Go binary with subcommands.
Merge the existing relayfile-mount functionality as the "mount" subcommand.

Create ${RELAYFILE}/cmd/relayfile-cli/main.go:

Use Go's flag package or a lightweight CLI library (cobra is fine if already a dep, otherwise use flag with manual subcommand routing).

Subcommands:

1. relayfile login --server URL --token TOKEN
   - If --token provided, store it directly
   - If no --token, prompt for API key
   - Validate: GET {server}/health
   - Store: ~/.relayfile/credentials.json

2. relayfile workspace create NAME
   - Load credentials
   - POST to create workspace (or just mint a token for the workspace name)
   - For MVP: workspaces are created on-demand when you first write to them
   - Store workspace name in ~/.relayfile/workspaces.json

3. relayfile workspace list
   - Load credentials
   - GET /v1/admin/workspaces or list from local config

4. relayfile mount WORKSPACE [LOCAL_DIR]
   - Load credentials from ~/.relayfile/credentials.json
   - Default LOCAL_DIR to current directory
   - Reuse the existing mountsync.Syncer logic from internal/mountsync
   - Run in foreground, show sync activity
   - Handle Ctrl+C gracefully

5. relayfile seed WORKSPACE [DIR]
   - Load credentials
   - Walk DIR, read all files
   - POST /v1/workspaces/{ws}/fs/bulk with file contents
   - Show progress: "Seeding 142/250 files..."

6. relayfile export WORKSPACE --format FORMAT --output FILE
   - GET /v1/workspaces/{ws}/fs/export?format=FORMAT
   - Write to FILE or stdout

7. relayfile status WORKSPACE
   - GET /v1/workspaces/{ws}/sync/status
   - Print file count, last activity

Also update the Makefile/build:
- go build -o relayfile ./cmd/relayfile-cli
- Keep the server binary as: go build -o relayfile-server ./cmd/relayfile
- Keep the mount-only binary as: go build -o relayfile-mount ./cmd/relayfile-mount

Write all files to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-install', {
    agent: 'install-dev',
    dependsOn: ['implement-cli'],
    task: `Create installation scripts and release automation.

Create:

1. ${RELAYFILE}/scripts/install.sh
   - curl-based installer: curl -fsSL https://relayfile.dev/install.sh | sh
   - Detects OS (darwin/linux) and arch (amd64/arm64)
   - Downloads the latest binary from GitHub releases
   - Installs to /usr/local/bin/relayfile
   - Verifies checksum

2. ${RELAYFILE}/Makefile
   - build: builds all three binaries (relayfile-cli, relayfile-server, relayfile-mount)
   - build-all: cross-compile for darwin/linux x amd64/arm64
   - install: builds and copies to /usr/local/bin
   - test: runs go test ./...
   - release: creates release artifacts with checksums

3. ${RELAYFILE}/.github/workflows/release.yml
   - Triggered on tag push (v*)
   - Cross-compile for darwin/linux x amd64/arm64
   - Create GitHub release with binaries + checksums
   - Build and push Docker image

4. ${RELAYFILE}/Formula/relayfile.rb (Homebrew formula template)
   - Install the CLI binary
   - Depends on: nothing (static Go binary)

Write all files to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('write-docs', {
    agent: 'docs-writer',
    dependsOn: ['design-cli', 'read-readme', 'read-openapi'],
    task: `Write user-facing documentation.

CLI design:
{{steps.design-cli.output}}

Current README:
{{steps.read-readme.output}}

Create/update:

1. Update ${RELAYFILE}/README.md:
   - Add "Quick Start" section at the top:
     Install: curl -fsSL https://relayfile.dev/install.sh | sh
     Login: relayfile login --server http://localhost:9090 --token dev-token
     Seed: relayfile seed my-project ./src
     Mount: relayfile mount my-project ./src
   - Keep existing technical content below
   - Add "Collaborate" section showing two-machine setup

2. Create ${RELAYFILE}/docs/guides/getting-started.md:
   - Prerequisites (Go for local server, or use hosted)
   - Start the server locally: go run ./cmd/relayfile
   - Login and create a workspace
   - Seed files from a project
   - Mount on two machines
   - Watch files sync

3. Create ${RELAYFILE}/docs/guides/cloud-integration.md:
   - How relayfile integrates with Agent Relay cloud workflows
   - Automatic workspace creation per workflow run
   - relayfile-mount in sandbox snapshots
   - How agents share files via relayfile

4. Create ${RELAYFILE}/docs/guides/collaboration.md:
   - Two humans collaborating:
     Machine A: relayfile mount project-x ./src
     Machine B: relayfile mount project-x ./src
     Edit a file on A → appears on B in ~1-2 seconds
   - Human + agent collaboration:
     Human mounts locally, agent mounts in cloud sandbox
     Both see the same files
   - Conflict resolution: what happens when both edit the same file

5. Create ${RELAYFILE}/docs/api-reference.md:
   - Document all REST endpoints with curl examples
   - Group by: Filesystem, Sync, Webhooks, Operations, Admin
   - Include auth header examples

Write all docs to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 4: SDK publish prep ──────────────────────────────────────

  .step('prepare-sdk-publish', {
    agent: 'cli-dev',
    dependsOn: ['implement-cli'],
    task: `Prepare the TypeScript SDK for npm publishing.

Read the current SDK: cat ${RELAYFILE}/packages/relayfile-sdk/package.json

Update ${RELAYFILE}/packages/relayfile-sdk/package.json:
- name: "@relayfile/sdk" (or "relayfile-sdk")
- version: "0.1.0"
- main: "dist/index.js"
- types: "dist/index.d.ts"
- files: ["dist"]
- scripts:
    build: "tsc"
    prepublishOnly: "npm run build"
- repository, license, description fields

Create ${RELAYFILE}/packages/relayfile-sdk/tsconfig.json:
- target: ES2022
- module: ESNext
- moduleResolution: bundler
- declaration: true
- outDir: dist
- rootDir: src

Verify it builds: cd packages/relayfile-sdk && npm run build

Write changes to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 5: Verify everything ─────────────────────────────────────

  .step('verify-all', {
    type: 'deterministic',
    dependsOn: ['implement-cli', 'implement-install', 'write-docs', 'prepare-sdk-publish'],
    command: `cd ${RELAYFILE} && echo "=== CLI builds ===" && \
go build ./cmd/relayfile-cli 2>&1 && echo "CLI: OK" || echo "CLI: FAIL" && \
echo "" && echo "=== Docs ===" && \
for f in docs/guides/getting-started.md docs/guides/collaboration.md docs/guides/cloud-integration.md docs/api-reference.md docs/cli-design.md; do
  [ -f "$f" ] && echo "$f: OK" || echo "$f: MISSING"
done && \
echo "" && echo "=== Install script ===" && \
[ -f scripts/install.sh ] && echo "install.sh: OK" || echo "install.sh: MISSING" && \
echo "" && echo "=== SDK builds ===" && \
cd packages/relayfile-sdk && npm install 2>&1 | tail -1 && npx tsc --noEmit 2>&1 | tail -3; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('fix-issues', {
    agent: 'dx-lead',
    dependsOn: ['verify-all'],
    task: `Fix any remaining issues.

Results:
{{steps.verify-all.output}}

Fix any build failures or missing files.
Review the docs for clarity and completeness.
Verify the README quick start actually works by reading the commands.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nDeveloper experience workflow: ${result.status}`);
}

main().catch(console.error);
