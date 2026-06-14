# Trajectory: Fix PR 149 conflicts and add eval GitHub Action

> **Status:** ❌ Abandoned
> **Started:** May 14, 2026 at 11:58 AM
> **Completed:** May 21, 2026 at 12:39 PM

---

## Key Decisions

### Add explicit CLI progress before initial sync wait
- **Chose:** Add explicit CLI progress before initial sync wait
- **Reasoning:** Provider OAuth readiness can complete before Relayfile sync status has a provider row, leaving integration connect silent while backend work continues.

### Clarified integration connect ready-to-sync handoff
- **Chose:** Clarified integration connect ready-to-sync handoff
- **Reasoning:** User feedback showed the CLI looked stuck after provider connection; the connected message now explicitly says Relayfile is preparing files and the command should remain running until initial sync finishes.

### Accepted workspace-scoped integration status fallback for CLI connect
- **Chose:** Accepted workspace-scoped integration status fallback for CLI connect
- **Reasoning:** GitHub can complete OAuth while the provisional session connectionId is not the identifier Cloud has persisted yet; the CLI now retries status without connectionId and accepts connected state values so it does not time out after a successful provider connect.

### Tightened GitHub status fallback regression test
- **Chose:** Tightened GitHub status fallback regression test
- **Reasoning:** PR feedback found a possible data race and incomplete assertion. The test now uses atomic flags and verifies both connection-scoped and workspace-scoped probes occurred.

### Memory pressure is mostly local mirror/sync breadth, not inherent relayfile baseline
- **Chose:** Memory pressure is mostly local mirror/sync breadth, not inherent relayfile baseline
- **Reasoning:** The local mount defaults to polling mirror mode, scans/hashes local files, writes full public state, and full-pulls/export snapshots; GitHub repo hydration can be lazy via RELAYFILE_LAZY_REPOS/--lazy-repos.

### Added low-memory mount mode and diagnostics before broader rate-limit work
- **Chose:** Added low-memory mount mode and diagnostics before broader rate-limit work
- **Reasoning:** Local memory pressure has clear code hot spots in full-pull snapshots and public-state scans; the shared 429 requires separate backend isolation but the mount fixes are safe and independently useful.

### Use JavaScript digest fixtures for CI Go tests
- **Chose:** Use JavaScript digest fixtures for CI Go tests
- **Reasoning:** GitHub Go jobs do not install npm dependencies, so TypeScript digest-function execution cannot rely on tsx there.

### Make watcher debounce test exercise queueChange directly
- **Chose:** Make watcher debounce test exercise queueChange directly
- **Reasoning:** The test is about the debounce timer, while real fsnotify write bursts can emit extra platform-dependent events outside the debounce window in CI.

### Document custom digest functions in a separate stacked PR
- **Chose:** Document custom digest functions in a separate stacked PR
- **Reasoning:** The implementation PR only had a spec; user-facing docs belong in a docs-only branch targeted at the implementation branch so reviewers see only documentation changes.

### Writing a repo-owned gap spec for PR 39 skill implementation coverage
- **Chose:** Writing a repo-owned gap spec for PR 39 skill implementation coverage
- **Reasoning:** The user clarified they need functional loose ends by repo, not skill installation status; a durable spec in relayfile docs can coordinate relayfile, cloud, and relayfile-adapters work.

### Fix PR 169 skills by adding Agent Skills frontmatter
- **Chose:** Fix PR 169 skills by adding Agent Skills frontmatter
- **Reasoning:** PRPM agent-skills schema requires SKILL.md YAML frontmatter with name and description; the new files were plain Markdown, so validation fails before content is considered.

### Split @relayfile/sdk CLI-only login and mount launcher behind explicit subpath exports
- **Chose:** Split @relayfile/sdk CLI-only login and mount launcher behind explicit subpath exports
- **Reasoning:** The default entry imported setup, which imported mount-launcher and cloud-login; moving default launcher/login behavior to @relayfile/sdk/cli keeps Worker bundles from statically reaching node:child_process/node:http while preserving explicit Node entry points.

### Fix issue 173 by making quiet event cycles honor forced and periodic full pulls
- **Chose:** Fix issue 173 by making quiet event cycles honor forced and periodic full pulls
- **Reasoning:** Remote provider records can appear without new filesystem events, so the event-feed short-circuit must not bypass --full-reconcile or the trust-but-verify full-tree cadence.

### Tightened issue 173 CLI guardrails after subagent review
- **Chose:** Tightened issue 173 CLI guardrails after subagent review
- **Reasoning:** --rehome could otherwise orphan a running old daemon, and broad slash-based tree path inference could hijack slash-containing workspace names; rehome now refuses running old mounts and tree only infers paths for known mirror roots.

