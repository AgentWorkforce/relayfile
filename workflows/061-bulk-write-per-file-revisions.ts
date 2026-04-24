/**
 * 061-bulk-write-per-file-revisions.ts
 *
 * Extend the relayfile server's `/v1/workspaces/:id/fs/bulk` response to
 * include per-file results (path + new revision + contentType) so the
 * mount daemon can reconcile tracked revisions WITHOUT a follow-up
 * GET /fs/file per file. This is the complement to workflow 060 — after
 * this lands, `TestBulkMigrationReducesHTTPCalls` tightens from "0 POST
 * /fs/file" to "0 total requests on /fs/file".
 *
 * Context:
 *   - 060 shipped the mount-side bulk migration. In production today the
 *     server's /fs/bulk response only returns
 *     {written, errorCount, errors, correlationId} — no per-file new
 *     revisions. So `reconcileBulkWrite` (mount) falls back to a per-file
 *     GET /fs/file per batched write to learn the new revision for next
 *     If-Match. A 10-file batch = 1 POST + 10 GETs.
 *   - The server already knows the new revision when it writes each file
 *     (Store.BulkWrite / Store.BulkWriteFork return per-file results
 *     internally). Extending the HTTP response to carry them is a small
 *     server-side change that eliminates the GET fallback entirely.
 *
 * Run from the relayfile repo root:
 *   agent-relay run workflows/061-bulk-write-per-file-revisions.ts
 */
import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

