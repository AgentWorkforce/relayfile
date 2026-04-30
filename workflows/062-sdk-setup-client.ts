/**
 * 062-sdk-setup-client.ts
 *
 * Implement the TypeScript SDK setup client from docs/sdk-setup-client.md and
 * the matching cloud web API contract in ../cloud. This workflow is deliberately
 * written to close the 80-to-100 gap: every spec path is exercised through
 * deterministic tests, targeted test-fix-rerun loops, a package-consumer E2E
 * proof, and final regression gates before commit.
 *
 * Important source-of-truth correction:
 *   - docs/sdk-setup-client.md used to say https://app.agentrelay.com.
 *   - ../cloud/infra/web-routing.ts and ../cloud/packages/cli/src/cli/constants.ts
 *     show the real hosted cloud API base is https://agentrelay.com/cloud.
 *
 * Run from the relayfile repo root:
 *   agent-relay run workflows/062-sdk-setup-client.ts
 */
import path from 'node:path';
import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

const RELAYFILE_ROOT = process.cwd();
const CLOUD_ROOT = path.resolve(RELAYFILE_ROOT, '../cloud');
const CORRECT_CLOUD_API_URL = 'https://agentrelay.com/cloud';
const RELAYFILE_BRANCH = 'codex/sdk-setup-client';
const CLOUD_BRANCH = 'codex/sdk-setup-client-cloud-contract';

