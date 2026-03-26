/**
 * relayfile-cloud-server.ts
 *
 * Creates a separate private repo (AgentWorkforce/relayfile-cloud) for the
 * Cloudflare Workers production server. The hosted server is closed-source
 * while the Go server, mount daemon, CLI, and SDK remain open-source.
 *
 * Repo structure:
 *   relayfile (public):
 *     - Go server (local dev/testing)
 *     - Go mount daemon
 *     - Go CLI
 *     - TS SDK
 *     - OpenAPI spec
 *     - Docs, landing page
 *
 *   relayfile-cloud (private):
 *     - CF Workers server (Hono + DO + R2 + D1)
 *     - Wrangler config
 *     - Deploy workflows
 *     - Production secrets management
 *     - Imports @relayfile/sdk for shared types
 *
 * The hosted server implements the same OpenAPI contract as the Go server.
 * Both must pass the same E2E test suite.
 *
 * Run: agent-relay run workflows/relayfile-cloud-server.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = process.env.RELAYFILE_PATH || '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const RELAYCAST = process.env.RELAYCAST_PATH || '/Users/khaliqgant/Projects/AgentWorkforce/relaycast';
const HOSTED = process.env.RELAYFILE_CLOUD_PATH || '/Users/khaliqgant/Projects/AgentWorkforce-relayfile-cloud';

async function main() {
const result = await workflow('relayfile-cloud-server')
  .description('Create private repo for closed-source CF Workers hosted server')
  .pattern('dag')
  .channel('wf-relayfile-cloud')
  .maxConcurrency(3)
  .timeout(3_600_000)

  .agent('architect', {
    cli: 'claude',
    preset: 'lead',
    role: 'Design repo structure, separation of concerns, shared types strategy',
    cwd: RELAYFILE,
  })
  .agent('server-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement the CF Workers server',
    cwd: HOSTED,
  })
  .agent('do-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement WorkspaceDO durable object',
    cwd: HOSTED,
  })
  .agent('infra-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Wrangler config, deploy workflows, secrets',
    cwd: HOSTED,
  })
  .agent('integrator', {
    cli: 'claude',
    preset: 'lead',
    role: 'Wire everything together, verify against OpenAPI contract',
    cwd: HOSTED,
  })

  // ── Phase 1: Read reference code ───────────────────────────────────

  .step('read-go-store', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/relayfile/store.go`,
    captureOutput: true,
  })

  .step('read-go-http', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/httpapi/server.go`,
    captureOutput: true,
  })

  .step('read-openapi', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/openapi/relayfile-v1.openapi.yaml`,
    captureOutput: true,
  })

  .step('read-relaycast-wrangler', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/wrangler.toml`,
    captureOutput: true,
  })

  .step('read-relaycast-worker', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/packages/server/src/worker.ts`,
    captureOutput: true,
  })

  .step('read-relaycast-channel-do', {
    type: 'deterministic',
    command: `head -200 ${RELAYCAST}/packages/server/src/durable-objects/channel.ts`,
    captureOutput: true,
  })

  .step('read-relaycast-deploy-workflow', {
    type: 'deterministic',
    command: `cat ${RELAYCAST}/.github/workflows/deploy.yml`,
    captureOutput: true,
  })

  .step('read-sdk-types', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/packages/relayfile-sdk/src/types.ts`,
    captureOutput: true,
  })

  // ── Phase 2: Create the private repo structure ─────────────────────

  .step('create-repo', {
    type: 'deterministic',
    command: `mkdir -p ${HOSTED} && cd ${HOSTED} && \
git init 2>/dev/null || true && \
echo "Repo initialized at ${HOSTED}"`,
    captureOutput: true,
  })

  .step('design-repo', {
    agent: 'architect',
    dependsOn: [
      'create-repo', 'read-go-store', 'read-go-http', 'read-openapi',
      'read-relaycast-wrangler', 'read-relaycast-worker', 'read-sdk-types',
    ],
    task: `Design the relayfile-cloud repo structure and scaffold it.

The goal: a private repo containing ONLY the CF Workers production server.
Everything open-source stays in the public relayfile repo.

Reference — relaycast's structure:
{{steps.read-relaycast-wrangler.output}}
{{steps.read-relaycast-worker.output}}

OpenAPI contract (the server must implement this):
{{steps.read-openapi.output}}

SDK types (import from @relayfile/sdk for shared types):
{{steps.read-sdk-types.output}}

Go store (the logic to port):
{{steps.read-go-store.output}}

Create these files at ${HOSTED}/:

1. package.json:
   - name: "relayfile-cloud"
   - private: true
   - dependencies: hono, @relayfile/sdk (for shared types)
   - devDependencies: @cloudflare/workers-types, wrangler, typescript

2. tsconfig.json

3. wrangler.toml — following relaycast's pattern:
   - name: relayfile-api
   - D1: relayfile
   - R2: relayfile-content
   - Queues: relayfile-envelopes, relayfile-writeback
   - DOs: WorkspaceDO
   - Staging and preview environments

4. src/env.ts — AppEnv with CF bindings

5. src/worker.ts — Hono app entry, export WorkspaceDO

6. src/types.ts — server-internal types (not in SDK)
   Import shared types from @relayfile/sdk where possible.

7. .gitignore: node_modules, dist, .wrangler

8. README.md:
   "# relayfile-cloud
   Private Cloudflare Workers server for relayfile.
   Implements the same API as the open-source Go server.

   ## Development
   npm install
   npm run dev     # wrangler dev
   npm run deploy  # wrangler deploy

   ## Contract
   The OpenAPI spec lives in the public repo: github.com/AgentWorkforce/relayfile/openapi/
   Both servers must pass the same E2E test suite."

Write all files. Create directories as needed.`,
    verification: { type: 'exit_code' },
  })

  .step('verify-scaffold', {
    type: 'deterministic',
    dependsOn: ['design-repo'],
    command: `cd ${HOSTED} && \
if [ -f package.json ] && [ -f wrangler.toml ] && [ -f src/worker.ts ] && [ -f src/env.ts ]; then \
  echo "Scaffold OK"; \
else \
  echo "Agent did not scaffold — creating minimal files" && \
  mkdir -p src && \
  echo '{"name":"relayfile-cloud","private":true,"scripts":{"dev":"wrangler dev","deploy":"wrangler deploy"},"dependencies":{"hono":"latest"},"devDependencies":{"@cloudflare/workers-types":"latest","wrangler":"latest","typescript":"latest"}}' > package.json && \
  echo 'name = "relayfile-api"\nmain = "src/worker.ts"\ncompatibility_date = "2024-01-01"\n\n[[d1_databases]]\nbinding = "DB"\ndatabase_name = "relayfile"\ndatabase_id = "local"\n\n[[r2_buckets]]\nbinding = "CONTENT"\nbucket_name = "relayfile-content"\n\n[durable_objects]\nbindings = [{name="WORKSPACE",class_name="WorkspaceDO"}]\n\n[[migrations]]\ntag = "v1"\nnew_classes = ["WorkspaceDO"]' > wrangler.toml && \
  echo 'export interface AppEnv { Bindings: { DB: D1Database; CONTENT: R2Bucket; WORKSPACE: DurableObjectNamespace } }' > src/env.ts && \
  echo 'import { Hono } from "hono"; import type { AppEnv } from "./env.js"; const app = new Hono<AppEnv>(); app.get("/health", c => c.json({status:"ok"})); export default app; export { WorkspaceDO } from "./durable-objects/workspace.js";' > src/worker.ts && \
  mkdir -p src/durable-objects && echo 'export class WorkspaceDO { constructor(private state: DurableObjectState) {} async fetch(req: Request) { return new Response("ok"); } }' > src/durable-objects/workspace.ts && \
  echo "Fallback scaffold created"; \
fi`,
    failOnError: true,
    captureOutput: true,
  })

  // ── Phase 3: Implement (parallel) ──────────────────────────────────

  .step('implement-workspace-do', {
    agent: 'do-dev',
    dependsOn: ['verify-scaffold', 'read-go-store', 'read-relaycast-channel-do'],
    task: `Implement WorkspaceDO at ${HOSTED}/src/durable-objects/workspace.ts.

This is the core — one DO per workspace, holding the file tree.

Go store logic to port:
{{steps.read-go-store.output}}

Relaycast DO pattern to follow:
{{steps.read-relaycast-channel-do.output}}

The WorkspaceDO must:

1. Use DO SQLite storage for file metadata:
   - files table: path, revision, content_type, content_ref (R2 key), size, encoding, updated_at, semantics_json
   - events table: event_id, type, path, revision, origin, provider, correlation_id, timestamp

2. Store file content in R2 (passed via env binding):
   - Key format: {workspaceId}/{path}@{revision}
   - DO stores content_ref pointing to R2

3. Methods (called via DO fetch):
   - listTree(path, depth, cursor)
   - readFile(path) — metadata from SQLite, content from R2
   - writeFile(path, ifMatch, content, contentType, encoding, semantics)
     - Check revision conflict via ifMatch
     - Increment revision counter
     - Store content in R2, metadata in SQLite
     - Emit event
   - deleteFile(path, ifMatch)
   - listEvents(provider?, cursor?, limit?)
   - queryFiles(options)
   - bulkWrite(files[]) — batch write without revision checks (for seeding)
   - exportWorkspace(format) — export all files

4. WebSocket hibernation for real-time event push

5. Alarm handler for writeback retry scheduling

Import shared types from @relayfile/sdk where they match.

Write complete implementation.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-routes', {
    agent: 'server-dev',
    dependsOn: ['verify-scaffold', 'read-go-http', 'read-openapi'],
    task: `Implement Hono routes at ${HOSTED}/src/routes/.

Go HTTP handlers to port:
{{steps.read-go-http.output}}

OpenAPI spec:
{{steps.read-openapi.output}}

Create route files matching the Go server's endpoints:
1. src/routes/fs.ts — tree, file CRUD, events, query, bulk, export, WebSocket
2. src/routes/sync.ts — sync status, ingress, dead-letter
3. src/routes/webhooks.ts — webhook ingestion
4. src/routes/ops.ts — operation tracking
5. src/routes/admin.ts — admin endpoints
6. src/routes/health.ts — GET /health

Each route:
- Extracts workspaceId from URL params
- Gets WorkspaceDO stub via env.WORKSPACE_DO.idFromName(workspaceId)
- Forwards request to DO
- Returns JSON matching the OpenAPI spec

Also create src/middleware/auth.ts — JWT verification (same as Go server's auth).

Write all files.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-deploy', {
    agent: 'infra-dev',
    dependsOn: ['verify-scaffold', 'read-relaycast-deploy-workflow'],
    task: `Create deployment infrastructure.

Relaycast deploy workflow:
{{steps.read-relaycast-deploy-workflow.output}}

Create:

1. ${HOSTED}/.github/workflows/deploy.yml:
   - Push to main → deploy to production
   - Push to staging branch → deploy to staging
   - PR → deploy to preview environment
   - Steps: install, typecheck, wrangler deploy
   - D1 migrations before deploy
   - Secrets: CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID

2. ${HOSTED}/.github/workflows/ci.yml:
   - PR trigger: typecheck, lint
   - Run E2E tests against wrangler dev (or Go server as reference)

3. ${HOSTED}/src/db/migrations/0001_init.sql:
   D1 migration for cross-workspace metadata:
   - workspace_stats table
   - dead_letters table

4. Update wrangler.toml with:
   - Staging environment
   - Preview environment
   - D1 migrations directory

Write all files.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 4: Wire together ─────────────────────────────────────────

  .step('integrate', {
    agent: 'integrator',
    dependsOn: ['implement-workspace-do', 'implement-routes', 'implement-deploy'],
    task: `Wire everything together in the hosted repo.

1. Update src/worker.ts:
   - Import and mount all routes
   - Export WorkspaceDO
   - Export queue consumers if any
   - Add CORS and error handling middleware

2. Install deps and typecheck:
   cd ${HOSTED} && npm install && npx tsc --noEmit

3. Fix any type errors.

4. Verify wrangler config is valid:
   npx wrangler deploy --dry-run 2>&1 | tail -20

5. Add a contract test: create ${HOSTED}/tests/contract.test.ts
   that imports the OpenAPI spec from the public repo and verifies
   all endpoints are implemented in the routes.

Fix all issues until it type-checks cleanly.`,
    verification: { type: 'exit_code' },
  })

  .step('typecheck', {
    type: 'deterministic',
    dependsOn: ['integrate'],
    command: `cd ${HOSTED} && npm install 2>&1 | tail -3 && npx tsc --noEmit 2>&1 | tail -10; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('fix-final', {
    agent: 'integrator',
    dependsOn: ['typecheck'],
    task: `Fix any remaining issues.

Typecheck:
{{steps.typecheck.output}}

If EXIT: 0, create the GitHub repo:
  gh repo create AgentWorkforce/relayfile-cloud --private --description "Hosted Cloudflare Workers server for relayfile"
  cd ${HOSTED} && git add -A && git commit -m "initial: CF Workers server for relayfile" && git remote add origin git@github.com:AgentWorkforce/relayfile-cloud.git && git push -u origin main

If there are errors, fix them first.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nHosted server workflow: ${result.status}`);
}

main().catch(console.error);