async function main() {
  const result = await workflow('061-bulk-write-per-file-revisions')
    .description(
      'Return per-file results (path + revision + contentType) from /fs/bulk so the mount daemon stops falling back to per-file GETs after bulk writes. Includes server-side changes, wire-type additions, mount-side reconcileBulkWrite simplification, and tightened e2e proof.',
    )
    .pattern('dag')
    .channel('wf-061-bulk-per-file-revisions')
    .maxConcurrency(3)
    .timeout(7_200_000)

    .agent('architect', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'analyst',
      role: 'Defines the exact server-side response shape change, wire-type updates, and mount-side simplification path before any code is touched.',
      retries: 1,
    })
    .agent('impl', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role: 'Implements Go changes across internal/httpapi, internal/relayfile, and internal/mountsync per the boundary doc.',
      retries: 2,
    })
    .agent('reviewer', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'reviewer',
      role: 'Checks the server response change preserves backward compatibility and the mount reconciliation matches the new shape.',
      retries: 1,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 1: Preflight + context
    // ────────────────────────────────────────────────────────────────
    .step('preflight', {
      type: 'deterministic',
      // This workflow STACKS on top of PR #63 (`fix/mount-bulk-sync-migration`)
      // until #63 merges to main. Starting from main would miss the bulk
      // client + types.go that 061 depends on. The final PR targets
      // `fix/mount-bulk-sync-migration` as base; GitHub will auto-retarget
      // to main once #63 merges.
      command: [
        'set -e',
        'BASE_BRANCH="fix/mount-bulk-sync-migration"',
        'WF_BRANCH="fix/bulk-write-per-file-revisions"',
        'BRANCH=$(git rev-parse --abbrev-ref HEAD)',
        'echo "current branch: $BRANCH"',
        'echo "stack base: $BASE_BRANCH"',
        'echo "workflow branch: $WF_BRANCH"',
        // If already on the workflow branch (resume case), do nothing.
        'if [ "$BRANCH" = "$WF_BRANCH" ]; then',
        '  echo "already on $WF_BRANCH — ok"',
        'elif [ "$BRANCH" = "$BASE_BRANCH" ]; then',
        '  git fetch origin "$BASE_BRANCH"',
        '  git checkout -b "$WF_BRANCH"',
        '  echo "created $WF_BRANCH on top of $BASE_BRANCH"',
        'elif [ "$BRANCH" = "main" ]; then',
        '  git fetch origin "$BASE_BRANCH"',
        '  git checkout "$BASE_BRANCH"',
        '  git checkout -b "$WF_BRANCH"',
        '  echo "checked out $BASE_BRANCH then created $WF_BRANCH"',
        'else',
        '  echo "ERROR: on unexpected branch \\"$BRANCH\\". Checkout $BASE_BRANCH or $WF_BRANCH first."',
        '  exit 1',
        'fi',
        'echo "working-tree files currently dirty (informational):"',
        'git diff --name-only | head -20 || true',
        'if ! git diff --cached --quiet; then',
        '  echo "ERROR: staging area dirty — unstage first"',
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
      command: [
        'set -e',
        'echo "=== current /fs/bulk server response shape ==="',
        'awk "/func .s .Server. handleBulkWrite/,/^}$/" internal/httpapi/server.go | tail -20 || true',
        'echo',
        'echo "=== Store.BulkWrite / Store.BulkWriteFork return types ==="',
        'rg -n "func .s .Store. BulkWrite\\\\b|func .s .Store. BulkWriteFork" internal/relayfile/store.go || true',
        'rg -n "BulkWriteFile|BulkWriteError" internal/relayfile/store.go | head -10 || true',
        'echo',
        'echo "=== mount-side reconcile that will simplify ==="',
        'awk "/func .s .Syncer. reconcileBulkWrite/,/^}$/" internal/mountsync/syncer.go | head -40 || true',
        'echo',
        'echo "=== mount-side wire-type aliases ==="',
        'cat internal/mountsync/types.go 2>/dev/null || true',
        'echo',
        'echo "=== the assertion that will tighten ==="',
        'rg -n "fsFilePostCount|bulk migration verified" internal/mountsync/syncer_test.go || true',
        'echo',
        'echo "=== existing server-side bulk tests ==="',
        'rg -n "handleBulkWrite|TestBulkWrite|/fs/bulk" internal/httpapi/server_test.go 2>/dev/null | head -20 || true',
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
        'Scope the server-side bulk response change and the matching mount reconciliation simplification.',
        '',
        'Collected context:',
        '{{steps.collect-context.output}}',
        '',
        'Write docs/bulk-per-file-revisions-boundary.md with EXACTLY these sections:',
        '',
        '1. Files touched — list every file with reason and create/modify:',
        '     - internal/httpapi/server.go (handleBulkWrite returns per-file results)',
        '     - internal/httpapi/server_test.go (new test asserting new fields present)',
        '     - internal/relayfile/store.go (BulkWrite/BulkWriteFork must surface per-file revisions — they already compute them internally; now return them)',
        '     - internal/relayfile/ (any shared response-type structs)',
        '     - internal/mountsync/types.go (BulkWriteResponse — field already exists as Results/Files; verify alignment)',
        '     - internal/mountsync/syncer.go (reconcileBulkWrite: stop falling back to ReadFile when revision is present in response)',
        '     - internal/mountsync/syncer_test.go (tighten TestBulkMigrationReducesHTTPCalls assertion: 0 total requests on /fs/file)',
        '2. New wire shape for /fs/bulk response — exact JSON:',
        '     {',
        '       "written": int,',
        '       "errorCount": int,',
        '       "errors": [...],',
        '       "results": [{"path": "...", "revision": "...", "contentType": "..."}],',
        '       "correlationId": "..."',
        '     }',
        '3. Backward compatibility — what happens if a client on an old SDK reads the new response? (answer: extra field is ignored; also the existing mount code already handles missing `results` by falling back to ReadFile). Document this.',
        '4. Store.BulkWrite signature change — propose the new return type (likely `(written int, results []BulkWriteResult, errors []BulkWriteError)`) and confirm Store.BulkWriteFork gets the same treatment.',
        '5. Mount-side change — remove the fallback ReadFile in reconcileBulkWrite when the response supplies a revision. Keep the fallback for the empty-revision case (defensive).',
        '6. Test updates:',
        '     - Server: a test asserting the new fields are populated for successful writes AND absent for failed ones.',
        '     - Mount: tighten TestBulkMigrationReducesHTTPCalls so it asserts 0 total requests on /fs/file (not just 0 POSTs). Remove the post-filter counter since it is no longer needed.',
        '7. Deliberately out of scope — FUSE mount path, SDK/TS bindings, observer/dashboard.',
        '8. Acceptance gates — exact commands that must pass:',
        '     - go test ./internal/httpapi/... -count=1 -timeout 120s',
        '     - go test ./internal/mountsync/... -count=1 -timeout 120s',
        '     - go build ./...',
        '',
        'Do NOT touch any .go file. End with the literal token BOUNDARY_READY on its own line.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/bulk-per-file-revisions-boundary.md' },
      retries: 1,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 3: Implement (server + store + mount in one pass — tight coupling)
    // ────────────────────────────────────────────────────────────────
    .step('implement', {
      agent: 'impl',
      dependsOn: ['define-boundary'],
      task: [
        'Implement the changes per docs/bulk-per-file-revisions-boundary.md.',
        '',
        'If docs/bulk-per-file-revisions-review.md exists from a previous run,',
        'read it first and address each REVIEW_CHANGES_REQUESTED item on',
        'this pass.',
        '',
        'Constraints:',
        '1. Only modify files listed in the boundary doc.',
        '2. Preserve backward compatibility — the existing {written, errorCount, errors, correlationId} fields must remain unchanged. Only ADD a new `results` field.',
        '3. Store.BulkWrite / Store.BulkWriteFork should return the new per-file results in addition to (not instead of) existing return values. If this changes the signature, update all call sites accordingly.',
        '4. On the mount side, the existing types.go defines BulkWriteResponse with Results []BulkWriteResult already. Confirm field names align with the server JSON tags; adjust if mismatched.',
        '5. reconcileBulkWrite: when response supplies a revision for the path, use it directly and do NOT call s.client.ReadFile. Keep the ReadFile fallback only for the empty-revision defensive case.',
        '6. gofmt everything touched.',
        '7. Do NOT run tests yet — the next step handles that.',
        '',
        'Print `git diff --stat` when done. End with IMPLEMENTATION_READY.',
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
        'git diff --stat internal/ 2>/dev/null || true',
        'echo',
        'echo "=== sanity grep: server returns results field ==="',
        'rg -n "results.*\\\\[\\\\]|Results.*\\\\[\\\\]BulkWriteResult|\\"results\\":" internal/httpapi/server.go | head -10 || true',
        'echo',
        'echo "=== sanity grep: Store returns per-file results ==="',
        'rg -n "BulkWriteResult|results \\\\[\\\\]" internal/relayfile/store.go | head -10 || true',
        'echo',
        'echo "=== sanity grep: mount uses response revision directly ==="',
        'rg -n "revisionsByPath|response.Results" internal/mountsync/syncer.go | head -10 || true',
        'echo',
        'echo "=== gofmt check (diff-scoped) ==="',
        // Scope to files this workflow actually touched so pre-existing
        // drift in unrelated files (e.g. internal/relayfile/adapters_test.go
        // column-alignment) does not fail the gate.
        'git diff --name-only | grep "\\.go$" | xargs -r gofmt -l 2>&1 | tee /tmp/gofmt-out-061',
        'if [ -s /tmp/gofmt-out-061 ]; then',
        '  echo "ERROR: workflow-touched files above are unformatted"',
        '  exit 1',
        'fi',
        'echo EDITS_VERIFIED',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 4: Tests — regression + new
    // ────────────────────────────────────────────────────────────────
    .step('run-regression-tests', {
      type: 'deterministic',
      dependsOn: ['verify-edits-landed'],
      command: 'go test ./internal/httpapi/... ./internal/mountsync/... ./internal/relayfile/... -count=1 -timeout 180s 2>&1 | tail -120',
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-regressions', {
      agent: 'impl',
      dependsOn: ['run-regression-tests'],
      task: [
        'If all tests passed ("ok  " for each package, no FAIL), do nothing and end with REGRESSIONS_OK.',
        '',
        'Test output:',
        '{{steps.run-regression-tests.output}}',
        '',
        'If there are failures, fix them and re-run:',
        '  go test ./internal/httpapi/... ./internal/mountsync/... ./internal/relayfile/... -count=1 -timeout 180s',
        '',
        'Do not weaken tests to make them pass — if the test expectation is stale given the new response shape, update the assertion correctly.',
        '',
        'End with REGRESSIONS_OK.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 2,
    })

    .step('run-regression-tests-final', {
      type: 'deterministic',
      dependsOn: ['fix-regressions'],
      command: 'go test ./internal/httpapi/... ./internal/mountsync/... ./internal/relayfile/... -count=1 -timeout 180s 2>&1 | tail -60',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 5: E2E proof — tightened assertion
    // ────────────────────────────────────────────────────────────────
    .step('tighten-e2e-proof', {
      agent: 'impl',
      dependsOn: ['run-regression-tests-final'],
      task: [
        'Update internal/mountsync/syncer_test.go TestBulkMigrationReducesHTTPCalls so the /fs/file assertion tightens from "0 POST /fs/file" to "0 total requests on /fs/file". Remove the fsFilePostCount counter (no longer needed). Drop the comment block explaining the GET fallback — the server now returns revisions inline. Keep the test structure otherwise identical.',
        '',
        'Then run:',
        '  go test ./internal/mountsync/... -run TestBulkMigrationReducesHTTPCalls -v -timeout 120s',
        '',
        'Assert it passes and prints the tightened log line. End with E2E_OK.',
      ].join('\n'),
      verification: { type: 'exit_code' },
      retries: 1,
    })

    .step('tighten-e2e-proof-final', {
      type: 'deterministic',
      dependsOn: ['tighten-e2e-proof'],
      command: 'go test ./internal/mountsync/... -run TestBulkMigrationReducesHTTPCalls -v -timeout 120s 2>&1 | tail -20',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 6: Build + vet
    // ────────────────────────────────────────────────────────────────
    .step('go-vet', {
      type: 'deterministic',
      dependsOn: ['tighten-e2e-proof-final'],
      command: 'go vet ./... 2>&1 | tail -20',
      captureOutput: true,
      failOnError: true,
    })

    .step('go-build', {
      type: 'deterministic',
      dependsOn: ['go-vet'],
      command: 'go build ./... 2>&1 && echo BUILD_OK',
      captureOutput: true,
      failOnError: true,
    })

    // ────────────────────────────────────────────────────────────────
    // Phase 7: Review + commit + PR
    // ────────────────────────────────────────────────────────────────
    .step('review', {
      agent: 'reviewer',
      dependsOn: ['go-build'],
      task: [
        'You are reviewing the CURRENT state of the diff, not any prior review.',
        'If docs/bulk-per-file-revisions-review.md exists from a previous',
        'run, IGNORE ITS CONTENTS — overwrite with a fresh verdict.',
        '',
        'Review against docs/bulk-per-file-revisions-boundary.md. Check:',
        '',
        '1. Every file modified is listed in section 1 of the boundary doc (no scope creep). Run `git diff --name-only` and cross-reference.',
        '2. Backward compatibility — verify the new `results` field is ADDITIVE. The existing {written, errorCount, errors, correlationId} response fields must be unchanged in name, type, and semantics. An older client that ignores `results` must still work.',
        '3. Store.BulkWrite / Store.BulkWriteFork signature changes are applied consistently at all call sites.',
        '4. Mount reconcileBulkWrite correctly prefers response-supplied revisions and only falls back to ReadFile on missing values.',
        '5. Tests exist for: server response includes results on success, server response excludes results on error (or lists the error path correctly), mount skips ReadFile when revision is present.',
        '6. E2E proof is tightened — TestBulkMigrationReducesHTTPCalls now asserts 0 total requests on /fs/file (not just 0 POSTs).',
        '7. gofmt / go vet / go build passed.',
        '',
        'Read the diff:',
        '  git diff --stat',
        '  git diff --name-only',
        '  git diff internal/httpapi/server.go',
        '  git diff internal/relayfile/store.go',
        '  git diff internal/mountsync/syncer.go',
        '',
        'Write fresh verdict to docs/bulk-per-file-revisions-review.md. End with REVIEW_APPROVED on its own line, or REVIEW_CHANGES_REQUESTED followed by a specific list.',
      ].join('\n'),
      verification: { type: 'file_exists', value: 'docs/bulk-per-file-revisions-review.md' },
      retries: 1,
    })

    .step('verify-review-approved', {
      type: 'deterministic',
      dependsOn: ['review'],
      command: 'grep -q "^REVIEW_APPROVED" docs/bulk-per-file-revisions-review.md && echo APPROVED || (echo "Review did not approve — see docs/bulk-per-file-revisions-review.md"; exit 1)',
      captureOutput: true,
      failOnError: true,
    })

    .step('commit', {
      type: 'deterministic',
      dependsOn: ['verify-review-approved'],
      command: [
        'set -e',
        'git add internal/httpapi/server.go internal/httpapi/server_test.go internal/relayfile/store.go internal/mountsync/types.go internal/mountsync/syncer.go internal/mountsync/syncer_test.go docs/bulk-per-file-revisions-boundary.md docs/bulk-per-file-revisions-review.md workflows/061-bulk-write-per-file-revisions.ts',
        // Catch any other .go file the impl agent may have touched.
        'git add internal/ 2>/dev/null || true',
        'MSG=$(mktemp)',
        'printf "%s\\n" "feat(bulk): return per-file revisions from /fs/bulk" "" "The existing /fs/bulk response only returned {written, errorCount, errors," "correlationId}. Mount clients had to follow every bulk write with N GET /fs/file" "requests to learn the new revisions for If-Match. That made the post-migration" "total request count N+1 for an N-file batch." "" "Extend the response with a per-file results array:" "  {\\"path\\":..., \\"revision\\":..., \\"contentType\\":...}" "" "Purely additive — existing fields unchanged. Mount now uses the inline revision" "and skips the ReadFile fallback. The e2e proof TestBulkMigrationReducesHTTPCalls" "tightens from \\"0 POST /fs/file\\" to \\"0 total requests on /fs/file\\"." > "$MSG"',
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
        'printf "%s\\n" "## Summary" "" "Extend /fs/bulk response with per-file results so mount clients do not need a follow-up GET /fs/file per batched write to learn new revisions." "" "## Why" "" "Workflow 060 migrated the mount daemon from per-file POST /fs/file to POST /fs/bulk. That eliminated per-file WRITES but left per-file GETs for revision read-back, because the /fs/bulk response did not carry per-file revisions. Total request count for an N-file batch was still N+1." "" "Server already computes per-file revisions in Store.BulkWrite / Store.BulkWriteFork. This PR surfaces them in the HTTP response as an additive results array." "" "## Backward compatibility" "" "Purely additive — existing response fields unchanged. Clients on older SDKs ignore the extra field; the mount code already handles missing results by falling back to ReadFile." "" "## Proof" "" "TestBulkMigrationReducesHTTPCalls now asserts 0 total requests on /fs/file (previously only 0 POST /fs/file, because GET fallbacks were expected). See docs/bulk-per-file-revisions-boundary.md for scope and docs/bulk-per-file-revisions-review.md for the approved review." "" "Authored by workflows/061-bulk-write-per-file-revisions.ts." > "$BODY"',
        // Stack on top of PR #63 until it merges. GitHub auto-retargets
        // this PR to main when the base branch merges.
        'gh pr create --base fix/mount-bulk-sync-migration --head "$BRANCH" --title "feat(bulk): return per-file revisions from /fs/bulk" --body-file "$BODY" | tee /tmp/pr-url-061.txt',
        'rm -f "$BODY"',
        'echo',
        'echo "PR: $(cat /tmp/pr-url-061.txt)"',
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