### Expanded issue 175 fix beyond PR 174 review comments
- **Chose:** Expanded issue 175 fix beyond PR 174 review comments
- **Reasoning:** Issue 175 identified foreground mount visibility, repeated oversized-file logs, and fragile incremental event backlog recovery as remaining relayfile-side gaps; patched those alongside the CodeRabbit rehome/name-fallback comments.

### Added intra-page incremental checkpoint for issue 175
- **Chose:** Added intra-page incremental checkpoint for issue 175
- **Reasoning:** Latest production repro showed event-page cursor checkpointing still repeats the same sorted ReadFile sweep when a timeout happens mid-page. A persisted page cursor + phase + path watermark lets the next cycle resume within that page and converge instead of rereading early paths forever.

---

## Chapters

### 1. Work
*Agent: default*

- Add explicit CLI progress before initial sync wait: Add explicit CLI progress before initial sync wait
- Clarified integration connect ready-to-sync handoff: Clarified integration connect ready-to-sync handoff
- CLI connect feedback now covers the post-OAuth initial-sync wait; focused relayfile-cli tests pass.
- Accepted workspace-scoped integration status fallback for CLI connect: Accepted workspace-scoped integration status fallback for CLI connect
- Fresh PR branch is based on origin/main; only CLI connect feedback/status fallback files are intended for commit. relayfile-cli tests pass.
- Tightened GitHub status fallback regression test: Tightened GitHub status fallback regression test
- Memory pressure is mostly local mirror/sync breadth, not inherent relayfile baseline: Memory pressure is mostly local mirror/sync breadth, not inherent relayfile baseline
- Added low-memory mount mode and diagnostics before broader rate-limit work: Added low-memory mount mode and diagnostics before broader rate-limit work
- Use JavaScript digest fixtures for CI Go tests: Use JavaScript digest fixtures for CI Go tests
- CI failures traced to tests depending on local tsx and to an OS-event debounce flake. The fixes keep digest-function coverage while matching CI's no-npm Go environment and make debounce coverage deterministic.
- Make watcher debounce test exercise queueChange directly: Make watcher debounce test exercise queueChange directly
- Document custom digest functions in a separate stacked PR: Document custom digest functions in a separate stacked PR
- Writing a repo-owned gap spec for PR 39 skill implementation coverage: Writing a repo-owned gap spec for PR 39 skill implementation coverage
- Drafted workspace primitive PR39 gap spec with repo ownership, acceptance checks, and cross-repo work items
- Starting final cross-repo ripple check on PR39 branches and PRs.
- Fix PR 169 skills by adding Agent Skills frontmatter: Fix PR 169 skills by adding Agent Skills frontmatter
- Fixed PR 169 skill validity by adding required Agent Skills YAML frontmatter to both new workforce SKILL.md files; PRPM codex skill schema validation now passes for both.
- Split @relayfile/sdk CLI-only login and mount launcher behind explicit subpath exports: Split @relayfile/sdk CLI-only login and mount launcher behind explicit subpath exports
- SDK default entry is Worker-safe by static import graph, full SDK tests, and an esbuild browser bundle check; CLI workflows remain available through explicit subpaths.
- Completed issue 170 implementation and validation: default @relayfile/sdk has no static CLI Node imports; docs point login/mount users to @relayfile/sdk/cli; package exports expose explicit CLI subpaths.
- Fix issue 173 by making quiet event cycles honor forced and periodic full pulls: Fix issue 173 by making quiet event cycles honor forced and periodic full pulls
- Issue 173 patch covers forced full reconcile, quiet-cycle periodic full pulls, mount re-home guard, and path-like tree args; focused tests and contract surface check pass
- Starting hard self-review with subagents for issue 173 patch; splitting mountsync correctness and CLI guardrail review before broader verification
- Tightened issue 173 CLI guardrails after subagent review: Tightened issue 173 CLI guardrails after subagent review
- Hard review complete: mountsync subagent found no issues; CLI subagent found rehome-daemon and broad tree heuristic risks, both patched with added regression tests; uncached go test ./..., targeted repeat tests, contract check, and diff check pass
- Expanded issue 175 fix beyond PR 174 review comments: Expanded issue 175 fix beyond PR 174 review comments
- Issue 175 relayfile-side follow-up is implemented and reviewed by Codex + Claude. CLI now preserves recorded mirrors, foreground mounts register pid state, rehome refuses unverified pid state, oversized local-file logs are deduped, and incremental event backlog progress is persisted only after applied pages. Full tests and contract check passed.
- Added intra-page incremental checkpoint for issue 175: Added intra-page incremental checkpoint for issue 175
- Abandoned: Superseded by new issue #178 work
