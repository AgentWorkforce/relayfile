# Trajectory Compaction: 2026-04-20 - 2026-05-09

## Summary
This run captures a dense stretch of work on the Relayfile project (SDK, cloud, adapters, CLI, eval harness, mount layer). The dominant arc was the SDK setup-client contract: workflow 062-sdk-setup-client.ts was authored with hard 80-to-100 validation gates, then iteratively repaired across multiple sessions — fixing the verify-edits-landed gate by adding the spec-required IntegrationConnectionTimeoutError to @relayfile/sdk and switching the generated cloud route test to Vitest (tsx --test couldn't run vi.mock), correcting capture-final-evidence to use absolute paths and git -C after a cd-in-redirect bug, and removing out-of-scope cloud script-generator churn before the review gate would approve. PR 65 review/feedback cycles aligned the implementation with docs/sdk-setup-client.md (relayfile JWT auth, runtime export of WORKSPACE_INTEGRATION_PROVIDERS, https://agentrelay.com/cloud as the canonical default per ../cloud/infra/web-routing.ts and packages/cli/src/cli/constants.ts).

## Key Decisions (10)
| Question | Decision | Impact |
|----------|----------|--------|
| How to package multiple independent follow-up PRs (cloud conventions, cloud sdk/core bump, adapters release pipeline) | Fan-out workers in separate worktrees | Produced cloud#504, cloud#505, and relayfile-adapters#59 in one session; rebased cloud branches after cloud#503 merged. |
| Linear slug-prefixed canonical path truncation budget | 255-byte path-segment guidance, not 80-char alias slug | cloud#504 guidance corrected with regression assertions in cataloging-agent-linear. |
| relayfile-adapters publish ordering | Topologically sort publish targets so adapter-core publishes before dependents | relayfile-adapters#59 fixes stale adapter-core ranges; npm test + GitHub Build & Test pass. |
| Async createMount API shape (issue #104) | Make createMount return Promise<MountHandle> directly; no parallel createMountAsync | Walker yields via setImmediate per directory and every 64 entries in flat dirs; updated launch.ts and all mount/auto-sync/launch tests, added event-loop yield regression test. |
| Where to put relayfile-specific eval checks | Local to the relayfile runner, reusing shared loading/artifact APIs | Added 5 suites / 13 cases, npm scripts, docs, gitignore; full+VFS+compile+list offline evals and typecheck all pass. |
| Guided CLI vs explicit subcommands | Bare relayfile / relayfile setup as guided path on top of existing token-first subcommands | Bare command runs cloud login → workspace create/join → Nango connect session → mount sync; CLI tests + go test ./... pass. |
| Agent workspace shape — second abstraction or extend WorkspaceHandle | One-handle helpers on existing WorkspaceHandle (connectNotion, waitForNotion, mountEnv, agentInvite) | Avoids a second setup abstraction; covered by unit + packaged E2E. |
| Golden-path workflow shape | Single DAG with parallel implementation tracks and hard E2E gate | workflows/063-agent-workspace-golden-path.ts authored with acceptance lock, edit verification, test-fix-rerun, packaged E2E, evidence, review, separate commits. |
| Local-mount file watcher | @parcel/watcher 2.5.6 over chokidar v4 | Replaced awaitWriteFinish with per-path setTimeout debounce; reconcile loop + mtime/content check absorb looser write semantics; 22 tests pass; exit-hang resolved. |
| Auth strategy for relayfile e2e/conformance script harnesses | RS256 with startup-local JWKS server, shared signer helper | Scripts pass RELAYAUTH_JWKS_URL to spawned Go server and close JWKS listener during teardown. |

## Conventions Established
- **80-to-100 workflow gates: verify-edits-landed, capture-final-evidence, verify-review-approved must stay strict; fix the contract or scope, never weaken the gate**: Workflow 062 demonstrated that gate failures revealed real spec drift (missing IntegrationConnectionTimeoutError, out-of-scope cloud churn) — relaxing them would have shipped wrong code. (scope: All workflows under workflows/ that follow the relay-80-100 pattern.)
- **Use absolute paths and `git -C <repo>` for evidence/check steps that touch multiple repos; never `cd` inside a redirected output block**: capture-final-evidence resolved docs/evidence relative to ../cloud after a cd inside the block, silently writing to the wrong repo. (scope: Multi-repo workflow steps with shell redirection.)
- **Choose test runner to match the test file: vitest for vi.mock-based tests, tsx --test for Node-runner-compatible tests**: Mixed runners in one workflow caused verify-edits-landed to fail when tsx tried to execute vi.mock. (scope: Workflow-generated test invocations.)
- **Treat docs/sdk-setup-client.md and ../cloud source files (infra/web-routing.ts, packages/cli/src/cli/constants.ts) as the authoritative SDK contract**: PR 65 review and workflow 062 both surfaced spec/impl drift; aligning the doc to ../cloud's appUrl='/cloud' fixed obsolete app.agentrelay.com references. (scope: @relayfile/sdk setup client and any cloud routing assumptions.)
- **Linear slug-prefixed canonical filenames truncate per the 255-byte path-segment budget, not the 80-char alias slug**: nameWithId is the source of truth; cataloging-agent-linear regression assertions encode this. (scope: cloud canonical path documentation and any new alias-aware tooling.)
- **Topologically sort publish targets when syncing internal dependency ranges before publish**: Dependents reference adapter-core's just-published version; out-of-order publishes leave stale ranges. (scope: relayfile-adapters release pipeline and similar monorepo publishers.)
- **Long-running init that loops over directories should yield via setImmediate at directory entry plus every 64 entries within a directory**: createMount async refactor — keeps consumer setInterval timers firing during heavy walks without measurable perf impact. (scope: Walkers/scanners called from CLI init paths (launch.ts and similar).)
- **Extend the existing WorkspaceHandle with one-handle helpers rather than adding a second setup object**: WorkspaceHandle already aggregates the credentials and metadata downstream agents need. (scope: @relayfile/sdk surface for agent workspace flows.)

## Lessons Learned
- Workflow gate failures are usually spec/impl drift, not gate bugs (Workflow 062 verify-edits-landed initially failed because the generated SDK exported CloudTimeoutError instead of IntegrationConnectionTimeoutError; multiple sessions to repair.) - On gate failure, diff against docs/sdk-setup-client.md and fix the implementation; only adjust the gate when the gate genuinely tests the wrong thing (e.g., wrong runner).
- Never `cd` inside a `cmd > path` block when path is relative (capture-final-evidence wrote to ../cloud/docs/evidence after switching directories mid-redirect.) - Resolve evidence paths absolutely up-front and use `git -C <repo>` for per-repo git commands.
- Native file watchers beat polling for clean process exit (chokidar v4's awaitWriteFinish + fs.watch left timers/handles that hung exit in packages/local-mount.) - Prefer @parcel/watcher (or platform-native) and implement debouncing with explicit setTimeout; rely on a reconcile loop + mtime/content check to absorb looser semantics.
- Out-of-scope churn blocks reviews even when functionality is correct (verify-review-approved on PR 65 failed due to unrelated cloud script-generator/start-from edits.) - Restore unrelated hunks before review; keep untracked unrelated work outside the branch.
- Pre-1.0 packages should change the function signature rather than ship parallel async/sync entry points (createMount became Promise<MountHandle> directly per issue #104.) - For single-consumer pre-1.0 APIs, prefer breaking change + consumer update over maintaining twin surfaces.
- Trajectory noise can leak into dependency PRs (Stray trajectory files were removed from cloud#505 during the fan-out follow-up.) - Verify .trajectories/* exclusions or scrub before pushing dependency-only branches.

## Open Questions
- CodeRabbit review on relayfile-adapters#59 still pending — does it require additional changes beyond the topological sort?
- Workflow 063-agent-workspace-golden-path.ts is authored but its end-to-end execution outcome across relayfile/cloud/relaycast is not captured in these trajectories.
- Should the per-64-entry yield interval in createMount be tunable for very deep or very flat trees, or is the fixed 64 sufficient long-term?
- Are there other workflows currently mixing tsx --test and vitest test files that could hit the same verify-edits-landed false negative as workflow 062?

## Stats
- Sessions: 20, Agents: default, orchestrator, Files: 0, Commits: 0
- Date range: 2026-04-20T20:35:15.759Z - 2026-05-09T13:57:04.293Z