# Trajectory: Fix Bugbot findings on PR #507 relay-cli surface

> **Status:** ✅ Completed
> **Task:** PR-507
> **Confidence:** 85%
> **Started:** September 17, 2026 at 04:29 PM
> **Completed:** September 17, 2026 at 04:32 PM

---

## Summary

Fixed 3 of 4 PR #507 review findings (version routing, Windows .exe checkout lookup, postinstall source-checkout skip); finding 2 was already fixed on the branch

**Approach:** Standard approach

---

## Key Decisions

### Route `version` in the surface without declaring it in the command tree
- **Chose:** Route `version` in the surface without declaring it in the command tree
- **Reasoning:** The Go binary handles `version` in wantsVersion() outside its command table and does not list it in printUsage, so the generated command-spec snapshot must not carry it; declaring it would need a new Go command or a hand-edit that check:command-spec flags as drift

### Duplicate source-checkout detection inside packages/cli/scripts/install.js
- **Chose:** Duplicate source-checkout detection inside packages/cli/scripts/install.js
- **Reasoning:** postinstall runs before packages/sdk/typescript/dist exists on a fresh clone, so it cannot import findSourceCheckoutRoot from the SDK to decide to skip; install.test.js pins the local copy against the SDK's implementation

### Kept RELAY_CLI_EXIT_BINARY_NOT_FOUND (127) rather than mirroring the shim's exit 1
- **Chose:** Kept RELAY_CLI_EXIT_BINARY_NOT_FOUND (127) rather than mirroring the shim's exit 1
- **Reasoning:** Finding 2 was already fixed on the branch; 127 is a documented constant that lets a host distinguish 'relayfile is not installed here' from 'relayfile ran and failed', and clean-install.test.ts already asserts it

---

## Chapters

### 1. Work
*Agent: default*

- Route `version` in the surface without declaring it in the command tree: Route `version` in the surface without declaring it in the command tree
- Duplicate source-checkout detection inside packages/cli/scripts/install.js: Duplicate source-checkout detection inside packages/cli/scripts/install.js
- Kept RELAY_CLI_EXIT_BINARY_NOT_FOUND (127) rather than mirroring the shim's exit 1: Kept RELAY_CLI_EXIT_BINARY_NOT_FOUND (127) rather than mirroring the shim's exit 1

---

## Artifacts

**Commits:** 50cf4d5c
**Files changed:** 6
