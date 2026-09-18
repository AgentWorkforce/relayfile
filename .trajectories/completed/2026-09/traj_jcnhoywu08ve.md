# Trajectory: Expose @relayfile/sdk/relay-cli CLI surface for agent-relay file

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** September 17, 2026 at 02:00 PM
> **Completed:** September 17, 2026 at 02:27 PM

---

## Summary

Added @relayfile/sdk/relay-cli CLI surface (23 top-level commands) for agent-relay file: declarative Go command table now drives run() dispatch and feeds a hidden __command-spec --json emitter snapshotted into the SDK; binary resolution and cloud preflight moved into the SDK as the single home, with run.js and install.js calling them; AST drift tests in Go, regenerate-and-diff snapshot test, and real-Go-binary E2E through surface.run()

**Approach:** Standard approach

---

## Key Decisions

### Declarative Go command table drives run() dispatch; AST drift tests for nested levels
- **Chose:** Declarative Go command table drives run() dispatch; AST drift tests for nested levels
- **Reasoning:** relayfile's Go CLI is a hand-rolled flag dispatcher, not cobra, so there is no command tree to walk. Making the table the dispatcher removes top-level drift structurally; parsing the per-group switches and each leaf FlagSet out of the source AST catches nested drift without rewriting 14.7k lines.

---

## Chapters

### 1. Work
*Agent: default*

- Declarative Go command table drives run() dispatch; AST drift tests for nested levels: Declarative Go command table drives run() dispatch; AST drift tests for nested levels
- Deliverable complete and pushed: SDK relay-cli surface, single-homed binary resolver + cloud preflight, Go command-spec emitter with AST drift tests, real-binary E2E. Awaiting lead decision on mount-with-args+subcommands and PR.

---

## Artifacts

**Commits:** c4456003
**Files changed:** 30
