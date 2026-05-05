// Writeback Reliability — relayfile-side phases (1 + 4) of cloud spec PR #448.
//
// Phase 1: mount daemon currently drops local writes silently — a user edits
//          a content.md, pendingWriteback drains 1 → 0, but the cloud never
//          received the PUT. Fix: every non-2xx response from the cloud must
//          be logged at WARN, increment `failedWritebacks` in state.json, and
//          dead-letter to ~/relayfile-mount/.relay/dead-letter/<opId>.json on
//          persistent failure.
//
// Phase 4: add `relayfile writeback status` and `relayfile writeback retry`
//          subcommands so agents can introspect failures from the CLI rather
//          than having to read CloudWatch.
//
// Pattern: relay-80-100. The workflow does not commit until:
//   - new Go test exercises a 4xx response from a httptest server and asserts
//     a dead-letter file gets written
//   - `go build ./...` clean
//   - `go test ./...` green (existing suite + the new tests)
//   - `relayfile writeback status` against a fixture mount surfaces the right
//     counts (deterministic shell-driven E2E)
//
// Team split (per writing-agent-relay-workflows skill rule §5):
//   lead / impl / tester → codex
//   reviewer            → claude
//
// Branch:   fix/writeback-reliability
// Repo:     relayfile (this repo, no worktree split)
// Spec:     AgentWorkforce/cloud docs/architecture/writeback-reliability.md (PR #448)

import { workflow } from '@agent-relay/sdk/workflows';
import { ClaudeModels, CodexModels } from '@agent-relay/config';

const BRANCH = 'fix/writeback-reliability';
const MAIN_GO = 'cmd/relayfile-cli/main.go';
const MAIN_TEST_GO = 'cmd/relayfile-cli/main_test.go';
const DAEMON_TEST_GO = 'cmd/relayfile-cli/writeback_daemon_test.go';

