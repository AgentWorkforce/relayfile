/**
 * 060-mount-bulk-sync-migration.ts
 *
 * Migrate `relayfile-mount` from per-file HTTP sync to bulk sync using the
 * existing `/v1/workspaces/:id/fs/bulk` endpoint, and fix the first-write
 * 412 "missing If-Match header" error that leaks into mount logs.
 *
 * Context (from AgentWorkforce/cloud workflow-hang investigation):
 *   - Mount's per-file HTTP POSTs thrash under concurrent agent file writes
 *     (dozens of requests/second during a workflow run), producing
 *     "context deadline exceeded" storms with the 15s per-call timeout.
 *   - First-write case sends `If-Match: 0` on create, which relayfile's
 *     optimistic-concurrency check rejects with 412 when the remote already
 *     has that path. This manifests as:
 *         mount local change failed: http 412 precondition_failed:
 *         missing If-Match header
 *   - Server-side `/fs/bulk` endpoint already exists
 *     (`internal/httpapi/server.go` handleBulkWrite @ line ~1556).
 *     The mount's `HTTPClient` (`internal/mountsync/syncer.go`) only has a
 *     per-file `WriteFile` method — no bulk path.
 *
 * Run from the relayfile repo root:
 *   agent-relay run workflows/060-mount-bulk-sync-migration.ts
 */
