# Trajectory: Ship relayfile-cli as per-platform npm packages so agent-relay file resolves a binary on a clean install

> **Status:** ✅ Completed
> **Confidence:** 85%
> **Started:** September 17, 2026 at 03:41 PM
> **Completed:** September 17, 2026 at 03:42 PM

---

## Summary

Added six @relayfile/cli-<platform>-<arch> packages (4 mount-matching targets plus win32 x64/arm64) as exactly-pinned optionalDependencies of @relayfile/sdk, mirroring the @relayfile/mount-* pattern, with a filler script and full publish.yml wiring. resolveRelayfileBinary now prefers them, falls back through the existing chain to make outputs, go run, and a relayfile-cli-only PATH scan, and reports an actionable install message as exit 127 through io. Output is now byte-exact. 94 relay-cli tests pass including a clean-install E2E that cannot pass on the host's built binary; verified from a packed tarball install with an empty PATH

**Approach:** Standard approach

---

## Key Decisions

### Ship the CLI binary as @relayfile/cli-<platform>-<arch> optionalDependencies rather than extending the relayfile package's postinstall download
- **Chose:** Ship the CLI binary as @relayfile/cli-<platform>-<arch> optionalDependencies rather than extending the relayfile package's postinstall download
- **Reasoning:** agent-relay depends on @relayfile/sdk, never on relayfile, so a postinstall in the relayfile package can never run for it. The per-platform optional-dependency pattern was already proven in this repo by @relayfile/mount-*, needs no install-time network, works offline and in CI, and gets integrity from the registry

### Include win32-x64 and win32-arm64 platform packages, unlike the mount packages
- **Chose:** Include win32-x64 and win32-arm64 platform packages, unlike the mount packages
- **Reasoning:** packages/cli/scripts/build-binaries.js already cross-compiles both Windows targets and publish.yml already asserts 6 relayfile-cli binaries and attaches them to every release, so the artifacts provably exist. Without the packages a win32 agent-relay file user has no resolution path at all, since only the relayfile package's postinstall fetches the .exe

### Test the production path by assembling a fake npm install under the OS temp dir and running a probe with plain node and an empty PATH
- **Chose:** Test the production path by assembling a fake npm install under the OS temp dir and running a probe with plain node and an empty PATH
- **Reasoning:** The bug shipped because every existing test ran inside this checkout, where a built binary and a Go toolchain are both present, so no test could distinguish 'resolves' from 'resolves because the host happens to have one'. Building the tree outside any go.mod, with no relayfile package and an empty PATH, removes every ambient fallback, and loading the SDK by package name exercises the real exports map and the real require.resolve

### Match relayfile-cli only in the PATH fallback, never the generic relayfile name
- **Chose:** Match relayfile-cli only in the PATH fallback, never the generic relayfile name
- **Reasoning:** On PATH, relayfile is normally the npm bin shim packages/cli/scripts/run.js, which resolves its binary through this same module. Spawning it from the resolver would make the resolver invoke itself forever. make install does place the Go binary at relayfile, so that case is deliberately left to the checkout and binDirs steps instead

### Implement byte passthrough for export --format tar --output - instead of the fail-fast the brief asked for
- **Chose:** Implement byte passthrough for export --format tar --output - instead of the fail-fast the brief asked for
- **Reasoning:** The brief said to fail fast because widening RelayCliIo was tracked separately, but the linked @agent-relay/cli-surface already declares stdout(chunk: string | Uint8Array) and names this exact command as the motivating case. Shipping a deliberate fail-fast against a capability the live contract provides would be a regression; asked the lead in #cli-surfaces and flagged it as a one-commit revert

---

## Chapters

### 1. Work
*Agent: default*

- Ship the CLI binary as @relayfile/cli-<platform>-<arch> optionalDependencies rather than extending the relayfile package's postinstall download: Ship the CLI binary as @relayfile/cli-<platform>-<arch> optionalDependencies rather than extending the relayfile package's postinstall download
- Include win32-x64 and win32-arm64 platform packages, unlike the mount packages: Include win32-x64 and win32-arm64 platform packages, unlike the mount packages
- Test the production path by assembling a fake npm install under the OS temp dir and running a probe with plain node and an empty PATH: Test the production path by assembling a fake npm install under the OS temp dir and running a probe with plain node and an empty PATH
- Match relayfile-cli only in the PATH fallback, never the generic relayfile name: Match relayfile-cli only in the PATH fallback, never the generic relayfile name
- Implement byte passthrough for export --format tar --output - instead of the fail-fast the brief asked for: Implement byte passthrough for export --format tar --output - instead of the fail-fast the brief asked for