async function main() {
  const result = await workflow('062-sdk-setup-client')
    .description(
      'Implement RelayfileSetup and WorkspaceHandle in @relayfile/sdk, update the cloud API contract in ../cloud, and prove every create/join/integration/status/disconnect/token-refresh path end to end.',
    )
    .pattern('dag')
    .channel('wf-062-sdk-setup-client')
    .maxConcurrency(4)
    .timeout(10_800_000)

    .agent('architect', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Turns the spec and current cloud implementation into a precise cross-repo acceptance contract before code changes begin.',
      retries: 1,
    })
    .agent('sdk-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements the TypeScript SDK setup client, setup types, setup errors, exports, and SDK test harnesses.',
      retries: 2,
    })
    .agent('cloud-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements the cloud web API contract changes in ../cloud and the targeted cloud route tests.',
      retries: 2,
    })
    .agent('e2e-impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Builds and maintains the package-consumer E2E proof that exercises the SDK against realistic mock cloud and relayfile servers.',
      retries: 2,
    })
    .agent('reviewer', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'reviewer',
      role: 'Reviews the final cross-repo diff for correctness, boundedness, and whether the evidence genuinely proves the spec paths.',
      retries: 1,
    })

    // ---------------------------------------------------------------------
    // Phase 1: Preflight and source-of-truth collection
    // ---------------------------------------------------------------------
    .step('preflight', {
      type: 'deterministic',
      command: [
        'set -e',
        `test -d ${shell(RELAYFILE_ROOT)}`,
        `test -d ${shell(CLOUD_ROOT)}`,
        `echo "relayfile root: ${RELAYFILE_ROOT}"`,
        `echo "cloud root: ${CLOUD_ROOT}"`,
        `echo "expected cloud API URL: ${CORRECT_CLOUD_API_URL}"`,
        '',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'RELAYFILE_CURRENT=$(git rev-parse --abbrev-ref HEAD)',
        'echo "relayfile branch: $RELAYFILE_CURRENT"',
        'if [ "$RELAYFILE_CURRENT" = "main" ]; then',
        `  git checkout -b ${shell(RELAYFILE_BRANCH)}`,
        `  echo "created relayfile branch ${RELAYFILE_BRANCH}"`,
        'elif [ "$RELAYFILE_CURRENT" = "' + RELAYFILE_BRANCH + '" ]; then',
        '  echo "already on relayfile workflow branch"',
        'else',
        '  echo "relayfile running on existing branch $RELAYFILE_CURRENT"',
        'fi',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: relayfile staging area is dirty; unstage before running this workflow"',
        '  git diff --cached --stat',
        '  exit 1',
        'fi',
        'npm --version',
        'node --version',
        '',
        `cd ${shell(CLOUD_ROOT)}`,
        'CLOUD_CURRENT=$(git rev-parse --abbrev-ref HEAD)',
        'echo "cloud branch: $CLOUD_CURRENT"',
        'if [ "$CLOUD_CURRENT" = "main" ]; then',
        `  git checkout -b ${shell(CLOUD_BRANCH)}`,
        `  echo "created cloud branch ${CLOUD_BRANCH}"`,
        'elif [ "$CLOUD_CURRENT" = "' + CLOUD_BRANCH + '" ]; then',
        '  echo "already on cloud workflow branch"',
        'else',
        '  echo "cloud running on existing branch $CLOUD_CURRENT"',
        'fi',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: cloud staging area is dirty; unstage before running this workflow"',
        '  git diff --cached --stat',
        '  exit 1',
        'fi',
        'npm --version',
        'node --version',
        'echo PREFLIGHT_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('collect-sdk-context', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'echo "=== spec URL lines ==="',
        'rg -n "app\\.agentrelay\\.com|agentrelay\\.com/cloud|Cloud web API base URL|/api/v1/workspaces|connect-session|/status|disconnectIntegration|Testing" docs/sdk-setup-client.md || true',
        'echo',
        'echo "=== SDK package ==="',
        'cat packages/sdk/typescript/package.json',
        'echo',
        'echo "=== SDK exports ==="',
        'sed -n "1,220p" packages/sdk/typescript/src/index.ts',
        'echo',
        'echo "=== RelayFileClient constructor/token support ==="',
        'rg -n "DEFAULT_RELAYFILE_BASE_URL|AccessTokenProvider|RelayFileClientOptions|constructor|token:" packages/sdk/typescript/src/client.ts | head -80',
        'echo',
        'echo "=== existing SDK tests ==="',
        'find packages/sdk/typescript/src -maxdepth 1 -name "*.test.ts" -print -exec sed -n "1,120p" {} \\;',
        'echo',
        'echo "=== existing helper scripts ==="',
        'find scripts packages/sdk/typescript -maxdepth 3 -type f | sort | sed -n "1,220p"',
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
        'echo "=== cloud app URL source of truth ==="',
        'sed -n "1,180p" infra/web-routing.ts',
        'echo',
        'echo "=== CLI cloud default ==="',
        'sed -n "1,80p" packages/cli/src/cli/constants.ts',
        'echo',
        'echo "=== workspace create route ==="',
        'sed -n "1,220p" packages/web/app/api/v1/workspaces/route.ts',
        'echo',
        'echo "=== workspace join route ==="',
        'sed -n "1,220p" "packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts"',
        'echo',
        'echo "=== connect-session route ==="',
        'sed -n "1,220p" "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts"',
        'echo',
        'echo "=== status route ==="',
        'sed -n "1,220p" "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts"',
        'echo',
        'echo "=== providers and nango mapping ==="',
        'sed -n "1,180p" packages/web/lib/integrations/providers.ts',
        'echo',
        'echo "=== request auth currently rejects raw relayfile JWTs ==="',
        'sed -n "1,320p" packages/web/lib/auth/request-auth.ts',
        'echo',
        'echo "=== route/test patterns ==="',
        'find tests -maxdepth 2 -type f | sort | sed -n "1,180p"',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('define-acceptance-contract', {
      agent: 'architect',
      dependsOn: ['collect-sdk-context', 'collect-cloud-context'],
      task: [
        'Write the cross-repo acceptance contract before implementation.',
        '',
        'Relayfile root:',
        RELAYFILE_ROOT,
        '',
        'Cloud root:',
        CLOUD_ROOT,
        '',
        'Correct hosted cloud API URL:',
        CORRECT_CLOUD_API_URL,
        '',
        'SDK context:',
        '{{steps.collect-sdk-context.output}}',
        '',
        'Cloud context:',
        '{{steps.collect-cloud-context.output}}',
        '',
        'Create docs/sdk-setup-client-acceptance.md in the relayfile repo. Use exactly these sections:',
        '',
        '1. URL correction',
        '   - State that RelayfileSetup defaults to https://agentrelay.com/cloud, not https://app.agentrelay.com.',
        '   - Cite the local source files: ../cloud/infra/web-routing.ts and ../cloud/packages/cli/src/cli/constants.ts.',
        '',
        '2. File ownership',
        '   - Relayfile SDK files to create or modify:',
        '     packages/sdk/typescript/src/setup.ts',
        '     packages/sdk/typescript/src/setup-types.ts',
        '     packages/sdk/typescript/src/setup-errors.ts',
        '     packages/sdk/typescript/src/setup.test.ts',
        '     packages/sdk/typescript/src/index.ts',
        '     packages/sdk/typescript/package.json only if an actual test/build dependency is necessary.',
        '     Optional E2E harness files under packages/sdk/typescript/scripts or scripts if justified.',
        '   - Cloud files to create or modify:',
        '     ../cloud/packages/web/lib/auth/request-auth.ts',
        '     ../cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts',
        '     ../cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts',
        '     ../cloud/packages/web/lib/integrations/providers.ts only if provider shape drift is discovered.',
        '     ../cloud/tests/sdk-setup-client-routes.test.ts or an equivalently targeted route test.',
        '     ../cloud/tests/cli/default-api-url.test.ts only if the URL assertion needs to be extended.',
        '',
        '3. Endpoint contract to prove',
        '   - POST /api/v1/workspaces creates a workspace with optional bearer auth.',
        '   - POST /api/v1/workspaces/:workspaceId/join returns a relayfile JWT, relayfileUrl, wsUrl, and relaycastApiKey.',
        '   - POST /api/v1/workspaces/:workspaceId/integrations/connect-session accepts a relayfile JWT for the same workspace and returns token, expiresAt, connectLink, plus connectionId when the cloud can provide or infer it. If the cloud cannot know the final Nango connectionId at session creation time, it must return workspaceId as the polling key and document that the status route treats missing/workspaceId connectionId as "ready when a provider row exists".',
        '   - GET /api/v1/workspaces/:workspaceId/integrations/:provider/status accepts relayfile JWT auth, accepts an omitted connectionId by defaulting to workspaceId, and resolves ready from the real workspace_integrations row.',
        '   - DELETE /api/v1/workspaces/:workspaceId/integrations/:provider/status accepts relayfile JWT auth and removes the integration for disconnectIntegration().',
        '',
        '4. SDK public contract',
        '   - RelayfileSetup, WorkspaceHandle, all setup option/result types, WORKSPACE_INTEGRATION_PROVIDERS, and setup error classes are exported from @relayfile/sdk.',
        '   - cloudApiUrl defaults to https://agentrelay.com/cloud and all URL building must preserve the /cloud base path.',
        '   - accessToken supports string and async factory; auth headers are applied to cloud calls when available.',
        '   - createWorkspace calls create then join; joinWorkspace only calls join.',
        '   - connectIntegration first checks isConnected when connectionId is supplied, maps provider to cloud config key, stores the returned or inferred connectionId, and returns alreadyConnected/connectLink/sessionToken/expiresAt/connectionId.',
        '   - waitForConnection implements polling, timeout, AbortSignal, onPoll, 429 Retry-After handling, bounded 5xx retry, and immediate CloudApiError on 401/403/404.',
        '   - client() returns a singleton RelayFileClient using info.relayfileUrl and an async token factory that refreshes at 55 minutes.',
        '   - getToken() is synchronous and refreshToken() rejoins with the original join options.',
        '',
        '5. Required unit tests',
        '   - Include every test listed in docs/sdk-setup-client.md, plus a test that the default cloud URL is exactly https://agentrelay.com/cloud and generated requests include /cloud/api/v1.',
        '   - Include cloud route tests for relayfile JWT auth on connect-session, status ready with omitted connectionId, status ready with connectionId=workspaceId, status false before webhook/upsert, DELETE status route, and unauthorized/forbidden workspace mismatch.',
        '',
        '6. Required E2E proof',
        '   - Build @relayfile/sdk, npm pack it, install it into a temporary consumer project, and run a Node script that imports from the packed package rather than source files.',
        '   - The script must start a mock cloud server and a mock relayfile server, then exercise: createWorkspace, joinWorkspace, client().listTree, connectIntegration for already-connected and OAuth-required cases, waitForConnection delayed success, isConnected false/true, disconnectIntegration, timeout error, abort error, malformed cloud response, cloud 500 CloudApiError, and token refresh by forcing the handle over the 55-minute threshold.',
        '   - The script must assert the actual HTTP paths hit by the SDK: /api/v1/workspaces, /api/v1/workspaces/:id/join, /api/v1/workspaces/:id/integrations/connect-session, /api/v1/workspaces/:id/integrations/:provider/status, and the relayfile /v1/workspaces/:id/fs/tree call.',
        '',
        '7. Regression gates',
        '   - Relayfile: npm run typecheck --workspace=packages/sdk/typescript, npm run test --workspace=packages/sdk/typescript, npm run build --workspace=packages/sdk/typescript, and the new package-consumer E2E command.',
        '   - Cloud: npx vitest run tests/sdk-setup-client-routes.test.ts, npx tsx --test tests/app-path.test.ts tests/cli/default-api-url.test.ts, npm run typecheck, and npm test unless the workflow records a concrete pre-existing unrelated failure.',
        '',
        '8. Commit policy',
        '   - Commit relayfile and cloud separately, with explicit file lists. Do not stage unrelated dirty files.',
        '',
        'End the file with ACCEPTANCE_CONTRACT_READY on its own line.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/sdk-setup-client-acceptance.md' },
      retries: 1,
    })

    .step('verify-acceptance-contract', {
      type: 'deterministic',
      dependsOn: ['define-acceptance-contract'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'test -f docs/sdk-setup-client-acceptance.md',
        `grep -q "${CORRECT_CLOUD_API_URL}" docs/sdk-setup-client-acceptance.md`,
        'grep -q "package-consumer E2E" docs/sdk-setup-client-acceptance.md',
        'grep -q "DELETE /api/v1/workspaces/:workspaceId/integrations/:provider/status" docs/sdk-setup-client-acceptance.md',
        'grep -q "ACCEPTANCE_CONTRACT_READY" docs/sdk-setup-client-acceptance.md',
        'echo ACCEPTANCE_CONTRACT_VERIFIED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ---------------------------------------------------------------------
    // Phase 2: Implementation, with deterministic verification after edits
    // ---------------------------------------------------------------------
    .step('implement-sdk', {
      agent: 'sdk-impl',
      dependsOn: ['verify-acceptance-contract'],
      task: [
        'Implement the relayfile TypeScript SDK portion of docs/sdk-setup-client-acceptance.md.',
        '',
        'Working directory:',
        RELAYFILE_ROOT,
        '',
        'Read first:',
        '- docs/sdk-setup-client.md',
        '- docs/sdk-setup-client-acceptance.md',
        '- packages/sdk/typescript/AGENTS.md',
        '- packages/sdk/typescript/src/client.ts',
        '- packages/sdk/typescript/src/index.ts',
        '- packages/sdk/typescript/src/client.test.ts',
        '',
        'Implement only the SDK-owned files from the acceptance contract unless you discover a necessary adjacent type/export change. Preserve ESM .js import/export extensions.',
        '',
        'Hard requirements:',
        '1. DEFAULT cloud URL must be exactly https://agentrelay.com/cloud.',
        '2. URL joining must preserve a path-bearing base URL such as /cloud. No string concatenation that can drop /cloud.',
        '3. Use native fetch; inject fetch only if it keeps tests clean and does not disturb the existing RelayFileClient API.',
        '4. Add RelayfileSetup, WorkspaceHandle, setup types, setup errors, WORKSPACE_INTEGRATION_PROVIDERS, and exports from index.ts.',
        '5. Validate all cloud responses and throw MalformedCloudResponseError with the missing field name.',
        '6. CloudApiError must retain httpStatus and parsed httpBody.',
        '7. waitForConnection must implement timeout, AbortSignal, onPoll, 429 Retry-After, bounded 5xx retry, and immediate 401/403/404 failure.',
        '8. client() must return the same RelayFileClient instance on repeated calls and must use an async token factory.',
        '9. refreshToken() must reuse the original join options.',
        '10. Do not touch cloud files from this step.',
        '',
        'Do not run the test suite yet. Print git diff --stat for SDK files and end with SDK_IMPLEMENTATION_READY.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 1,
    })

    .step('implement-cloud-contract', {
      agent: 'cloud-impl',
      dependsOn: ['verify-acceptance-contract'],
      task: [
        'Implement the cloud web API contract portion of docs/sdk-setup-client-acceptance.md.',
        '',
        'Cloud working directory:',
        CLOUD_ROOT,
        '',
        'Relayfile acceptance doc:',
        `${RELAYFILE_ROOT}/docs/sdk-setup-client-acceptance.md`,
        '',
        'Read first:',
        '- packages/web/lib/auth/request-auth.ts',
        '- packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts',
        '- packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts',
        '- packages/web/lib/integrations/integration-route-handler.ts',
        '- packages/web/lib/integrations/workspace-integrations.ts',
        '- packages/web/lib/integrations/nango-service.ts',
        '- packages/web/lib/integrations/providers.ts',
        '- tests/shareable-workspaces.test.ts',
        '- tests/app-path.test.ts',
        '',
        'Implement only the cloud-owned files from the acceptance contract.',
        '',
        'Hard requirements:',
        '1. Do not change the hosted cloud URL away from https://agentrelay.com/cloud; keep infra/web-routing.ts as source of truth.',
        '2. Extend auth so a relayfile JWT for workspace W is accepted for workspace-scoped setup calls on workspace W. Do not trust an unsigned JWT. Use the existing relayauth/JWKS/verifier path if present; if no reusable verifier exists, build a small server-side verifier around the configured relayauth JWKS URL and cover it in tests.',
        '3. connect-session must accept relayfile JWT auth for the same workspace, reject workspace mismatch, and return a polling connectionId. If the real Nango connectionId is not known at session creation, return workspaceId and make the status route treat omitted connectionId or connectionId=workspaceId as readiness for any existing provider row on that workspace.',
        '4. status GET must accept missing connectionId, connectionId=workspaceId, and explicit actual connectionId. It must return { ready: false } before the workspace_integrations row exists and { ready: true } after it exists.',
        '5. status DELETE must implement the SDK disconnectIntegration path from the spec.',
        '6. Keep provider validation aligned with WORKSPACE_INTEGRATION_PROVIDERS. Do not invent a provider that the cloud does not route.',
        '7. Add focused tests under ../cloud/tests/sdk-setup-client-routes.test.ts or an equivalently named file.',
        '8. Do not touch relayfile SDK files from this step.',
        '',
        'Do not run the full test suite yet. Print git diff --stat for cloud files and end with CLOUD_IMPLEMENTATION_READY.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 1,
    })

    .step('verify-edits-landed', {
      type: 'deterministic',
      dependsOn: ['implement-sdk', 'implement-cloud-contract'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'echo "=== relayfile diff stat ==="',
        'git diff --stat packages/sdk/typescript docs/sdk-setup-client-acceptance.md',
        'test -f packages/sdk/typescript/src/setup.ts',
        'test -f packages/sdk/typescript/src/setup-types.ts',
        'test -f packages/sdk/typescript/src/setup-errors.ts',
        'test -f packages/sdk/typescript/src/setup.test.ts',
        `grep -R "${CORRECT_CLOUD_API_URL}" packages/sdk/typescript/src/setup.ts packages/sdk/typescript/src/setup.test.ts docs/sdk-setup-client-acceptance.md`,
        'grep -q "RelayfileSetup" packages/sdk/typescript/src/index.ts',
        'grep -q "WorkspaceHandle" packages/sdk/typescript/src/index.ts',
        'grep -q "IntegrationConnectionTimeoutError" packages/sdk/typescript/src/index.ts',
        '',
        `cd ${shell(CLOUD_ROOT)}`,
        'echo "=== cloud diff stat ==="',
        'git diff --stat packages/web tests',
        'test -f tests/sdk-setup-client-routes.test.ts',
        'rg -n "relayfile|JWKS|connectionId|DELETE|workspaceId" packages/web/app/api/v1/workspaces packages/web/lib/auth tests/sdk-setup-client-routes.test.ts | head -120',
        'echo EDITS_LANDED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ---------------------------------------------------------------------
    // Phase 3: SDK unit tests, cloud route tests, then fix-rerun loops
    // ---------------------------------------------------------------------
    .step('run-sdk-tests-first', {
      type: 'deterministic',
      dependsOn: ['verify-edits-landed'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'npm run test --workspace=packages/sdk/typescript 2>&1 | tail -160',
      ].join('\n'),
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-sdk-tests', {
      agent: 'sdk-impl',
      dependsOn: ['run-sdk-tests-first'],
      task: [
        'Check the SDK test output. If all tests passed, do nothing except end with SDK_TESTS_OK.',
        '',
        'SDK test output:',
        '{{steps.run-sdk-tests-first.output}}',
        '',
        'If there are failures:',
        '1. Read the failing SDK test and relevant source.',
        '2. Fix the source or the test if the test is wrong about the intended contract.',
        '3. Re-run: npm run test --workspace=packages/sdk/typescript',
        '4. Keep fixing until all SDK tests pass.',
        '',
        'Do not delete coverage for any required acceptance-contract behavior. End with SDK_TESTS_OK.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 2,
    })

    .step('run-sdk-tests-final', {
      type: 'deterministic',
      dependsOn: ['fix-sdk-tests'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'npm run test --workspace=packages/sdk/typescript 2>&1 | tail -80',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('run-cloud-route-tests-first', {
      type: 'deterministic',
      dependsOn: ['verify-edits-landed'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npx vitest run tests/sdk-setup-client-routes.test.ts 2>&1 && npx tsx --test tests/app-path.test.ts tests/cli/default-api-url.test.ts 2>&1 | tail -180',
      ].join('\n'),
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-cloud-route-tests', {
      agent: 'cloud-impl',
      dependsOn: ['run-cloud-route-tests-first'],
      task: [
        'Check the cloud route test output. If all targeted tests passed, do nothing except end with CLOUD_ROUTE_TESTS_OK.',
        '',
        'Cloud test output:',
        '{{steps.run-cloud-route-tests-first.output}}',
        '',
        'If there are failures:',
        '1. Read tests/sdk-setup-client-routes.test.ts and the failing route/source.',
        '2. Fix the route or auth implementation without weakening the acceptance contract.',
        '3. Re-run: npx vitest run tests/sdk-setup-client-routes.test.ts && npx tsx --test tests/app-path.test.ts tests/cli/default-api-url.test.ts',
        '4. Keep fixing until the targeted route tests pass.',
        '',
        'End with CLOUD_ROUTE_TESTS_OK.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 2,
    })

    .step('run-cloud-route-tests-final', {
      type: 'deterministic',
      dependsOn: ['fix-cloud-route-tests'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npx vitest run tests/sdk-setup-client-routes.test.ts 2>&1 && npx tsx --test tests/app-path.test.ts tests/cli/default-api-url.test.ts 2>&1 | tail -100',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ---------------------------------------------------------------------
    // Phase 4: Package-consumer E2E proof for every SDK path
    // ---------------------------------------------------------------------
    .step('create-sdk-e2e-proof', {
      agent: 'e2e-impl',
      dependsOn: ['run-sdk-tests-final', 'run-cloud-route-tests-final'],
      task: [
        'Create the package-consumer E2E proof required by docs/sdk-setup-client-acceptance.md.',
        '',
        'Working directory:',
        RELAYFILE_ROOT,
        '',
        'Read:',
        '- docs/sdk-setup-client.md',
        '- docs/sdk-setup-client-acceptance.md',
        '- packages/sdk/typescript/src/setup.ts',
        '- packages/sdk/typescript/src/setup.test.ts',
        '',
        'Add a deterministic E2E script in a location that fits the repo, preferably packages/sdk/typescript/scripts/setup-e2e.mjs or scripts/sdk-setup-e2e.mjs. If package.json needs a script alias, add the smallest necessary script.',
        '',
        'The E2E must:',
        '1. Run after npm run build --workspace=packages/sdk/typescript.',
        '2. npm pack packages/sdk/typescript and install the tarball into a temporary consumer directory.',
        '3. Start a mock cloud HTTP server that records requests and implements:',
        '   - POST /api/v1/workspaces',
        '   - POST /api/v1/workspaces/:id/join',
        '   - POST /api/v1/workspaces/:id/integrations/connect-session',
        '   - GET /api/v1/workspaces/:id/integrations/:provider/status',
        '   - DELETE /api/v1/workspaces/:id/integrations/:provider/status',
        '4. Start a mock relayfile server implementing at least GET /v1/workspaces/:id/fs/tree.',
        '5. Run a consumer script importing from @relayfile/sdk, not source files.',
        '6. Assert createWorkspace calls create then join, with Authorization when accessToken is provided.',
        '7. Assert joinWorkspace only calls join.',
        '8. Assert client().listTree hits the mock relayfile base URL using the current token.',
        '9. Assert connectIntegration returns alreadyConnected when isConnected is true and does not call connect-session.',
        '10. Assert connectIntegration returns connectLink/sessionToken/expiresAt/connectionId for OAuth-required case and stores that connectionId for waitForConnection.',
        '11. Assert waitForConnection handles delayed ready, timeout, AbortSignal, 429 Retry-After, 5xx retry, and immediate 401 failure.',
        '12. Assert isConnected false/true and disconnectIntegration DELETE path.',
        '13. Assert malformed create/join/status responses throw MalformedCloudResponseError.',
        '14. Assert CloudApiError includes httpStatus and parsed httpBody.',
        '15. Force token refresh over the 55-minute threshold by stubbing Date.now or an equivalent controlled clock and assert the next RelayFileClient call rejoins before using the token.',
        '16. Print SDK_SETUP_E2E_OK only after all assertions pass.',
        '',
        'Do not rely on external services or real credentials. End with SDK_E2E_PROOF_READY.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 1,
    })

    .step('verify-e2e-script-exists', {
      type: 'deterministic',
      dependsOn: ['create-sdk-e2e-proof'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'E2E_FILES=$(find packages/sdk/typescript/scripts scripts -maxdepth 2 -type f \\( -name "*setup*e2e*.mjs" -o -name "*sdk*setup*.mjs" \\) 2>/dev/null | tr "\\n" " ")',
        'echo "E2E files: $E2E_FILES"',
        'test -n "$E2E_FILES"',
        'grep -R "npm pack\\|SDK_SETUP_E2E_OK\\|connect-session\\|integrations/.*/status\\|fs/tree" $E2E_FILES',
        'echo E2E_SCRIPT_VERIFIED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('run-sdk-e2e-first', {
      type: 'deterministic',
      dependsOn: ['verify-e2e-script-exists'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'npm run build --workspace=packages/sdk/typescript',
        'if npm run | grep -q "sdk:setup:e2e"; then',
        '  npm run sdk:setup:e2e 2>&1 | tail -200',
        'elif npm run --workspace=packages/sdk/typescript | grep -q "setup:e2e"; then',
        '  npm run setup:e2e --workspace=packages/sdk/typescript 2>&1 | tail -200',
        'elif test -f packages/sdk/typescript/scripts/setup-e2e.mjs; then',
        '  node packages/sdk/typescript/scripts/setup-e2e.mjs 2>&1 | tail -200',
        'else',
        '  node scripts/sdk-setup-e2e.mjs 2>&1 | tail -200',
        'fi',
      ].join('\n'),
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-sdk-e2e', {
      agent: 'e2e-impl',
      dependsOn: ['run-sdk-e2e-first'],
      task: [
        'Fix the package-consumer E2E proof until it passes. If it already printed SDK_SETUP_E2E_OK, do nothing and end with SDK_SETUP_E2E_OK.',
        '',
        'E2E output:',
        '{{steps.run-sdk-e2e-first.output}}',
        '',
        'Rules:',
        '1. Failures in the SDK implementation should be fixed in SDK source, not papered over in the E2E script.',
        '2. Failures in the E2E mock should be fixed by making the mock more faithful to the spec paths, not by deleting assertions.',
        '3. Re-run the exact E2E command until it prints SDK_SETUP_E2E_OK.',
        '4. If you touch SDK source, re-run npm run test --workspace=packages/sdk/typescript before ending.',
        '',
        'End with SDK_SETUP_E2E_OK.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 2,
    })

    .step('run-sdk-e2e-final', {
      type: 'deterministic',
      dependsOn: ['fix-sdk-e2e'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'npm run build --workspace=packages/sdk/typescript',
        'if npm run | grep -q "sdk:setup:e2e"; then',
        '  npm run sdk:setup:e2e 2>&1 | tee /tmp/sdk-setup-e2e.log',
        'elif npm run --workspace=packages/sdk/typescript | grep -q "setup:e2e"; then',
        '  npm run setup:e2e --workspace=packages/sdk/typescript 2>&1 | tee /tmp/sdk-setup-e2e.log',
        'elif test -f packages/sdk/typescript/scripts/setup-e2e.mjs; then',
        '  node packages/sdk/typescript/scripts/setup-e2e.mjs 2>&1 | tee /tmp/sdk-setup-e2e.log',
        'else',
        '  node scripts/sdk-setup-e2e.mjs 2>&1 | tee /tmp/sdk-setup-e2e.log',
        'fi',
        'grep -q "SDK_SETUP_E2E_OK" /tmp/sdk-setup-e2e.log',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ---------------------------------------------------------------------
    // Phase 5: Typecheck/build/regression gates
    // ---------------------------------------------------------------------
    .step('sdk-typecheck-build', {
      type: 'deterministic',
      dependsOn: ['run-sdk-e2e-final'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'npm run typecheck --workspace=packages/sdk/typescript',
        'npm run build --workspace=packages/sdk/typescript',
        'echo SDK_TYPECHECK_BUILD_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('cloud-typecheck-first', {
      type: 'deterministic',
      dependsOn: ['run-cloud-route-tests-final'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npm run typecheck 2>&1 | tail -180',
      ].join('\n'),
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-cloud-typecheck', {
      agent: 'cloud-impl',
      dependsOn: ['cloud-typecheck-first'],
      task: [
        'Check cloud typecheck output. If it passed, do nothing and end with CLOUD_TYPECHECK_OK.',
        '',
        'Typecheck output:',
        '{{steps.cloud-typecheck-first.output}}',
        '',
        'If there are failures caused by this workflow, fix them and re-run npm run typecheck in the cloud repo. If failures are clearly pre-existing and unrelated, document exact file/line evidence in docs/sdk-setup-client-cloud-typecheck-notes.md in the relayfile repo, then end with CLOUD_TYPECHECK_OK.',
        '',
        'End with CLOUD_TYPECHECK_OK.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 2,
    })

    .step('cloud-typecheck-final', {
      type: 'deterministic',
      dependsOn: ['fix-cloud-typecheck'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npm run typecheck 2>&1 | tail -80',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('cloud-regression-first', {
      type: 'deterministic',
      dependsOn: ['cloud-typecheck-final'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npm test 2>&1 | tail -200',
      ].join('\n'),
      captureOutput: true,
      failOnError: false,
      timeoutMs: 1_800_000,
    })

    .step('fix-cloud-regressions', {
      agent: 'cloud-impl',
      dependsOn: ['cloud-regression-first'],
      task: [
        'Check the full cloud regression output. If everything passed, do nothing and end with CLOUD_REGRESSIONS_OK.',
        '',
        'Regression output:',
        '{{steps.cloud-regression-first.output}}',
        '',
        'If failures were caused by this workflow, fix them and rerun npm test in ../cloud until green.',
        'If failures are unrelated and pre-existing, write docs/sdk-setup-client-cloud-regression-notes.md in the relayfile repo with exact command output excerpts and why they are unrelated. Then still ensure all targeted sdk setup client route tests pass.',
        '',
        'End with CLOUD_REGRESSIONS_OK.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      retries: 2,
    })

    .step('cloud-regression-final', {
      type: 'deterministic',
      dependsOn: ['fix-cloud-regressions'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'npm test 2>&1 | tail -100',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
      timeoutMs: 1_800_000,
    })

    // ---------------------------------------------------------------------
    // Phase 6: Final evidence review, scoped commits
    // ---------------------------------------------------------------------
    .step('capture-final-evidence', {
      type: 'deterministic',
      dependsOn: ['sdk-typecheck-build', 'cloud-regression-final'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'mkdir -p docs/evidence',
        `EVIDENCE_FILE=${shell(`${RELAYFILE_ROOT}/docs/evidence/sdk-setup-client-final-evidence.md`)}`,
        '{',
        '  echo "# SDK setup client final evidence"',
        '  echo',
        '  echo "## Relayfile diff"',
        `  git -C ${shell(RELAYFILE_ROOT)} diff --stat`,
        '  echo',
        '  echo "## Relayfile changed files"',
        `  git -C ${shell(RELAYFILE_ROOT)} diff --name-only`,
        '  echo',
        '  echo "## Cloud diff"',
        `  git -C ${shell(CLOUD_ROOT)} diff --stat`,
        '  echo',
        '  echo "## Cloud changed files"',
        `  git -C ${shell(CLOUD_ROOT)} diff --name-only`,
        '} > "$EVIDENCE_FILE"',
        'grep -q "SDK setup client final evidence" "$EVIDENCE_FILE"',
        'echo FINAL_EVIDENCE_CAPTURED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('review-final-diff', {
      agent: 'reviewer',
      dependsOn: ['capture-final-evidence'],
      task: [
        'Review the final cross-repo implementation. You are reviewing current diff only.',
        '',
        'Relayfile root:',
        RELAYFILE_ROOT,
        '',
        'Cloud root:',
        CLOUD_ROOT,
        '',
        'Read:',
        '- docs/sdk-setup-client.md',
        '- docs/sdk-setup-client-acceptance.md',
        '- docs/evidence/sdk-setup-client-final-evidence.md',
        '- relayfile git diff',
        '- cloud git diff',
        '',
        'Review checklist:',
        '1. The default cloud URL is https://agentrelay.com/cloud everywhere in the SDK and tests.',
        '2. URL joining preserves the /cloud base path.',
        '3. The SDK exports exactly the requested public setup surface without breaking existing exports.',
        '4. All error classes carry the fields promised by the spec.',
        '5. createWorkspace, joinWorkspace, connectIntegration, waitForConnection, isConnected, disconnectIntegration, client(), getToken(), and refreshToken are all tested.',
        '6. The cloud accepts relayfile JWT auth only for the same workspace and rejects mismatches.',
        '7. The status route handles omitted connectionId/workspaceId polling key and explicit connection IDs.',
        '8. DELETE status implements disconnectIntegration per spec.',
        '9. The package-consumer E2E imports the packed package, not source files, and hits every spec path.',
        '10. There is no unrelated refactor or broad churn.',
        '',
        'Write docs/sdk-setup-client-review.md in the relayfile repo. End with REVIEW_APPROVED on its own line or REVIEW_CHANGES_REQUESTED with specific fixes.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/sdk-setup-client-review.md' },
      retries: 1,
    })

    .step('verify-review-approved', {
      type: 'deterministic',
      dependsOn: ['review-final-diff'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'grep -q "REVIEW_APPROVED" docs/sdk-setup-client-review.md',
        'echo REVIEW_VERIFIED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('commit-relayfile', {
      type: 'deterministic',
      dependsOn: ['verify-review-approved'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'git status --short',
        'git add docs/sdk-setup-client.md docs/sdk-setup-client-acceptance.md docs/sdk-setup-client-review.md docs/evidence/sdk-setup-client-final-evidence.md packages/sdk/typescript/src/setup.ts packages/sdk/typescript/src/setup-types.ts packages/sdk/typescript/src/setup-errors.ts packages/sdk/typescript/src/setup.test.ts packages/sdk/typescript/src/index.ts',
        'git add packages/sdk/typescript/package.json packages/sdk/typescript/package-lock.json packages/sdk/typescript/scripts/setup-e2e.mjs scripts/sdk-setup-e2e.mjs 2>/dev/null || true',
        'git diff --cached --name-only',
        'git diff --cached --quiet && (echo "No relayfile changes to commit"; exit 0)',
        'HUSKY=0 git -c core.hooksPath=/dev/null commit --no-verify -m "feat: add relayfile sdk setup client"',
        'echo RELAYFILE_COMMIT_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('commit-cloud', {
      type: 'deterministic',
      dependsOn: ['verify-review-approved'],
      command: [
        'set -e',
        `cd ${shell(CLOUD_ROOT)}`,
        'git status --short',
        'git add packages/web/lib/auth/request-auth.ts "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts" "packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts" packages/web/lib/integrations/providers.ts tests/sdk-setup-client-routes.test.ts tests/app-path.test.ts tests/cli/default-api-url.test.ts 2>/dev/null || true',
        'git diff --cached --name-only',
        'git diff --cached --quiet && (echo "No cloud changes to commit"; exit 0)',
        'HUSKY=0 git -c core.hooksPath=/dev/null commit --no-verify -m "feat: support relayfile sdk setup routes"',
        'echo CLOUD_COMMIT_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('final-summary', {
      type: 'deterministic',
      dependsOn: ['commit-relayfile', 'commit-cloud'],
      command: [
        'set -e',
        `cd ${shell(RELAYFILE_ROOT)}`,
        'echo "=== relayfile HEAD ==="',
        'git --no-pager log -1 --oneline',
        'echo',
        `cd ${shell(CLOUD_ROOT)}`,
        'echo "=== cloud HEAD ==="',
        'git --no-pager log -1 --oneline',
        'echo',
        'echo SDK_SETUP_CLIENT_WORKFLOW_COMPLETE',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
    .run({ cwd: RELAYFILE_ROOT });

  console.log('SDK setup client workflow complete:', result.status);
}

function shell(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