import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('060-mount-bulk-sync-migration')
    .description(
      'Migrate relayfile-mount to bulk sync and fix first-write 412 errors, with go test-fix-rerun validation and a final e2e proof that bulk requests replace per-file storms.',
    )
    .pattern('dag')
    .channel('wf-060-mount-bulk-sync')
    .maxConcurrency(3)
    .timeout(7_200_000)

    .agent('architect', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Defines the exact boundary of the bulk-sync migration and writes an explicit implementation spec before any code is touched.',
      retries: 1,
    })
    .agent('impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements the Go changes in internal/mountsync and cmd/relayfile-mount per the boundary doc.',
      retries: 2,
    })
    .agent('reviewer', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'reviewer',
      role: 'Reads the final diff against the boundary doc, checks concurrency correctness of the batching path, confirms no behavior drift outside the bounded files.',
      retries: 1,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 1: Context
    // ────────────────────────────────────────────────────────────────
    .step('preflight', {
      type: 'deterministic',
      command: [
        'set -e',
        'BRANCH=$(git rev-parse --abbrev-ref HEAD)',
        'echo "branch: $BRANCH"',
        // Auto-create the workflow branch only when starting from main.
        'if [ "$BRANCH" = "main" ]; then',
        '  git checkout -b fix/mount-bulk-sync-migration',
        '  echo "created branch fix/mount-bulk-sync-migration"',
        'fi',
        // Working-tree drift check is intentionally relaxed — the commit
        // step later uses explicit `git add <files>` for the migration's
        // files, so unrelated dirty files never enter the PR. We only
        // guard against a dirty staging area (which WOULD commit).
        'echo "working-tree files currently dirty (informational):"',
        'git diff --name-only | head -20 || true',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: staging area dirty — running this workflow would include those staged files in its commit. Unstage them first."',
        '  git diff --cached --stat',
        '  exit 1',
        'fi',
        'gh auth status >/dev/null 2>&1 || (echo "ERROR: gh CLI not authenticated"; exit 1)',
        'go version',
        'echo PREFLIGHT_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('collect-context', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      // Uses `.join('\n')` — the heredoc at the end cannot be chained with
      // `&&`, and the production-log samples contain `(x N/second)` which
      // the shell would try to interpret as a subshell if it saw them
      // outside heredoc context.
      command: [
        'set -e',
        'echo "=== mountsync/syncer.go HTTPClient surface ==="',
        'rg -n "type HTTPClient struct|func \\\\(c \\\\*HTTPClient\\\\)|WriteFile\\\\(|DeleteFile\\\\(|BulkWrite|If-Match|httpClient\\\\.Do|baseRevision|Timeout:" internal/mountsync/syncer.go | head -80 || true',
        'echo',
        'echo "=== server-side bulk endpoint ==="',
        'rg -n "bulk_write|handleBulkWrite|BulkWriteFile|BulkWriteError|/fs/bulk" internal/httpapi/server.go | head -30 || true',
        'echo',
        'echo "=== BulkWriteFile type ==="',
        'rg -n "BulkWriteFile|BulkWriteError|BulkWriteResponse" internal/relayfile/ 2>/dev/null | head -20 || true',
        'echo',
        'echo "=== Syncer write entrypoints ==="',
        'rg -n "func \\\\(s \\\\*Syncer\\\\).*Write|handleLocalWriteOrCreate|handleLocalChange|pushLocal" internal/mountsync/syncer.go | head -20 || true',
        'echo',
        'echo "=== existing bulk test coverage (if any) ==="',
        'rg -n "Bulk|bulk" internal/mountsync/syncer_test.go internal/mountsync/http_client_test.go 2>/dev/null | head -20 || true',
        'echo',
        'echo "=== sample error storm from production mount logs ==="',
        'cat <<EOF',
        '2026/04/24 14:00:37 mount sync cycle failed: context deadline exceeded',
        '2026/04/24 14:01:15 mount local change failed: context deadline exceeded   [repeated N/second]',
        '2026/04/24 13:13:19 mount local change failed: http 412 precondition_failed: missing If-Match header',
        'EOF',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 2: Boundary doc
    // ────────────────────────────────────────────────────────────────
    .step('define-boundary', {
      agent: 'architect',
      dependsOn: ['collect-context'],
      task: [
        'You are scoping a bounded migration of relayfile-mount from per-file HTTP sync to bulk sync. The full investigation is in the commit message area of AgentWorkforce/cloud#342. The problem summary is in the collect-context output.',
        '',
        'Context output:',
        '{{steps.collect-context.output}}',
        '',
        'Write docs/mount-bulk-migration-boundary.md with EXACTLY these sections:',
        '',
        '1. Files touched (list every file, why, and whether create/modify)',
        '2. HTTPClient.WriteFilesBulk signature (exact Go signature, matching BulkWriteFile / BulkWriteResponse types used server-side)',
        '3. Syncer batching strategy — when to batch vs when to stay per-file. Include the concurrency rules (Syncer already serializes writes within a cycle; the batching layer sits BETWEEN the per-file handler and the HTTPClient, aggregating writes observed during one sync cycle and flushing them in one request at end of cycle, OR every N files).',
        '4. First-write 412 fix — describe the exact change to handleLocalWriteOrCreate or the HTTPClient write path. Options: send If-Match: * on create, or fetch the current revision before writing when our baseRevision is empty/"0". Pick one and justify.',
        '5. New test cases (3-5 bullet points, each a specific scenario like "bulk write of 5 files produces exactly one HTTP call to /fs/bulk")',
        '6. Deliberately-out-of-scope items (e.g. FUSE mount path, relayfile-server changes). Keep this list short.',
        '7. Acceptance gates — exact go test command(s) and grep assertions that must pass before commit.',
        '',
        'Do NOT touch any .go file yet. This step only writes the boundary doc. End with the literal token BOUNDARY_READY on its own line.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/mount-bulk-migration-boundary.md' },
      retries: 1,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 3: Implementation (single impl agent since relayfile repo is small)
    // ────────────────────────────────────────────────────────────────
    .step('implement', {
      agent: 'impl',
      dependsOn: ['define-boundary'],
      task: [
        'Implement the migration per docs/mount-bulk-migration-boundary.md.',
        '',
        'IMPORTANT — prior-run review feedback to address on this pass:',
        '',
        'If docs/mount-bulk-migration-review.md exists from a previous run,',
        'read it first. The reviewer flagged two specific issues that MUST be',
        'fixed on this pass:',
        '',
        '  (a) Scope creep: internal/httpapi/ changes were outside the',
        '      boundary. Those files have already been stashed — do NOT',
        '      re-edit internal/httpapi/ on this pass.',
        '  (b) Missing revision read-back after bulk flush. The current',
        '      reconcileBulkWrite at syncer.go:~691 copies',
        '      `pendingWrite.tracked.Revision` — that is the STALE pre-write',
        '      revision. After a successful bulk write, the server returns',
        '      the new revision for each file in response.Files (or',
        '      response.Results, whatever the actual field is — rg the',
        '      response type). You MUST copy THAT revision into',
        '      s.state.Files[path].Revision, not the pre-write one. Without',
        '      this, the next write will send If-Match: <stale> and hit 412.',
        '      Add a test asserting the tracked revision after a bulk write',
        '      matches the response revision, not the pre-write one.',
        '',
        'Constraints:',
        '1. Only modify the files listed in the boundary doc. Do not edit unrelated code.',
        '2. Follow existing code style in internal/mountsync/syncer.go — tab indentation, existing naming, existing error-wrapping patterns.',
        '3. The bulk request must reuse the same BulkWriteFile / BulkWriteError types the server already defines; import them if they are in a shared package (rg the repo first).',
        '4. Preserve backward compatibility — if Client interface is exported, a bulk method must be additive (or provide a default implementation).',
        '5. When batching, preserve error semantics: if one file in a bulk request fails, the Syncer must be able to reconcile per-file (bulk response includes per-file errors).',
        '6. After a successful bulk flush, update the Syncer\'s tracked revisions from the response so the next write sends correct If-Match. Cover this with a test (the reviewer will check).',
        '7. gofmt everything you touch.',
        '8. Do NOT run go test yet — the next step does that.',
        '',
        'When done, run `git diff --stat` and print it so the next step can see what changed. End with IMPLEMENTATION_READY.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 1,
    })

    .step('verify-edits-landed', {
      type: 'deterministic',
      dependsOn: ['implement'],
      command: [
        'set -e',
        'echo "=== git diff --stat ==="',
        'git diff --stat internal/mountsync/ cmd/relayfile-mount/',
        'echo',
        'echo "=== sanity grep: bulk method present ==="',
        'rg -n "WriteFilesBulk|writeFilesBulk|BulkWrite" internal/mountsync/syncer.go | head -10',
        'echo',
        'echo "=== sanity grep: If-Match first-write change present ==="',
        'rg -n "If-Match" internal/mountsync/syncer.go | head -10',
        'echo',
        'echo "=== gofmt check ==="',
        'gofmt -l internal/mountsync/ cmd/relayfile-mount/ | tee /tmp/gofmt-out',
        'if [ -s /tmp/gofmt-out ]; then',
        '  echo "ERROR: unformatted files above"',
        '  exit 1',
        'fi',
        'echo EDITS_VERIFIED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 4: Test-fix-rerun on existing suite (regression gate)
    // ────────────────────────────────────────────────────────────────
    .step('run-regression-tests', {
      type: 'deterministic',
      dependsOn: ['verify-edits-landed'],
      command: 'go test ./internal/mountsync/... -count=1 -timeout 120s 2>&1 | tail -120',
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-regressions', {
      agent: 'impl',
      dependsOn: ['run-regression-tests'],
      task: [
        'The existing mountsync test suite was run. Read the output — if everything passed (look for "ok  \\tgithub.com/..."), do nothing and end with REGRESSIONS_OK.',
        '',
        'Test output:',
        '{{steps.run-regression-tests.output}}',
        '',
        'If there are failures:',
        '1. Read the failing test to understand what it expects',
        '2. Find the file(s) you changed that broke it',
        '3. Fix either the test (if the expectation is stale given the new bulk path) or your code (if you broke a real contract)',
        '4. Re-run: go test ./internal/mountsync/... -count=1 -timeout 120s',
        '5. Repeat until all tests pass',
        '6. End with REGRESSIONS_OK',
        '',
        'Rule: do not "fix" a test by deleting it. If the test expected per-file HTTP calls and now sees bulk, change the test to assert the new behavior correctly — do not weaken it.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 2,
    })

    .step('run-regression-tests-final', {
      type: 'deterministic',
      dependsOn: ['fix-regressions'],
      command: 'go test ./internal/mountsync/... -count=1 -timeout 120s 2>&1 | tail -40',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 5: New tests for bulk behavior + 412 fix
    // ────────────────────────────────────────────────────────────────
    .step('add-new-tests', {
      agent: 'impl',
      dependsOn: ['run-regression-tests-final'],
      task: [
        'Add new tests for the bulk migration in internal/mountsync/syncer_test.go (or http_client_test.go where appropriate). The boundary doc lists the scenarios in section 5.',
        '',
        'At minimum, each of these scenarios must have a test:',
        '1. HTTPClient.WriteFilesBulk sends ONE POST to /v1/workspaces/:id/fs/bulk with all files in the request body',
        '2. When the Syncer observes N>1 local changes in one cycle, exactly one bulk request is made (use httptest.Server to count requests)',
        '3. If bulk response reports a per-file error, the Syncer marks that file as dirty for retry without failing the whole cycle',
        '4. First-write to a path that already exists remotely no longer returns 412 (the If-Match fix)',
        '',
        'Follow the existing test style in syncer_test.go — use httptest.NewServer, atomic counters, require.Equal, etc.',
        '',
        'Do not run the tests yet — the next deterministic step will. End with NEW_TESTS_READY.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 1,
    })

    .step('run-new-tests', {
      type: 'deterministic',
      dependsOn: ['add-new-tests'],
      command: 'go test ./internal/mountsync/... -run "TestBulk|TestSyncOnceBatches|TestFirstWrite" -count=1 -timeout 120s -v 2>&1 | tail -80',
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-new-tests', {
      agent: 'impl',
      dependsOn: ['run-new-tests'],
      task: [
        'Fix any failures in the new tests. Read the output, iterate on the code or the test until they pass. Re-run:',
        '  go test ./internal/mountsync/... -run "TestBulk|TestSyncOnceBatches|TestFirstWrite" -count=1 -timeout 120s -v',
        '',
        'Test output:',
        '{{steps.run-new-tests.output}}',
        '',
        'End with NEW_TESTS_OK.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 2,
    })

    .step('run-new-tests-final', {
      type: 'deterministic',
      dependsOn: ['fix-new-tests'],
      command: 'go test ./internal/mountsync/... -count=1 -timeout 120s 2>&1 | tail -60',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 6: Build the binary + vet
    // ────────────────────────────────────────────────────────────────
    .step('go-vet', {
      type: 'deterministic',
      dependsOn: ['run-new-tests-final'],
      command: 'go vet ./internal/mountsync/... ./cmd/relayfile-mount/... 2>&1',
      captureOutput: true,
      failOnError: true,
    })

    .step('go-build-mount', {
      type: 'deterministic',
      dependsOn: ['go-vet'],
      command: 'go build -o /tmp/relayfile-mount-060 ./cmd/relayfile-mount && ls -la /tmp/relayfile-mount-060 && rm -f /tmp/relayfile-mount-060 && echo BUILD_OK',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 7: End-to-end proof via httptest
    // ────────────────────────────────────────────────────────────────
    .step('e2e-bulk-proof', {
      agent: 'impl',
      dependsOn: ['go-build-mount'],
      task: [
        'Write an end-to-end test that proves the migration reduces HTTP call volume. Add it to internal/mountsync/syncer_test.go as TestBulkMigrationReducesHTTPCalls.',
        '',
        'Shape:',
        '1. httptest.NewServer counting requests per path (atomic counter per endpoint)',
        '2. Create a Syncer, write 10 files to the local mount in one cycle',
        '3. Run one sync iteration',
        '4. Assert: exactly one request to /v1/workspaces/:id/fs/bulk, zero requests to the per-file /v1/workspaces/:id/fs/file endpoint',
        '',
        'This is the 80-to-100 proof — before the migration this test would have produced 10 per-file requests.',
        '',
        'When done, run:',
        '  go test ./internal/mountsync/... -run TestBulkMigrationReducesHTTPCalls -v -timeout 120s',
        '',
        'and confirm it passes. End with E2E_OK.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 1,
    })

    .step('e2e-bulk-proof-final', {
      type: 'deterministic',
      dependsOn: ['e2e-bulk-proof'],
      command: 'go test ./internal/mountsync/... -run TestBulkMigrationReducesHTTPCalls -v -timeout 120s 2>&1 | tail -40',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 8: Review + commit + PR
    // ────────────────────────────────────────────────────────────────
    .step('review', {
      agent: 'reviewer',
      dependsOn: ['e2e-bulk-proof-final'],
      task: [
        'You are reviewing the CURRENT state of the diff, not any prior review.',
        'If docs/mount-bulk-migration-review.md already exists from a previous',
        'run, IGNORE ITS CONTENTS — overwrite it with a fresh verdict based',
        'only on the current `git diff` output. Do not carry forward claims',
        'from a prior review without verifying them against the current tree.',
        '',
        'Review the diff against docs/mount-bulk-migration-boundary.md. Check:',
        '',
        '1. Every file modified is listed in the boundary doc (no scope creep). Run `git diff --name-only` and cross-reference against section 1 of the boundary doc.',
        '2. The batching path is concurrency-safe — no shared slice mutated without a lock, no races with the Syncer\'s existing cycle-level serialization.',
        '3. The 412 first-write fix does not introduce an unbounded GET before every write.',
        '4. Error handling: per-file errors from the bulk response are surfaced back to the Syncer for retry; the whole cycle does not fail on one bad file.',
        '5. Post-flush revision read-back is implemented — after a successful bulk write, s.state.Files[path].Revision is updated FROM THE RESPONSE, not from the pre-write tracked value. Read reconcileBulkWrite (or whatever it is called now) carefully and confirm this.',
        '6. gofmt / go vet / go build all passed (they did — this is a sanity check for the reviewer reading the code).',
        '',
        'Read the final diff with:',
        '  git diff --stat',
        '  git diff --name-only',
        '  git diff internal/mountsync/syncer.go',
        '',
        'Write your verdict to docs/mount-bulk-migration-review.md as a fresh document. End with either REVIEW_APPROVED (on its own line, nothing after) or REVIEW_CHANGES_REQUESTED (followed by an explicit list). If changes are requested, be specific enough that the impl agent can fix them on the next pass.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/mount-bulk-migration-review.md' },
      retries: 1,
    })

    .step('verify-review-approved', {
      type: 'deterministic',
      dependsOn: ['review'],
      command: 'grep -q "^REVIEW_APPROVED" docs/mount-bulk-migration-review.md && echo APPROVED || (echo "Review did not approve — see docs/mount-bulk-migration-review.md"; exit 1)',
      captureOutput: true,
      failOnError: true,
    })

    .step('commit', {
      type: 'deterministic',
      dependsOn: ['verify-review-approved'],
      command: [
        'set -e',
        'git add internal/mountsync/syncer.go internal/mountsync/syncer_test.go internal/mountsync/http_client_test.go docs/mount-bulk-migration-boundary.md docs/mount-bulk-migration-review.md workflows/060-mount-bulk-sync-migration.ts',
        // Only add these if they exist (cmd/relayfile-mount may or may not be touched)
        'git add cmd/relayfile-mount/main.go 2>/dev/null || true',
        'MSG=$(mktemp)',
        'printf "%s\\n" "fix(mount): migrate to bulk sync + fix first-write 412" "" "Per-file HTTP sync in the mount daemon tar-pits under concurrent" "agent file writes (seen in AgentWorkforce/cloud workflow runs as" "\\"context deadline exceeded\\" storms). Server already exposes" "/v1/workspaces/:id/fs/bulk — migrate the HTTPClient + Syncer" "to batch writes observed in one sync cycle into a single request." "" "Also fix the first-write 412 \\"missing If-Match header\\" case" "(exact fix in docs/mount-bulk-migration-boundary.md section 4)." "" "Validated by new TestBulkMigrationReducesHTTPCalls which proves" "10 local file writes produce exactly one /fs/bulk request." > "$MSG"',
        'git commit -F "$MSG"',
        'rm -f "$MSG"',
        'echo COMMIT_OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .step('push-and-pr', {
      type: 'deterministic',
      dependsOn: ['commit'],
      command: [
        'set -e',
        'BRANCH=$(git rev-parse --abbrev-ref HEAD)',
        'git push -u origin "$BRANCH"',
        'BODY=$(mktemp)',
        'printf "%s\\n" "## Summary" "" "Migrate relayfile-mount from per-file HTTP sync to the existing /fs/bulk endpoint, and fix the first-write 412 \\"missing If-Match header\\" error." "" "## Why" "" "Per-file sync produces \\"context deadline exceeded\\" storms under concurrent agent writes — seen in AgentWorkforce/cloud#342 investigation where a workflow modified dozens of small files per second and mount\\u2019s 15s per-call timeout cascaded." "" "## What changed" "" "See docs/mount-bulk-migration-boundary.md for the exact scope and docs/mount-bulk-migration-review.md for the approved review." "" "## Proof" "" "TestBulkMigrationReducesHTTPCalls asserts 10 local file writes produce exactly 1 request to /fs/bulk and 0 requests to the per-file endpoint." "" "Authored by workflows/060-mount-bulk-sync-migration.ts." > "$BODY"',
        'gh pr create --base main --head "$BRANCH" --title "fix(mount): migrate to bulk sync + fix first-write 412" --body-file "$BODY" | tee /tmp/pr-url.txt',
        'rm -f "$BODY"',
        'echo',
        'echo "PR: $(cat /tmp/pr-url.txt)"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
    .run({ cwd: process.cwd() });

  console.log('Workflow status:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
