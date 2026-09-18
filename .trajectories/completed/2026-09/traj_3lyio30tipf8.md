# Trajectory: Fix four Devin findings on relayfile PR #507 (CLI surface)

> **Status:** ✅ Completed
> **Task:** PR-507
> **Confidence:** 85%
> **Started:** September 17, 2026 at 11:29 PM
> **Completed:** September 17, 2026 at 11:29 PM

---

## Summary

Fixed 3 of 4 Devin findings on PR #507 and rejected the 4th as already fixed on-branch. go-run now builds then executes so the child keeps the caller's cwd (shared by the mounted surface and the bin shim); listen/dev/workspace-status declare their workspace positional; supervisor install declares runListen's flags instead of a --interval runListen rejects. Closed both AST drift blind spots. Tests: vitest relay-cli 105 pass/0 fail (was 97), SDK-wide 416 pass/1 pre-existing fail (client.test.ts ErrorEvent, fails on the unmodified branch too); go ./cmd/relayfile-cli 386 pass/0 fail/8 skip (was 382); packages/cli 15 pass/0 fail (was 14).

**Approach:** Standard approach

---

## Key Decisions

### Fix the go-run cwd bug by building then executing, in the shared resolver
- **Chose:** Fix the go-run cwd bug by building then executing, in the shared resolver
- **Reasoning:** go run gives the launched program the go command's own working directory, and go only finds the module from the checkout. I verified both escape hatches fail: an absolute package path outside a module errors ('go.mod file not found'), and 'go -C <dir> run' hands the child that same dir. Building to a temp path (not the checkout's bin/, so a working tree is never written to) and spawning the binary with the caller's cwd is the only way to separate the two. Put it in resolve-binary.ts so the CLI shim and the mounted surface share one implementation.

### Declare listen's workspace positional on listen, dev and workspace status
- **Chose:** Declare listen's workspace positional on listen, dev and workspace status
- **Reasoning:** runListen reads fs.Arg(0) as the workspace and dev forwards argv to it verbatim; the host builds its parser from the emitted spec, so an undeclared positional is a rejected-but-valid invocation. workspace status is the same bug, surfaced by the new AST guard rather than by the review.

### Give supervisor install flagSource runListen instead of deleting --interval
- **Chose:** Give supervisor install flagSource runListen instead of deleting --interval
- **Reasoning:** The drift test exempted 'supervisor install' from the flag check (isPassThroughCommand) precisely because it declared options with no flagSource — that exemption is why --interval survived. Removing the exemption and pointing flagSource at runListen makes the table's claim checkable: supervisor install embeds its argv into ExecStart as 'relayfile listen ...', so its options are listen's options, no more and no less. Deleting --interval alone would have left it under-declaring the flags it really forwards.

### Rejected finding 1 (binary stdout) as already fixed
- **Chose:** Rejected finding 1 (binary stdout) as already fixed
- **Reasoning:** setEncoding was removed in af70448f on this same branch, before the review round. I mutation-checked the covering test (binary-output.test.ts) by reintroducing setEncoding: it fails on the 0xff 0xfe payload, so the guard is real, not vacuous. No change made.

---

## Chapters

### 1. Work
*Agent: default*

- Fix the go-run cwd bug by building then executing, in the shared resolver: Fix the go-run cwd bug by building then executing, in the shared resolver
- Declare listen's workspace positional on listen, dev and workspace status: Declare listen's workspace positional on listen, dev and workspace status
- Give supervisor install flagSource runListen instead of deleting --interval: Give supervisor install flagSource runListen instead of deleting --interval
- Rejected finding 1 (binary stdout) as already fixed: Rejected finding 1 (binary stdout) as already fixed

---

## Artifacts

**Commits:** c1751c42
**Files changed:** 12
