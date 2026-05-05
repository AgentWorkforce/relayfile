// workflows/057-writeback-reliability-mount-and-cli.ts
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
// Pattern: relay-80-100. The workflow does not sign off until:
//   - new Go test exercises a 4xx response from a httptest server and asserts
//     a dead-letter file gets written
//   - `go build ./...` clean
//   - `go test ./...` green (existing suite + the new tests)
//   - `relayfile writeback status` against a fixture mount surfaces the right
//     counts (deterministic shell-driven E2E)
//
// Team split (per writing-agent-relay-workflows skill rule §5):
//   impl / tester agents → codex
//   review gates         → deterministic checks in this artifact
//
// Branch:   fix/writeback-reliability
// Repo:     relayfile (this repo, no worktree split)
// Spec:     AgentWorkforce/cloud docs/architecture/writeback-reliability.md (PR #448)

import { workflow } from '@agent-relay/sdk/workflows';

const BRANCH = 'fix/writeback-reliability';
const WORKFLOW_ARTIFACT =
  'workflows/057-writeback-reliability-mount-and-cli.ts';
const MAIN_GO = 'cmd/relayfile-cli/main.go';
const DAEMON_TEST_GO = 'cmd/relayfile-cli/writeback_daemon_test.go';
const STATUS_TEST_GO = 'cmd/relayfile-cli/writeback_status_test.go';
const AGENT_STEP_TIMEOUT_MS = 300_000;
const AGENT_FILE_STEP_TIMEOUT_MS = 180_000;
const PHASE4_IMPL_CLI_TIMEOUT_MS = 150_000;
const RICKY_LAUNCHER_TIMEOUT_MS = 600_000;

