/**
 * integrate-relayauth.ts
 *
 * Integrates relayauth into relayfile: verify relayauth JWTs for all
 * filesystem operations. Scoped access to paths, revision-controlled
 * writes, and audit trail for every file operation.
 *
 * Depends on: @relayauth/sdk (TokenVerifier, ScopeChecker)
 *
 * Changes:
 *   - Go server: verify relayauth JWTs (via JWKS) alongside existing JWT auth
 *   - Go mount daemon: accept relayauth tokens for authentication
 *   - TS SDK: pass relayauth tokens in requests
 *   - Scope enforcement: relayfile:fs:read:/path, relayfile:fs:write:/path
 *   - Path-scoped access: agent can only read/write files matching their scope paths
 *
 * Run: agent-relay run workflows/integrate-relayauth.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile';
const RELAYAUTH = '/Users/khaliqgant/Projects/AgentWorkforce/relayauth';

async function main() {
const result = await workflow('integrate-relayauth-relayfile')
  .description('Add relayauth JWT verification to relayfile server and mount daemon')
  .pattern('dag')
  .channel('wf-relayfile-relayauth')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', {
    cli: 'claude',
    preset: 'lead',
    role: 'Design the integration, review code, fix issues',
    cwd: RELAYFILE,
  })
  .agent('go-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement Go server and mount daemon auth changes',
    cwd: RELAYFILE,
  })
  .agent('sdk-dev', {
    cli: 'codex',
    preset: 'worker',
    role: 'Update TS SDK to pass relayauth tokens',
    cwd: RELAYFILE,
  })
  .agent('test-writer', {
    cli: 'codex',
    preset: 'worker',
    role: 'Write tests for auth integration',
    cwd: RELAYFILE,
  })
  .agent('reviewer', {
    cli: 'claude',
    preset: 'reviewer',
    role: 'Review for security, path-scoping correctness, backwards compat',
    cwd: RELAYFILE,
  })

  // ── Phase 1: Read existing code ────────────────────────────────────

  .step('read-go-auth', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/internal/httpapi/auth.go`,
    captureOutput: true,
  })

  .step('read-go-server', {
    type: 'deterministic',
    command: `head -100 ${RELAYFILE}/internal/httpapi/server.go`,
    captureOutput: true,
  })

  .step('read-mount-daemon', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/cmd/relayfile-mount/main.go`,
    captureOutput: true,
  })

  .step('read-ts-sdk-client', {
    type: 'deterministic',
    command: `head -60 ${RELAYFILE}/packages/relayfile-sdk/src/client.ts`,
    captureOutput: true,
  })

  .step('read-relayauth-types', {
    type: 'deterministic',
    command: `cat ${RELAYAUTH}/packages/types/src/token.ts && echo "=== SCOPE ===" && cat ${RELAYAUTH}/packages/types/src/scope.ts`,
    captureOutput: true,
  })

  .step('read-relayauth-sdk', {
    type: 'deterministic',
    command: `cat ${RELAYAUTH}/packages/sdk/src/verify.ts`,
    captureOutput: true,
  })

  // ── Phase 2: Write tests + Implement ──────────────────────────────

  .step('write-tests', {
    agent: 'test-writer',
    dependsOn: ['read-go-auth', 'read-relayauth-types'],
    task: `Write tests for relayfile + relayauth integration.

Current Go auth:
{{steps.read-go-auth.output}}

RelayAuth token format:
{{steps.read-relayauth-types.output}}

Create ${RELAYFILE}/internal/httpapi/relayauth_test.go:

Tests (Go testing package):
1. Valid relayauth JWT with relayfile:fs:read:* → can read any file
2. Valid relayauth JWT with relayfile:fs:read:/src/* → can read /src/foo.ts, cannot read /config/secret.yaml
3. Valid relayauth JWT with relayfile:fs:write:/src/api/* → can write /src/api/route.ts, cannot write /src/ui/page.tsx
4. Expired JWT → 401
5. JWT with wrong audience (not "relayfile") → 401
6. Legacy relayfile JWT (existing format) → still works
7. No token → 401
8. Path-scoped write: agent writes to allowed path → 200
9. Path-scoped write: agent writes to disallowed path → 403
10. SponsorChain present in auth context

Mock JWKS by serving a test public key on a local HTTP server in the test.
Write to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-go-auth', {
    agent: 'go-dev',
    dependsOn: ['read-go-auth', 'read-go-server', 'read-relayauth-sdk', 'write-tests'],
    task: `Add relayauth JWT verification to the Go server.

Current Go auth:
{{steps.read-go-auth.output}}

Current server:
{{steps.read-go-server.output}}

RelayAuth SDK (reference for token format):
{{steps.read-relayauth-sdk.output}}

Changes to ${RELAYFILE}/internal/httpapi/auth.go:

1. Add JWKS fetching: fetch public keys from RELAYAUTH_JWKS_URL env var
   - Cache JWKS with 5-minute TTL
   - Use crypto/rsa + encoding/json to parse JWK → public key

2. Add JWT verification function: verifyRelayAuthToken(tokenString) → (claims, error)
   - Parse JWT header to get kid (key ID)
   - Look up public key from cached JWKS
   - Verify RS256 signature
   - Check exp, iss, aud ("relayfile" must be in aud array)
   - Return parsed claims including scopes, sponsorId, sponsorChain

3. Update the auth middleware chain:
   - Try relayauth JWT first (if RELAYAUTH_JWKS_URL is configured)
   - If valid: extract scopes, attach to request context
   - If not valid (not a JWT, wrong format): fall back to existing auth
   - Keep existing auth fully functional as fallback

4. Add path-scoped access enforcement:
   - New function: checkPathScope(scopes []string, action string, path string) bool
   - For each relayfile:fs:{action}:{pathPattern} scope, check if the request path matches
   - Wildcard matching: /src/* matches /src/foo.ts and /src/api/route.ts
   - Apply on read and write operations

5. Add env var: RELAYAUTH_JWKS_URL (optional — if not set, relayauth is disabled)

Write changes to disk. Use standard library only (no external JWT packages).`,
    verification: { type: 'exit_code' },
  })

  .step('implement-mount-auth', {
    agent: 'go-dev',
    dependsOn: ['read-mount-daemon', 'implement-go-auth'],
    task: `Update mount daemon to accept relayauth tokens.

Current mount daemon:
{{steps.read-mount-daemon.output}}

The mount daemon already accepts a --token flag. The change is minimal:
relayauth tokens are JWTs — they work with the existing --token flag.
The server-side auth (implemented in the previous step) handles verification.

Changes to ${RELAYFILE}/cmd/relayfile-mount/main.go:
1. Add --relayauth-url flag (optional) for documentation/logging purposes
2. Log at startup: "Authenticating via relayauth" if token looks like a JWT (has 3 dot-separated parts)
3. No functional change needed — the token is passed as Bearer header, server verifies

Write changes to disk. Keep it minimal.`,
    verification: { type: 'exit_code' },
  })

  .step('implement-ts-sdk', {
    agent: 'sdk-dev',
    dependsOn: ['read-ts-sdk-client'],
    task: `Update the TS SDK to support relayauth tokens.

Current SDK client:
{{steps.read-ts-sdk-client.output}}

The SDK already accepts a token in RelayFileClientOptions. The change is:

1. Add optional relayauthToken to RelayFileClientOptions:
   relayauthToken?: string | (() => string | Promise<string>);

2. When relayauthToken is set, use it as the Bearer token instead of the regular token.
   This allows the SDK to work with either relayfile-native tokens or relayauth tokens.

3. Add a helper: RelayFileClient.fromRelayAuth(baseUrl, relayauthToken)
   Convenience factory that creates a client authenticated via relayauth.

Edit ${RELAYFILE}/packages/relayfile-sdk/src/client.ts
Write to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('verify-files', {
    type: 'deterministic',
    dependsOn: ['implement-go-auth', 'implement-mount-auth', 'implement-ts-sdk', 'write-tests'],
    command: `bash -lc 'cd ${RELAYFILE} && echo "=== Go files ===" && ls internal/httpapi/relayauth_test.go 2>&1 && echo "=== Go build ===" && set -o pipefail && go build ./... 2>&1 | tail -5'`,
    captureOutput: true,
    failOnError: false,
  })

  // ── Phase 3: Review + Fix ─────────────────────────────────────────

  .step('run-tests', {
    type: 'deterministic',
    dependsOn: ['verify-files'],
    command: `bash -lc 'cd ${RELAYFILE} && set -o pipefail && go test ./internal/httpapi/... 2>&1 | tail -20'`,
    captureOutput: true,
    failOnError: false,
  })

  .step('review', {
    agent: 'reviewer',
    dependsOn: ['run-tests'],
    task: `Review the relayauth integration.

Test results:
{{steps.run-tests.output}}

Read changed files:
- cat ${RELAYFILE}/internal/httpapi/auth.go
- cat ${RELAYFILE}/internal/httpapi/relayauth_test.go

Verify:
1. JWKS caching has a TTL (not fetched on every request)
2. Path-scoped access: /src/* correctly matches /src/foo.ts but not /config/x
3. Backwards compat: existing JWT auth still works when RELAYAUTH_JWKS_URL not set
4. No hardcoded keys or URLs
5. RS256 verification uses standard library correctly
6. SponsorChain is passed through to request context`,
    verification: { type: 'exit_code' },
  })

  .step('fix', {
    agent: 'architect',
    dependsOn: ['review'],
    task: `Fix issues from review and tests.

Tests: {{steps.run-tests.output}}
Review: {{steps.review.output}}

Fix all issues. Run go test ./... and verify clean.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nRelayfile + RelayAuth integration: ${result.status}`);
}

main().catch(console.error);