async function main() {
  const result = await workflow('057-writeback-reliability-mount-and-cli')
    .description(
      'Fix mount daemon silent writeback drop (phase 1) and add writeback status / retry CLI surfaces (phase 4) per AgentWorkforce/cloud#448.',
    )
    .pattern('dag')
    .channel('wf-057-writeback-reliability')
    .maxConcurrency(5)
    .timeout(3_600_000)

    // ── Agents (codex implements, claude reviews) ────────────────────────
    .agent('lead-codex', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role:
        'Lead + coordinator. Posts the work-split, monitors the channel, calls the merge gate after the reviewer signs off. Owns #wf-057-writeback-reliability.',
      retries: 1,
    })
    .agent('impl-daemon', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role:
        'Phase 1: extend the mount daemon writeback push (cmd/relayfile-cli/main.go) to log every non-2xx response, increment a new failedWritebacks counter in state.json, and dead-letter to ~/relayfile-mount/.relay/dead-letter/<opId>.json on persistent failure. Add a Go test that uses httptest to simulate 4xx and asserts the dead-letter file appears.',
      retries: 2,
    })
    .agent('impl-cli', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      role:
        'Phase 4: add `writeback status` and `writeback retry` subcommands to the CLI dispatcher in cmd/relayfile-cli/main.go. `status` reads state.json + the dead-letter dir and prints pending / failed / dead-lettered counts and the most recent error per provider. `retry` re-enqueues a single dead-lettered op by id.',
      retries: 2,
    })
    .agent('tester', {
      cli: 'codex',
      model: CodexModels.GPT_5_4,
      preset: 'worker',
      role:
        'Runs go test ./..., go build ./..., and the deterministic CLI E2E. Reads failures, fixes the right Go file, re-runs until green.',
      retries: 2,
    })
    .agent('reviewer-claude', {
      cli: 'claude',
      model: ClaudeModels.OPUS,
      preset: 'reviewer',
      role:
        'Reviews diffs after each phase. Specifically checks: (a) failed writebacks actually dead-letter rather than getting silently swallowed, (b) the new CLI subcommands handle a missing dead-letter dir gracefully, (c) state.json schema additions are backwards-compatible with older mount versions.',
      retries: 1,
    })

    // ── Preflight (relayfile-flavoured: no npm install, just go) ─────────
    .step('setup-branch', {
      type: 'deterministic',
      command: [
        'set -e',
        'git config user.email "agent@agent-relay.com"',
        'git config user.name "Writeback Reliability Bot"',
        `git checkout -B ${BRANCH}`,
        'git log -1 --oneline',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('preflight', {
      type: 'deterministic',
      dependsOn: ['setup-branch'],
      command: [
        'set -e',
        'BRANCH=$(git rev-parse --abbrev-ref HEAD)',
        `if [ "$BRANCH" != "${BRANCH}" ]; then echo "ERROR: wrong branch (expected ${BRANCH})"; exit 1; fi`,
        // Files this workflow rewrites; everything else fails preflight.
        'ALLOWED_DIRTY="cmd/relayfile-cli/main\\.go|cmd/relayfile-cli/main_test\\.go|cmd/relayfile-cli/writeback_daemon_test\\.go|cmd/relayfile-cli/writeback_status_test\\.go|go\\.mod|go\\.sum"',
        'DIRTY=$(git diff --name-only | grep -vE "^(${ALLOWED_DIRTY})$" || true)',
        'if [ -n "$DIRTY" ]; then echo "ERROR: unexpected tracked drift:"; echo "$DIRTY"; exit 1; fi',
        'if ! git diff --cached --quiet; then echo "ERROR: staging area is dirty"; git diff --cached --stat; exit 1; fi',
        'gh auth status >/dev/null 2>&1 || (echo "ERROR: gh CLI not authenticated"; exit 1)',
        'go version',
        'echo PREFLIGHT_OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('go-mod-tidy', {
      // Fast cache warm-up. No install equivalent in Go modules; this just
      // pulls deps so the build/test steps don't include cold download time.
      type: 'deterministic',
      dependsOn: ['preflight'],
      command: 'go mod download && go vet ./... 2>&1 | tail -10',
      captureOutput: true,
      failOnError: false, // vet may surface pre-existing nits we don't own
    })

    // ── Pre-inject context once ──────────────────────────────────────────
    .step('read-spec', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      // Spec lives in the cloud repo; fetch the raw file via gh.
      command:
        'gh api -H "Accept: application/vnd.github.v3.raw" /repos/AgentWorkforce/cloud/contents/docs/architecture/writeback-reliability.md',
      captureOutput: true,
      failOnError: true,
    })
    .step('read-writeback-region', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      // main.go is ~3000 lines. Don't dump it all — extract the writeback
      // push function + surrounding context. The agent can re-read in full
      // with cat if needed.
      command: [
        'set -e',
        `grep -n "writeback\\|WRITEBACK_QUEUE\\|deadLetterRecord\\|pendingWriteback\\|failedWritebacks" ${MAIN_GO} | head -40`,
        'echo "---"',
        // Print 80 lines around the first writeback-related func definition
        `LINE=$(grep -n "func .*[Ww]riteback" ${MAIN_GO} | head -1 | cut -d: -f1)`,
        'if [ -n "$LINE" ]; then',
        `  START=$((LINE > 20 ? LINE - 20 : 1))`,
        `  END=$((LINE + 80))`,
        `  sed -n "$START,$END\\p" ${MAIN_GO}`,
        'fi',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('read-cmd-dispatch', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      // The subcommand router lives near the top of main.go — print the
      // case "ops" / case "mount" / case "status" lines so impl-cli knows
      // where to plug in `writeback status` / `writeback retry`.
      command: [
        'set -e',
        `grep -n 'case "ops"\\|case "mount"\\|case "status"\\|case "stop"\\|case "logs"\\|case "pull"' ${MAIN_GO} | head -20`,
        'echo "---"',
        // Print the help output too — new subcommands need help entries.
        `grep -n -B 1 -A 30 "Subcommands:" ${MAIN_GO} | head -50`,
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    // ── Lead posts the plan; impl-daemon and impl-cli start in parallel ──
    .step('lead-coordinate', {
      agent: 'lead-codex',
      dependsOn: ['read-spec', 'read-writeback-region', 'read-cmd-dispatch'],
      task: [
        'You are the lead on #wf-057-writeback-reliability.',
        '',
        'Spec excerpt:',
        '{{steps.read-spec.output}}',
        '',
        'Acceptance contract (LOCK these — do not relax):',
        '  P1.1 Every non-2xx response from the cloud writeback PUT is logged at WARN with the response body (truncated to 1KB).',
        '  P1.2 state.json gains a failedWritebacks counter (additive, defaults to 0 for older state files — backwards compatible).',
        '  P1.3 On persistent failure (after retries exhausted), the daemon writes ~/relayfile-mount/.relay/dead-letter/<opId>.json with { opId, path, attempts, lastStatus, lastBody, ts }.',
        '  P1.4 New Go test in cmd/relayfile-cli/writeback_daemon_test.go: spin up a httptest server that 400s, drive the daemon, assert a dead-letter file appears AND failedWritebacks > 0.',
        '  P4.1 New subcommand `relayfile writeback status [WORKSPACE]` prints pending / failed / dead-lettered counts and the most recent error per provider.',
        '  P4.2 New subcommand `relayfile writeback retry --opId OP` re-enqueues a dead-lettered op by id.',
        '  P4.3 Both subcommands handle a missing dead-letter dir / state.json gracefully (don’t panic).',
        '  P4.4 New Go test in cmd/relayfile-cli/writeback_status_test.go: builds the CLI, runs it against a fixture mount with known dead-letter files, asserts the human-readable output contains the expected counts.',
        '',
        'Workers: impl-daemon (P1), impl-cli (P4), tester (runs/fixes go test + go build), reviewer-claude (reviews diffs at each gate).',
        '',
        'Post the work-split as the first message. Do not call the merge gate until the reviewer-claude has approved both diffs.',
      ].join('\n'),
    })

    // ──────────────────────────────────────────────────────────────────────
    // PHASE 1 — mount daemon: log + dead-letter on writeback failure
    // ──────────────────────────────────────────────────────────────────────
    .step('impl-daemon-fix', {
      agent: 'impl-daemon',
      dependsOn: ['read-writeback-region'],
      task: [
        `Edit ${MAIN_GO}.`,
        '',
        'Relevant region:',
        '{{steps.read-writeback-region.output}}',
        '',
        'Make these edits:',
        '',
        '1. Find the function that issues the PUT to the cloud writeback API. It will look something like daemonFlushWriteback / pushWriteback / writebackPump (search the file). When the response is non-2xx:',
        '   - read at most 1KB of the response body',
        '   - log via the existing logger at WARN with: opId, path, status, bodyTruncated',
        '   - increment a new field on the daemon state struct: FailedWritebacks (uint64)',
        '   - persist that field through state.json (additive — older state files load with 0)',
        '',
        '2. After retries are exhausted (existing retry loop already exists), write a dead-letter file at <localRoot>/.relay/dead-letter/<opId>.json with this shape:',
        '   { "opId": string, "path": string, "attempts": int, "lastStatus": int, "lastBody": string, "ts": RFC3339 }',
        '',
        '3. Surface FailedWritebacks in the syncStateSnapshot (the JSON returned by `relayfile status`) so the new CLI command can read it.',
        '',
        'Only edit cmd/relayfile-cli/main.go. Do NOT touch the cmd routing yet (that is impl-cli\'s phase).',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('verify-daemon-fix', {
      type: 'deterministic',
      dependsOn: ['impl-daemon-fix'],
      command: [
        'set -e',
        `if git diff --quiet ${MAIN_GO}; then echo "NOT MODIFIED"; exit 1; fi`,
        // Look for the new symbols / log lines / fields. Use literal
        // substrings (BSD grep + alternation pitfalls noted in feedback memory).
        `grep -q "FailedWritebacks" ${MAIN_GO} || (echo "MISSING FailedWritebacks"; exit 1)`,
        `grep -q "dead-letter" ${MAIN_GO} || (echo "MISSING dead-letter path handling"; exit 1)`,
        `grep -q "lastBody" ${MAIN_GO} || (echo "MISSING lastBody field"; exit 1)`,
        'echo OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .step('impl-daemon-test', {
      agent: 'impl-daemon',
      dependsOn: ['verify-daemon-fix'],
      task: [
        `Create ${DAEMON_TEST_GO}.`,
        '',
        'Use net/http/httptest to spin up a server that always returns 400 with a known body. Drive a single writeback through the daemon. Assert:',
        '  - the dead-letter file path/.relay/dead-letter/<opId>.json exists after retries',
        '  - the file contents parse and contain { opId, lastStatus: 400, lastBody: <body> }',
        '  - the daemon\'s state snapshot reports failedWritebacks > 0',
        '',
        'Use a t.TempDir() for the local mount root. Build the test with the existing test helpers in main_test.go for the daemon harness — search for fakeBroker or testDaemon and reuse.',
        '',
        'IMPORTANT: Write the file to disk. Do NOT output to stdout.',
      ].join('\n'),
      verification: { type: 'file_exists', value: DAEMON_TEST_GO },
    })

    .step('run-daemon-test', {
      type: 'deterministic',
      dependsOn: ['impl-daemon-test'],
      command:
        'go test -count=1 -run TestWritebackDeadLetter ./cmd/relayfile-cli/ 2>&1 | tail -40',
      captureOutput: true,
      failOnError: false, // first run may need fixes
    })
    .step('fix-daemon-test', {
      agent: 'tester',
      dependsOn: ['run-daemon-test'],
      task: [
        'Test output:',
        '{{steps.run-daemon-test.output}}',
        '',
        'If all tests passed, do nothing.',
        'If failures: read the failing test, read main.go, fix whichever is wrong (most often the impl is missing a path or the test misuses a helper), re-run.',
        '',
        'Re-run: go test -count=1 -run TestWritebackDeadLetter ./cmd/relayfile-cli/',
        '',
        'Iterate until green.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('run-daemon-test-final', {
      type: 'deterministic',
      dependsOn: ['fix-daemon-test'],
      command:
        'go test -count=1 -run TestWritebackDeadLetter ./cmd/relayfile-cli/ 2>&1',
      captureOutput: true,
      failOnError: true, // hard gate
    })

    .step('review-phase-1', {
      agent: 'reviewer-claude',
      dependsOn: ['run-daemon-test-final'],
      task: [
        'Review the Phase 1 diff. Run: git diff main -- cmd/relayfile-cli/main.go cmd/relayfile-cli/writeback_daemon_test.go',
        '',
        'Specifically check:',
        '  1. Failed writebacks dead-letter rather than being silently swallowed.',
        '  2. The state.json schema addition is backwards-compatible (older mounts deserialize cleanly with FailedWritebacks=0).',
        '  3. The dead-letter file write is atomic (temp file + rename) so a crash mid-write does not leave a partial JSON.',
        '  4. The retry loop bounds are sane (no infinite retry, no zero-backoff).',
        '',
        'Post your verdict in #wf-057-writeback-reliability. If issues, list specific fixes; impl-daemon will iterate. If approved, say "PHASE 1 APPROVED" so the lead knows to proceed.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    // ──────────────────────────────────────────────────────────────────────
    // PHASE 4 — `writeback status` + `writeback retry` CLI subcommands
    // ──────────────────────────────────────────────────────────────────────
    .step('impl-cli-add', {
      agent: 'impl-cli',
      dependsOn: ['review-phase-1', 'read-cmd-dispatch'],
      task: [
        `Edit ${MAIN_GO}.`,
        '',
        'Routing context:',
        '{{steps.read-cmd-dispatch.output}}',
        '',
        'Add two new subcommands plumbed through the existing case dispatch:',
        '',
        '1. `writeback status [WORKSPACE]`',
        '   - Resolves workspace via the same lookup `relayfile status` uses.',
        '   - Reads <localDir>/.relay/state.json for pending + failedWritebacks.',
        '   - Lists files in <localDir>/.relay/dead-letter/.',
        '   - Prints a human-readable block plus a `--json` mode that emits a structured shape: { workspaceId, pending, failed, deadLettered: [{ opId, path, lastStatus, ts }], lastErrorByProvider }.',
        '   - Exits 0 if all counts are 0; non-zero exit if any failed/dead-lettered (so CI can gate on it).',
        '',
        '2. `writeback retry --opId OP [WORKSPACE]`',
        '   - Reads <localDir>/.relay/dead-letter/<opId>.json.',
        '   - Re-enqueues the writeback (use the existing writeback-queue insertion code path the daemon uses).',
        '   - On success: removes the dead-letter file.',
        '   - Errors loudly (and exits non-zero) if opId is unknown or the queue insert fails.',
        '',
        'Update the Subcommands: help block to include both new commands.',
        '',
        'Both must handle missing dead-letter dir / state.json gracefully — print "no failures" and exit 0, do NOT panic.',
        '',
        'Only edit cmd/relayfile-cli/main.go.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('verify-cli-add', {
      type: 'deterministic',
      dependsOn: ['impl-cli-add'],
      command: [
        'set -e',
        `if git diff --quiet ${MAIN_GO}; then echo "NOT MODIFIED"; exit 1; fi`,
        `grep -q "writeback status" ${MAIN_GO} || (echo "MISSING writeback status routing"; exit 1)`,
        `grep -q "writeback retry" ${MAIN_GO} || (echo "MISSING writeback retry routing"; exit 1)`,
        'echo OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .step('impl-cli-test', {
      agent: 'impl-cli',
      dependsOn: ['verify-cli-add'],
      task: [
        'Create cmd/relayfile-cli/writeback_status_test.go.',
        '',
        'Use t.TempDir() to build a fixture mount layout:',
        '  <tmp>/.relay/state.json with { pendingWriteback: 0, failedWritebacks: 2 }',
        '  <tmp>/.relay/dead-letter/op_a.json with { opId: "op_a", path: "...", lastStatus: 400, ts: "..." }',
        '  <tmp>/.relay/dead-letter/op_b.json similarly',
        '',
        'Run the CLI as a subprocess (exec.Command on the just-built binary, or invoke the runWritebackStatus function directly if the dispatch helper is exported within the package).',
        '',
        'Assert:',
        '  - human output contains "failed: 2" (or whatever exact format you chose)',
        '  - --json output is valid JSON with deadLettered.length === 2',
        '  - exit code is non-zero (because there ARE failures)',
        '',
        'Then test the no-failures case: empty fixture, exit code 0, output mentions "no failures".',
        '',
        'IMPORTANT: Write the file to disk. Do NOT output to stdout.',
      ].join('\n'),
      verification: {
        type: 'file_exists',
        value: 'cmd/relayfile-cli/writeback_status_test.go',
      },
    })

    .step('run-cli-test', {
      type: 'deterministic',
      dependsOn: ['impl-cli-test'],
      command:
        'go test -count=1 -run TestWritebackStatus ./cmd/relayfile-cli/ 2>&1 | tail -40',
      captureOutput: true,
      failOnError: false,
    })
    .step('fix-cli-test', {
      agent: 'tester',
      dependsOn: ['run-cli-test'],
      task: [
        'Test output:',
        '{{steps.run-cli-test.output}}',
        '',
        'If all tests passed, do nothing.',
        'If failures, read the test, fix whichever side is wrong, re-run.',
        '',
        'Re-run: go test -count=1 -run TestWritebackStatus ./cmd/relayfile-cli/',
        '',
        'Iterate until green.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('run-cli-test-final', {
      type: 'deterministic',
      dependsOn: ['fix-cli-test'],
      command:
        'go test -count=1 -run TestWritebackStatus ./cmd/relayfile-cli/ 2>&1',
      captureOutput: true,
      failOnError: true,
    })

    .step('review-phase-4', {
      agent: 'reviewer-claude',
      dependsOn: ['run-cli-test-final'],
      task: [
        'Review the Phase 4 diff. Run: git diff main -- cmd/relayfile-cli/main.go cmd/relayfile-cli/writeback_status_test.go',
        '',
        'Specifically check:',
        '  1. Both subcommands handle a missing dead-letter dir without panicking.',
        '  2. The --json output shape is stable and machine-parseable (no time-dependent fields beyond explicit ts strings).',
        '  3. `writeback retry` removes the dead-letter file only AFTER the queue insert succeeds (don’t lose the record on a transient failure).',
        '  4. Exit codes are non-zero only when there are real failures, so CI gating is meaningful.',
        '',
        'Post verdict in #wf-057-writeback-reliability. Approve with "PHASE 4 APPROVED" once satisfied.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })

    // ── Cross-cutting build + full regression ────────────────────────────
    .step('go-build', {
      type: 'deterministic',
      dependsOn: ['review-phase-4'],
      command: 'go build ./... 2>&1 | tail -20; echo "EXIT: $?"',
      captureOutput: true,
      failOnError: false,
    })
    .step('fix-build', {
      agent: 'tester',
      dependsOn: ['go-build'],
      task: [
        'Output:',
        '{{steps.go-build.output}}',
        '',
        'If exit was 0, do nothing.',
        'Else fix the type / build errors and re-run: go build ./...',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('go-build-final', {
      type: 'deterministic',
      dependsOn: ['fix-build'],
      command: 'go build ./... 2>&1',
      captureOutput: true,
      failOnError: true,
    })

    .step('go-test-all', {
      type: 'deterministic',
      dependsOn: ['go-build-final'],
      command: 'go test -count=1 ./... 2>&1 | tail -60',
      captureOutput: true,
      failOnError: false,
    })
    .step('fix-regressions', {
      agent: 'tester',
      dependsOn: ['go-test-all'],
      task: [
        'Existing test suite output:',
        '{{steps.go-test-all.output}}',
        '',
        'If all green, do nothing.',
        'If regressions: read the failing test, find what we broke, fix it. Most likely cause is the state.json schema addition (FailedWritebacks) hitting a snapshot or strict-decode test, or the new CLI dispatch case shadowing an existing route.',
        '',
        'Re-run: go test -count=1 ./...',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
    })
    .step('go-test-all-final', {
      type: 'deterministic',
      dependsOn: ['fix-regressions'],
      command: 'go test -count=1 ./... 2>&1 | tail -20',
      captureOutput: true,
      failOnError: true,
    })

    // ── Commit + push + PR ──────────────────────────────────────────────
    .step('commit', {
      type: 'deterministic',
      dependsOn: ['go-test-all-final'],
      command: [
        'set -e',
        // Explicit add list — never `git add -A`.
        `git add ${MAIN_GO} ${MAIN_TEST_GO} ${DAEMON_TEST_GO} cmd/relayfile-cli/writeback_status_test.go go.mod go.sum 2>/dev/null || true`,
        'MSG=$(mktemp)',
        'printf "%s\\n" "fix(writeback): mount-daemon dead-letter + writeback status/retry CLI" "" "Implements relayfile-side phases (1, 4) of the writeback-reliability spec (AgentWorkforce/cloud#448)." "" "Phase 1 — mount daemon (cmd/relayfile-cli/main.go):" "- Every non-2xx response from the cloud writeback PUT is now logged at WARN with status + truncated body" "- New FailedWritebacks counter on the daemon state, persisted in state.json (additive, backwards compatible)" "- After retries exhausted, the daemon writes ~/relayfile-mount/.relay/dead-letter/<opId>.json with op metadata" "- New Go test (writeback_daemon_test.go) drives an httptest 4xx through the daemon and asserts the dead-letter file appears" "" "Phase 4 — CLI surfaces (cmd/relayfile-cli/main.go):" "- New \\\`relayfile writeback status [WORKSPACE]\\\` prints pending / failed / dead-lettered counts and most-recent error per provider; --json mode for machine consumption; non-zero exit iff there are failures so CI can gate" "- New \\\`relayfile writeback retry --opId OP [WORKSPACE]\\\` re-enqueues a dead-lettered op and removes the dead-letter file on success" "- Both gracefully handle missing dead-letter dir / state.json" "- New Go test (writeback_status_test.go) builds the binary and asserts output against a fixture mount" "" "Phases plus / 2 / 3 land in the cloud repo separately (AgentWorkforce/cloud workflow PR)." "" "Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>" > "$MSG"',
        'git commit -F "$MSG"',
        'rm -f "$MSG"',
        'git log -1 --oneline',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('push', {
      type: 'deterministic',
      dependsOn: ['commit'],
      command: `git push -u origin ${BRANCH}`,
      captureOutput: true,
      failOnError: true,
    })
    .step('open-pr', {
      type: 'deterministic',
      dependsOn: ['push'],
      command: [
        'set -e',
        'BODY=$(mktemp)',
        'printf "%s\\n" "## Summary" "" "Relayfile-side phases (1, 4) of the writeback-reliability spec — AgentWorkforce/cloud#448. The cloud-side phases (plus / 2 / 3) land in a sibling PR on the cloud repo." "" "**Phase 1 — mount daemon stops silently dropping writes.**" "" "Today the daemon flips \\\`pendingWriteback: 1 → 0\\\` even when the cloud never received the PUT. After this change:" "- every non-2xx is logged at WARN with status + truncated body" "- \\\`failedWritebacks\\\` counter is persisted in \\\`state.json\\\` (additive, backwards compatible)" "- after retries exhausted, daemon writes \\\`~/relayfile-mount/.relay/dead-letter/<opId>.json\\\` with op metadata" "- \\\`writeback_daemon_test.go\\\` drives an httptest 4xx through the daemon and asserts the dead-letter file appears" "" "**Phase 4 — CLI surfaces for writeback health.**" "" "- \\\`relayfile writeback status [WORKSPACE]\\\`: pending / failed / dead-lettered counts + most-recent error per provider; \\\`--json\\\` mode; non-zero exit iff failures" "- \\\`relayfile writeback retry --opId OP [WORKSPACE]\\\`: re-enqueues a dead-lettered op, removes the dead-letter file on success" "- both handle missing state.json / dead-letter dir gracefully" "- \\\`writeback_status_test.go\\\` builds the binary and asserts against a fixture mount" "" "## Test plan" "" "- [x] \\\`go build ./...\\\` clean" "- [x] \\\`go test ./...\\\` green (existing suite + 2 new tests)" "- [x] Phase 1 dead-letter test passes (httptest-driven)" "- [x] Phase 4 CLI test passes (fixture-mount-driven)" "- [ ] After merge: pair with cloud-side PR to validate end-to-end with a real Notion writeback" "" "Closes part of AgentWorkforce/cloud#448." "" "🤖 Generated with [Claude Code](https://claude.com/claude-code)" > "$BODY"',
        `gh pr create --base main --head ${BRANCH} --title "fix(writeback): mount-daemon dead-letter + writeback status/retry CLI" --body-file "$BODY" | tee /tmp/wf057-pr-url.txt`,
        'rm -f "$BODY"',
        'echo "PR URL: $(cat /tmp/wf057-pr-url.txt)"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .onError('retry', { maxRetries: 2, retryDelayMs: 10_000 })
    .run({ cwd: process.cwd() });

  console.log('Workflow status:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