async function main() {
  const result = await workflow('057-writeback-reliability-mount-and-cli')
    .description(
      'Fix mount daemon silent writeback drop (phase 1) and add writeback status / retry CLI surfaces (phase 4) per AgentWorkforce/cloud#448.',
    )
    .pattern('dag')
    .channel('wf-057-writeback-reliability')
    .maxConcurrency(5)
    .timeout(3_600_000)

    // ── Agents (codex implements/tests; no lead) ─────────────────────────
    // Codex-as-lead stomped on codex impl agents on the same channel
    // (run 4fa4aff6, 2026-05-05) — both interpreted "owner" of the file
    // literally. With only two phases × two implementers there's no
    // coordinator to add: each impl reads the spec directly, the
    // deterministic review gates + collect-evidence are the merge gates.
    .agent('impl-daemon', {
      cli: 'codex',
      preset: 'worker',
      role:
        'Phase 1: extend the mount daemon writeback push (cmd/relayfile-cli/main.go) to log every non-2xx response, increment a new failedWritebacks counter in state.json, and dead-letter to ~/relayfile-mount/.relay/dead-letter/<opId>.json on persistent failure. Add a Go test that uses httptest to simulate 4xx and asserts the dead-letter file appears.',
      retries: 2,
    })
    .agent('impl-cli', {
      cli: 'codex',
      preset: 'worker',
      role:
        'Phase 4: add `writeback status` and `writeback retry` subcommands to the CLI dispatcher in cmd/relayfile-cli/main.go. `status` reads state.json + the dead-letter dir and prints pending / failed / dead-lettered counts and the most recent error per provider. `retry` re-enqueues a single dead-lettered op by id.',
      // Keep retries at zero so Ricky's 600s launcher budget surfaces a
      // concrete failed step instead of timing out the whole launch.
      retries: 0,
    })
    .agent('tester', {
      cli: 'codex',
      preset: 'worker',
      role:
        'Runs go test ./..., go build ./..., and the deterministic CLI E2E. Reads failures, fixes the right Go file, re-runs until green.',
      retries: 2,
    })
    // ── Runtime launch gate (resume anchor for Ricky local runner) ──────
    .step('runtime-launch', {
      type: 'deterministic',
      command: [
        'set -e',
        'echo "Runtime launch gate: validating local workflow runtime dependencies"',
        'if ! node -e "require.resolve(\'@agent-relay/sdk/workflows\')" >/dev/null 2>&1; then',
        '  echo "WARN: @agent-relay/sdk/workflows is not resolvable; installing local Node dependencies."',
        '  if [ -f package-lock.json ]; then',
        '    npm ci --no-audit --no-fund',
        '  else',
        '    npm install --no-audit --no-fund',
        '  fi',
        'fi',
        'node -e "require.resolve(\'@agent-relay/sdk/workflows\'); console.log(\'WORKFLOW_RUNTIME_READY\')"',
        'node --version',
        'echo "runtime_exit_code=0"',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    // ── Preflight (relayfile-flavoured: no npm install, just go) ─────────
    .step('setup-branch', {
      type: 'deterministic',
      dependsOn: ['runtime-launch'],
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
        'CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)',
        `if [ "$CURRENT_BRANCH" != "${BRANCH}" ]; then echo "WARN: switching branch to ${BRANCH} for resume safety"; git checkout -B ${BRANCH}; fi`,
        // Files this workflow rewrites plus workflow-local trajectory state.
        // The `trail` instrumentation updates `.trajectories/*` during runs
        // and this artifact can be edited while repairing/resuming.
        `ALLOWED_DIRTY="cmd/relayfile-cli/main\\.go|cmd/relayfile-cli/main_test\\.go|cmd/relayfile-cli/writeback_daemon_test\\.go|cmd/relayfile-cli/writeback_status_test\\.go|go\\.mod|go\\.sum|${WORKFLOW_ARTIFACT.replace(/\//g, '\\/').replace(/\./g, '\\.')}|\\.trajectories/index\\.json|\\.trajectories/active/traj_.*\\.json|\\.trajectories/completed/.*|\\.agent-relay/.*|\\.agent-relay\\.stale\\..*"`,
        'UNEXPECTED_DIRTY=$({ git diff --name-only; git ls-files --others --exclude-standard; } | sort -u | grep -vE "^(${ALLOWED_DIRTY})$" || true)',
        'if [ -n "$UNEXPECTED_DIRTY" ]; then echo "WARN: unrelated workspace drift detected (continuing with bounded workflow scope):"; echo "$UNEXPECTED_DIRTY"; fi',
        'if ! git diff --cached --quiet; then echo "ERROR: staging area is dirty"; git diff --cached --stat; exit 1; fi',
        'gh auth status >/dev/null 2>&1 || (echo "ERROR: gh CLI not authenticated"; exit 1)',
        'go version',
        'echo PREFLIGHT_OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('agent-runtime-guard', {
      type: 'deterministic',
      dependsOn: ['preflight'],
      command: [
        'set -e',
        'echo "Agent runtime guard: validating broker state and agent CLIs"',
        'if [ -d .agent-relay ]; then',
        '  STALE_DIR=".agent-relay.stale.$(date +%Y%m%d%H%M%S)"',
        '  mv .agent-relay "$STALE_DIR"',
        '  echo "archived_stale_agent_relay_dir=$STALE_DIR"',
        'fi',
        'for cli in codex; do',
        '  if ! command -v "$cli" >/dev/null 2>&1; then',
        '    echo "required_cli_missing:$cli"',
        '    exit 1',
        '  fi',
        'done',
        'node <<\'NODE\'',
        'const { spawnSync } = require("node:child_process");',
        'const checks = [["codex", ["--version"]]];',
        'for (const [cli, args] of checks) {',
        '  const result = spawnSync(cli, args, { encoding: "utf8", timeout: 30000 });',
        '  if (result.error) {',
        '    console.error(`${cli}_check_failed:${result.error.message}`);',
        '    process.exit(1);',
        '  }',
        '  if (result.status !== 0) {',
        '    console.error(`${cli}_nonzero_exit:${result.status}`);',
        '    process.exit(result.status || 1);',
        '  }',
        '  console.log(`${cli}_ready`);',
        '}',
        'NODE',
        'echo AGENT_RUNTIME_READY',
      ].join('\n'),
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
      // Prefer local spec copies for deterministic resumes; fall back to gh.
      command: [
        'set -e',
        'read_spec_file() {',
        '  spec_file="$1"',
        '  if [ -f "$spec_file" ]; then',
        '    sed -n "1,260p" "$spec_file"',
        '    return 0',
        '  fi',
        '  return 1',
        '}',
        'if read_spec_file "docs/architecture/writeback-reliability.md"; then exit 0; fi',
        'if read_spec_file "../cloud/docs/architecture/writeback-reliability.md"; then exit 0; fi',
        // Some environments expose only one of these repo slugs; try both.
        'if gh api -H "Accept: application/vnd.github.v3.raw" /repos/AgentWorkforce/cloud/contents/docs/architecture/writeback-reliability.md 2>/dev/null; then exit 0; fi',
        'if gh api -H "Accept: application/vnd.github.v3.raw" /repos/agentworkforce/cloud/contents/docs/architecture/writeback-reliability.md 2>/dev/null; then exit 0; fi',
        'echo "WARN: unable to fetch docs/architecture/writeback-reliability.md from local paths or GH; continuing with embedded acceptance contract."',
        'cat <<\'SPEC_FALLBACK\'',
        'Writeback reliability spec fallback:',
        '- Phase 1: non-2xx writeback responses must be WARN-logged, counted in failedWritebacks, and persistent failures must dead-letter.',
        '- Phase 4: add relayfile writeback status/retry CLI paths for operator-visible recovery.',
        '- Hard gate: new daemon + status tests, go build ./..., and go test ./... must pass before signoff.',
        'SPEC_FALLBACK',
        'exit 0',
      ].join('\n'),
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
        '  sed -n "${START},${END}p" ' + MAIN_GO,
        'fi',
      ].join('\n'),
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

    // No lead step — codex-as-lead overrode codex impl agents on the same
    // channel and stole file ownership (run 4fa4aff6, 2026-05-05). The
    // implementer task prompts already encode the work-split, the spec is
    // injected directly into each impl agent, and deterministic review
    // gates provide inter-phase quality checks. No coordinator needed for
    // two phases × two implementers.

    // ──────────────────────────────────────────────────────────────────────
    // PHASE 1 — mount daemon: log + dead-letter on writeback failure
    // ──────────────────────────────────────────────────────────────────────
    .step('impl-daemon-fix', {
      agent: 'impl-daemon',
      dependsOn: ['agent-runtime-guard', 'read-writeback-region'],
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
      timeout: AGENT_STEP_TIMEOUT_MS,
    })
    .step('verify-daemon-fix', {
      type: 'deterministic',
      dependsOn: ['impl-daemon-fix'],
      command: [
        'set -e',
        `if git diff --quiet ${MAIN_GO}; then echo "main_go_not_modified"; exit 1; fi`,
        // Look for the new symbols / log lines / fields. Use literal
        // substrings (BSD grep + alternation pitfalls noted in feedback memory).
        `grep -q "FailedWritebacks" ${MAIN_GO} || (echo "missing_failed_writebacks_symbol"; exit 1)`,
        `grep -q "dead-letter" ${MAIN_GO} || (echo "missing_dead_letter_handling"; exit 1)`,
        `grep -q "lastBody" ${MAIN_GO} || (echo "missing_last_body_field"; exit 1)`,
        'echo OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    .step('agent-precheck-daemon-test', {
      type: 'deterministic',
      dependsOn: ['agent-runtime-guard', 'verify-daemon-fix'],
      // Ricky's local launcher enforces a 600s outer timeout. This precheck
      // plus shorter file-creation agent timeout keeps hangs deterministic and
      // classifiable before the outer launch timeout fires.
      command: [
        'set -e',
        'echo "Agent precheck: impl-daemon-test"',
        'if [ -f .agent-relay/team/workers.json ]; then',
        '  echo "WARN: stale workers.json detected before impl-daemon-test"',
        '  sed -n "1,160p" .agent-relay/team/workers.json',
        'fi',
        'node -e "const { spawnSync } = require(\'node:child_process\'); const r = spawnSync(\'codex\', [\'--version\'], { encoding: \'utf8\', timeout: 20000 }); if (r.error || r.status !== 0) { console.error(\'codex_precheck_failed\'); process.exit(1); } console.log(\'codex_precheck_ok\');"',
        'echo AGENT_PRECHECK_DAEMON_TEST_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('impl-daemon-test', {
      agent: 'impl-daemon',
      dependsOn: ['agent-precheck-daemon-test'],
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
        '',
        'Execution contract: keep the update scoped to cmd/relayfile-cli/writeback_daemon_test.go and exit promptly once written.',
      ].join('\n'),
      verification: { type: 'file_exists', value: DAEMON_TEST_GO },
      timeout: AGENT_FILE_STEP_TIMEOUT_MS,
    })

    .step('run-daemon-test', {
      type: 'deterministic',
      dependsOn: ['impl-daemon-test'],
      command:
        'go test -count=1 -run TestWritebackDaemonDeadLettersHTTP400 ./cmd/relayfile-cli/ 2>&1 | tail -40',
      captureOutput: true,
      failOnError: false, // first run may need fixes
    })
    .step('agent-progress-guard-daemon-test', {
      type: 'deterministic',
      dependsOn: ['run-daemon-test'],
      command: [
        'set -e',
        `if [ ! -f "${DAEMON_TEST_GO}" ]; then echo "WARN: daemon test file missing after impl-daemon-test; continuing to tester fix loop"; fi`,
        'echo AGENT_PROGRESS_GUARD_DAEMON_TEST_OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: false,
    })
    .step('fix-daemon-test', {
      agent: 'tester',
      dependsOn: ['agent-progress-guard-daemon-test'],
      task: [
        'Test output:',
        '{{steps.run-daemon-test.output}}',
        '',
        'If all tests passed, do nothing.',
        'If failures: read the failing test, read main.go, fix whichever is wrong (most often the impl is missing a path or the test misuses a helper), re-run.',
        '',
        'Re-run: go test -count=1 -run TestWritebackDaemonDeadLettersHTTP400 ./cmd/relayfile-cli/',
        '',
        'Iterate until green.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      timeout: AGENT_FILE_STEP_TIMEOUT_MS,
    })
    .step('run-daemon-test-final', {
      type: 'deterministic',
      dependsOn: ['fix-daemon-test'],
      command:
        'go test -count=1 -run TestWritebackDaemonDeadLettersHTTP400 ./cmd/relayfile-cli/ 2>&1',
      captureOutput: true,
      failOnError: true, // hard gate
    })

    .step('review-phase-1', {
      type: 'deterministic',
      dependsOn: ['run-daemon-test-final'],
      command: [
        'set -e',
        'echo "Phase 1 deterministic review gate"',
        `git diff -- ${MAIN_GO} ${DAEMON_TEST_GO} | sed -n "1,220p"`,
        `grep -q "FailedWritebacks" ${MAIN_GO} || (echo "phase1_review_missing_failed_writebacks"; exit 1)`,
        `grep -q "dead-letter" ${MAIN_GO} || (echo "phase1_review_missing_dead_letter_path"; exit 1)`,
        `grep -q "os.Rename" ${MAIN_GO} || (echo "phase1_review_missing_atomic_rename"; exit 1)`,
        `grep -q "writebackMaxHTTPAttempts" ${MAIN_GO} || (echo "phase1_review_missing_retry_bound"; exit 1)`,
        'go test -count=1 -run TestWritebackDaemonDeadLettersHTTP400 ./cmd/relayfile-cli/ >/tmp/wf057-phase1-review.log 2>&1',
        'tail -20 /tmp/wf057-phase1-review.log',
        'echo "PHASE 1 APPROVED"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    // ──────────────────────────────────────────────────────────────────────
    // PHASE 4 — `writeback status` + `writeback retry` CLI subcommands
    // ──────────────────────────────────────────────────────────────────────
    .step('agent-precheck-cli-add', {
      type: 'deterministic',
      dependsOn: ['agent-runtime-guard', 'review-phase-1'],
      // Deterministic gate for the previously failing path. If impl-cli-add
      // later hangs, this run still has classifier-friendly gate evidence and
      // a bounded timeout on the agent step below.
      command: [
        'set -e',
        'echo "Agent precheck: impl-cli-add"',
        `echo "launcher_budget_ms=${RICKY_LAUNCHER_TIMEOUT_MS}"`,
        `echo "impl_cli_timeout_ms=${PHASE4_IMPL_CLI_TIMEOUT_MS}"`,
        'if [ -f .agent-relay/team/workers.json ]; then',
        '  echo "WARN: stale workers.json detected before impl-cli-add"',
        '  sed -n "1,160p" .agent-relay/team/workers.json',
        'fi',
        'node -e "const { spawnSync } = require(\'node:child_process\'); const r = spawnSync(\'codex\', [\'--version\'], { encoding: \'utf8\', timeout: 20000 }); if (r.error || r.status !== 0) { console.error(\'codex_precheck_failed\'); process.exit(1); } console.log(\'codex_precheck_ok\');"',
        'echo AGENT_PRECHECK_CLI_ADD_OK',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })
    .step('impl-cli-add', {
      agent: 'impl-cli',
      dependsOn: ['agent-precheck-cli-add', 'read-cmd-dispatch'],
      task: [
        `Edit ${MAIN_GO}.`,
        '',
        'Execution guardrails:',
        '  - Start by checking whether `runWritebackStatus` and `runWritebackRetry` already exist.',
        '  - If both already exist and are wired in dispatch/help, verify behavior against this task contract and make only minimal corrective edits.',
        '  - If they are missing/incomplete, implement them fully.',
        '  - Finish by printing exactly `CLI_PHASE4_DONE` in your final output.',
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
      verification: { type: 'output_contains', value: 'CLI_PHASE4_DONE' },
      timeout: PHASE4_IMPL_CLI_TIMEOUT_MS,
    })
    .step('verify-cli-add', {
      type: 'deterministic',
      dependsOn: ['impl-cli-add'],
      command: [
        'set -e',
        `if git diff --quiet ${MAIN_GO}; then echo "main_go_not_modified"; exit 1; fi`,
        `grep -q "writeback status" ${MAIN_GO} || (echo "missing_writeback_status_route"; exit 1)`,
        `grep -q "writeback retry" ${MAIN_GO} || (echo "missing_writeback_retry_route"; exit 1)`,
        'echo OK',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    .step('impl-cli-test', {
      type: 'deterministic',
      dependsOn: ['verify-cli-add'],
      // Keep this step deterministic so resume from a previous run can
      // continue quickly from impl-cli-test without waiting on an agent
      // subprocess that may outlive Ricky's 600s outer launcher timeout.
      command: [
        'set -e',
        `cat > ${STATUS_TEST_GO} <<'GOEOF'`,
        'package main',
        '',
        'import (',
        '	"bytes"',
        '	"encoding/json"',
        '	"errors"',
        '	"os"',
        '	"path/filepath"',
        '	"strings"',
        '	"testing"',
        '	"time"',
        ')',
        '',
        'func TestWritebackStatusReportsFailuresAndJSON(t *testing.T) {',
        '	t.Setenv("HOME", t.TempDir())',
        '	clearRelayfileEnv(t)',
        '',
        '	localDir := t.TempDir()',
        '	if err := ensureMirrorLayout(localDir); err != nil {',
        '		t.Fatalf("ensureMirrorLayout failed: %v", err)',
        '	}',
        '	dlDir := filepath.Join(localDir, ".relay", "dead-letter")',
        '	if err := os.MkdirAll(dlDir, 0o755); err != nil {',
        '		t.Fatalf("mkdir dead-letter failed: %v", err)',
        '	}',
        '	statePayload := []byte(`{"pendingWriteback":0,"failedWritebacks":2}` + "\\n")',
        '	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), statePayload, 0o644); err != nil {',
        '		t.Fatalf("write state failed: %v", err)',
        '	}',
        '	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400,"ts":"2026-05-05T14:00:00Z"}`), 0o644); err != nil {',
        '		t.Fatalf("write op_a failed: %v", err)',
        '	}',
        '	if err := os.WriteFile(filepath.Join(dlDir, "op_b.json"), []byte(`{"opId":"op_b","path":"/notion/b.md","lastStatus":409,"ts":"2026-05-05T14:01:00Z"}`), 0o644); err != nil {',
        '		t.Fatalf("write op_b failed: %v", err)',
        '	}',
        '',
        '	if _, err := upsertWorkspaceDetails(workspaceRecord{',
        '		Name:       "demo",',
        '		ID:         "ws_demo",',
        '		LocalDir:   localDir,',
        '		CreatedAt:  time.Now().UTC().Format(time.RFC3339),',
        '		LastUsedAt: time.Now().UTC().Format(time.RFC3339),',
        '	}); err != nil {',
        '		t.Fatalf("upsertWorkspaceDetails failed: %v", err)',
        '	}',
        '	if err := saveCredentials(credentials{Server: defaultServerURL, Token: testJWTWithWorkspace("ws_demo")}); err != nil {',
        '		t.Fatalf("saveCredentials failed: %v", err)',
        '	}',
        '',
        '	var human bytes.Buffer',
        '	err := run([]string{"writeback", "status", "demo"}, strings.NewReader(""), &human, &human)',
        '	if !errors.Is(err, errWritebackFailuresPresent) {',
        '		t.Fatalf("expected errWritebackFailuresPresent, got %v", err)',
        '	}',
        '	if got := human.String(); !strings.Contains(got, "failed: 2") {',
        '		t.Fatalf("expected failed count in human output, got: %q", got)',
        '	}',
        '',
        '	var jsonOut bytes.Buffer',
        '	err = run([]string{"writeback", "status", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut)',
        '	if !errors.Is(err, errWritebackFailuresPresent) {',
        '		t.Fatalf("expected errWritebackFailuresPresent in --json mode, got %v", err)',
        '	}',
        '	var report struct {',
        '		WorkspaceID  string `json:"workspaceId"`',
        '		Pending      int    `json:"pending"`',
        '		Failed       int    `json:"failed"`',
        '		DeadLettered []struct {',
        '			OpID string `json:"opId"`',
        '		} `json:"deadLettered"`',
        '	}',
        '	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {',
        '		t.Fatalf("parse --json output failed: %v\\npayload:\\n%s", err, jsonOut.String())',
        '	}',
        '	if report.Failed != 2 {',
        '		t.Fatalf("expected failed=2, got %d", report.Failed)',
        '	}',
        '	if len(report.DeadLettered) != 2 {',
        '		t.Fatalf("expected 2 dead-lettered entries, got %d", len(report.DeadLettered))',
        '	}',
        '}',
        '',
        'func TestWritebackStatusNoFailures(t *testing.T) {',
        '	t.Setenv("HOME", t.TempDir())',
        '	clearRelayfileEnv(t)',
        '',
        '	localDir := t.TempDir()',
        '	if err := ensureMirrorLayout(localDir); err != nil {',
        '		t.Fatalf("ensureMirrorLayout failed: %v", err)',
        '	}',
        '	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(`{"pendingWriteback":0,"failedWritebacks":0}`+"\\n"), 0o644); err != nil {',
        '		t.Fatalf("write state failed: %v", err)',
        '	}',
        '',
        '	if _, err := upsertWorkspaceDetails(workspaceRecord{',
        '		Name:       "demo",',
        '		ID:         "ws_demo",',
        '		LocalDir:   localDir,',
        '		CreatedAt:  time.Now().UTC().Format(time.RFC3339),',
        '		LastUsedAt: time.Now().UTC().Format(time.RFC3339),',
        '	}); err != nil {',
        '		t.Fatalf("upsertWorkspaceDetails failed: %v", err)',
        '	}',
        '	if err := saveCredentials(credentials{Server: defaultServerURL, Token: testJWTWithWorkspace("ws_demo")}); err != nil {',
        '		t.Fatalf("saveCredentials failed: %v", err)',
        '	}',
        '',
        '	var human bytes.Buffer',
        '	if err := run([]string{"writeback", "status", "demo"}, strings.NewReader(""), &human, &human); err != nil {',
        '		t.Fatalf("run writeback status failed: %v", err)',
        '	}',
        '	if got := strings.ToLower(human.String()); !strings.Contains(got, "no failures") {',
        '		t.Fatalf("expected no-failures marker, got: %q", human.String())',
        '	}',
        '',
        '	var jsonOut bytes.Buffer',
        '	if err := run([]string{"writeback", "status", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut); err != nil {',
        '		t.Fatalf("run writeback status --json failed: %v", err)',
        '	}',
        '	var report struct {',
        '		Failed       int `json:"failed"`',
        '		DeadLettered []struct{} `json:"deadLettered"`',
        '	}',
        '	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {',
        '		t.Fatalf("parse --json output failed: %v\\npayload:\\n%s", err, jsonOut.String())',
        '	}',
        '	if report.Failed != 0 {',
        '		t.Fatalf("expected failed=0, got %d", report.Failed)',
        '	}',
        '	if len(report.DeadLettered) != 0 {',
        '		t.Fatalf("expected no dead-letter entries, got %d", len(report.DeadLettered))',
        '	}',
        '}',
        'GOEOF',
        `gofmt -w ${STATUS_TEST_GO}`,
        `test -f ${STATUS_TEST_GO}`,
        `grep -q "TestWritebackStatusReportsFailuresAndJSON" ${STATUS_TEST_GO}`,
        'echo CLI_TEST_FILE_READY',
      ].join('\n'),
      captureOutput: true,
      failOnError: true,
    })

    .step('run-cli-test', {
      type: 'deterministic',
      dependsOn: ['impl-cli-test'],
      command:
        'go test -count=1 -run TestWriteback(Status|Retry) ./cmd/relayfile-cli/ 2>&1 | tail -40',
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
        'Re-run: go test -count=1 -run TestWriteback(Status|Retry) ./cmd/relayfile-cli/',
        '',
        'Iterate until green.',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      timeout: AGENT_STEP_TIMEOUT_MS,
    })
    .step('run-cli-test-final', {
      type: 'deterministic',
      dependsOn: ['fix-cli-test'],
      command:
        'go test -count=1 -run TestWriteback(Status|Retry) ./cmd/relayfile-cli/ 2>&1',
      captureOutput: true,
      failOnError: true,
    })

    .step('review-phase-4', {
      type: 'deterministic',
      dependsOn: ['run-cli-test-final'],
      command: [
        'set -e',
        'echo "Phase 4 deterministic review gate"',
        `git diff -- ${MAIN_GO} ${STATUS_TEST_GO} | sed -n "1,260p"`,
        `grep -q "func runWritebackStatus" ${MAIN_GO} || (echo "phase4_review_missing_status_handler"; exit 1)`,
        `grep -q "func runWritebackRetry" ${MAIN_GO} || (echo "phase4_review_missing_retry_handler"; exit 1)`,
        `grep -q "no failures" ${MAIN_GO} || (echo "phase4_review_missing_no_failures_path"; exit 1)`,
        `grep -q "errWritebackFailuresPresent" ${MAIN_GO} || (echo "phase4_review_missing_failure_exit_contract"; exit 1)`,
        `grep -q "retryDeadLetterWriteback" ${MAIN_GO} || (echo "phase4_review_missing_retry_insert_path"; exit 1)`,
        `grep -q "os.Remove(recordPath)" ${MAIN_GO} || (echo "phase4_review_missing_dead_letter_cleanup"; exit 1)`,
        `awk 'BEGIN{infn=0; retry=0; remove=0} /func runWritebackRetry\\(/ {infn=1} infn && /retryDeadLetterWriteback\\(/ {retry=NR} infn && /os.Remove\\(recordPath\\)/ {remove=NR} infn && /^}/ {if (retry==0 || remove==0 || remove < retry) {exit 1} else {exit 0}} END {if (infn==0) exit 1}' ${MAIN_GO}`,
        'go test -count=1 -run TestWriteback(Status|Retry) ./cmd/relayfile-cli/ >/tmp/wf057-phase4-review.log 2>&1',
        'tail -20 /tmp/wf057-phase4-review.log',
        'echo "PHASE 4 APPROVED"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })

    // ── Cross-cutting build + full regression ────────────────────────────
    .step('go-build', {
      type: 'deterministic',
      dependsOn: ['review-phase-4'],
      command: [
        'set +e',
        'go build ./... > /tmp/wf057-go-build.log 2>&1',
        'STATUS=$?',
        'tail -20 /tmp/wf057-go-build.log',
        'echo "GO_BUILD_EXIT=$STATUS"',
        'exit 0',
      ].join('\n'),
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
        'If GO_BUILD_EXIT=0, do nothing.',
        'Else fix the type / build errors and re-run: go build ./...',
      ].join('\n'),
      verification: { type: 'exit_code', value: '0' },
      timeout: AGENT_STEP_TIMEOUT_MS,
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
      timeout: AGENT_STEP_TIMEOUT_MS,
    })
    .step('go-test-all-final', {
      type: 'deterministic',
      dependsOn: ['fix-regressions'],
      command: 'go test -count=1 ./... 2>&1 | tail -20',
      captureOutput: true,
      failOnError: true,
    })

    // ── Final evidence + signoff (bounded side effects only) ────────────
    .step('collect-evidence', {
      type: 'deterministic',
      dependsOn: ['go-test-all-final'],
      command: [
        'set -e',
        'echo "=== git status --short ==="',
        'git status --short',
        'echo "---"',
        'echo "=== diffstat ==="',
        `git diff --stat -- ${MAIN_GO} ${DAEMON_TEST_GO} ${STATUS_TEST_GO} go.mod go.sum`,
        'echo "---"',
        'echo "=== acceptance evidence ==="',
        'go test -count=1 -run TestWritebackDaemonDeadLettersHTTP400 ./cmd/relayfile-cli/',
        'go test -count=1 -run TestWriteback(Status|Retry) ./cmd/relayfile-cli/',
        'go build ./...',
        'go test -count=1 ./...',
        'echo "EVIDENCE_OK"',
      ].join(' && '),
      captureOutput: true,
      failOnError: true,
    })
    // No lead-signoff agent step — collect-evidence is already deterministic
    // and emits EVIDENCE_OK on success (failOnError: true). A codex agent
    // writing a free-form summary on top of that adds nothing the human
    // reviewer doesn't already see in `git status` and the PR diff.

    .onError('retry', { maxRetries: 2, retryDelayMs: 10_000 })
    .run({ cwd: process.cwd() });

  console.log('Workflow status:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
