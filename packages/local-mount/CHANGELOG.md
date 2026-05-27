# Changelog

All notable changes to `@relayfile/local-mount` are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_No unreleased changes._

## [0.8.2] - 2026-05-27

_No user-visible changes in this release._

## [0.8.1] - 2026-05-27

_No user-visible changes in this release._

## [0.8.0] - 2026-05-26

_No user-visible changes in this release._

## [0.7.40] - 2026-05-25

_No user-visible changes in this release._

## [0.7.39] - 2026-05-23

_No user-visible changes in this release._

## [0.7.38] - 2026-05-22

_No user-visible changes in this release._

## [0.7.37] - 2026-05-22

_No user-visible changes in this release._

## [0.7.36] - 2026-05-22

_No user-visible changes in this release._

## [0.7.35] - 2026-05-22

_No user-visible changes in this release._

## [0.7.34] - 2026-05-22

_No user-visible changes in this release._

## [0.7.33] - 2026-05-21

_No user-visible changes in this release._

## [0.7.32] - 2026-05-21

_No user-visible changes in this release._

## [0.7.31] - 2026-05-21

_No user-visible changes in this release._

## [0.7.30] - 2026-05-21

_No user-visible changes in this release._

## [0.7.29] - 2026-05-21

_No user-visible changes in this release._

## [0.7.28] - 2026-05-21

_No user-visible changes in this release._

## [0.7.27] - 2026-05-21

_No user-visible changes in this release._

## [0.7.26] - 2026-05-21

_No user-visible changes in this release._

## [0.7.25] - 2026-05-21

_No user-visible changes in this release._

## [0.7.24] - 2026-05-20

### Changed
- Initial mount and auto-sync file copies now request filesystem reflinks when available, while preserving byte-copy fallback behavior on filesystems without copy-on-write support.
- `createMount` now reports `initialFileCount` and `initialMountDurationMs` on the returned handle for caller-side mount setup telemetry.

## [0.7.23] - 2026-05-19

_No user-visible changes in this release._

## [0.7.22] - 2026-05-18

_No user-visible changes in this release._

## [0.7.21] - 2026-05-15

_No user-visible changes in this release._

## [0.7.20] - 2026-05-14

_No user-visible changes in this release._

## [0.7.19] - 2026-05-14

_No user-visible changes in this release._

## [0.7.18] - 2026-05-14

### Changed
- `startAutoSync` now runs periodic full reconciles on a slower default cadence while watcher subscriptions are healthy, falls back to the existing 10s cadence when watchers are degraded, and accepts `scanIntervalMs: 0` or `Infinity` to disable periodic full reconciles. (#135)

## [0.7.17] - 2026-05-14

_No user-visible changes in this release._

## [0.7.16] - 2026-05-14

_No user-visible changes in this release._

## [0.7.15] - 2026-05-14

_No user-visible changes in this release._

## [0.7.14] - 2026-05-14

_No user-visible changes in this release._

## [0.7.13] - 2026-05-13

_No user-visible changes in this release._

## [0.7.12] - 2026-05-13

_No user-visible changes in this release._

## [0.7.11] - 2026-05-12

_No user-visible changes in this release._

## [0.7.10] - 2026-05-12

_No user-visible changes in this release._

## [0.7.9] - 2026-05-12

_No user-visible changes in this release._

## [0.7.8] - 2026-05-11

### Changed
- `launchOnMount` now uses auto-sync's dirty path state for the final sync-back when watcher subscriptions stayed healthy, avoiding a full mount-tree walk on idle or low-change sessions. The full sweep remains the fallback when auto-sync is disabled or watcher state is degraded. (#134)
- `launchOnMount` now waits for auto-sync watcher readiness before running `onBeforeLaunch` or spawning the child process, so the dirty-path final sync-back cannot miss short-lived writes made before watcher events are trusted. (#134)

## [0.7.7] - 2026-05-11

### Changed
- `createMount` now requests non-forcing filesystem reflink clones for initial project-to-mount file copies, falling back to ordinary byte copies on unsupported filesystems or cross-device mounts. (#132)

## [0.7.6] - 2026-05-11

_No user-visible changes in this release._

## [0.7.5] - 2026-05-11

_No user-visible changes in this release._

## [0.7.4] - 2026-05-09

_No user-visible changes in this release._

## [0.7.3] - 2026-05-09

_No user-visible changes in this release._

## [0.7.2] - 2026-05-09

_No user-visible changes in this release._

## [0.7.1] - 2026-05-09

_No user-visible changes in this release._

## [0.7.0] - 2026-05-08

### Changed
- **BREAKING:** `createMount` is now async and returns `Promise<MountHandle>`. The walker yields the event loop between directory entries during init, so a consumer's `setInterval` (e.g. an `ora` spinner) keeps firing while the mount is being built. Previously the synchronous walker froze the consumer's event loop for the entire init window, causing spinners to display a static frame. Callers must `await createMount(...)` — there is no `createMountSync`. `launchOnMount` already awaits it internally so its surface is unchanged. (#104)

## [0.6.15] - 2026-05-08

_No user-visible changes in this release._

## [0.6.14] - 2026-05-08

### Changed
- `.npm-cache` is now excluded by default alongside `.git` and `node_modules`. Project-local npm caches accumulate tens of thousands of files (often hundreds of MB) that have no place inside an agent's mount; both the initial walk and the autosync `@parcel/watcher` subscription now skip them automatically. The `excludeDirs` option only adds to defaults — there is no way to opt the cache back in — so callers who need an npm cache visible inside the mount should point npm at a different location (e.g. via `npm_config_cache`) or populate it post-mount from `onBeforeLaunch`.
- The autosync watcher now derives its ignore globs from the live `excludeDirs` set instead of a hardcoded probe list. User-supplied `excludeDirs` entries (not just the library defaults) now produce `@parcel/watcher` ignore globs, so custom heavy directories the caller declares are skipped at subscription time as well as during the initial walk. Bare directory names (e.g. `node_modules`) are matched at any depth, while path-style entries (e.g. `build/cache`) are anchored at each watch root — mirroring `isExcludedPath`'s root-anchored prefix semantics so the watcher hint never hides events that the canonical predicate would have allowed.

## [0.6.13] - 2026-05-08

_No user-visible changes in this release._

## [0.6.11] - 2026-05-07

_No user-visible changes in this release._

## [0.6.10] - 2026-05-07

_No user-visible changes in this release._

## [0.6.9] - 2026-05-06

_No user-visible changes in this release._

## [0.6.8] - 2026-05-06

_No user-visible changes in this release._

## [0.6.7] - 2026-05-06

_No user-visible changes in this release._

## [0.6.6] - 2026-05-06

_No user-visible changes in this release._

## [0.6.5] - 2026-05-06

_No user-visible changes in this release._

## [0.6.4] - 2026-05-06

_No user-visible changes in this release._

## [0.6.3] - 2026-05-05

_No user-visible changes in this release._

## [0.6.2] - 2026-05-05

_No user-visible changes in this release._

## [0.6.1] - 2026-05-01

_No user-visible changes in this release._

## [0.6.0] - 2026-04-30

### Added
- `MountOptions.includeGit` (also exposed on `launchOnMount`) opts the project's `.git` directory back into the mount with one-way project→mount sync. Git operations work inside the mount; mount-side `.git` mutations stay sandboxed and are discarded on cleanup. Fixes [#66](https://github.com/AgentWorkforce/relayfile/issues/66).

## [0.5.3] - 2026-04-24

_No user-visible changes in this release._

## [0.5.2] - 2026-04-24

_No user-visible changes in this release._

## [0.5.1] - 2026-04-23

_No user-visible changes in this release._

## [0.5.0] - 2026-04-21

### Added
- `launchOnMount({ shutdownSignal })`, `AutoSyncHandle.stop({ signal })`, `AutoSyncHandle.reconcile({ signal })`, and `syncBack({ signal })` now support cooperative shutdown cancellation so post-child finalize work can return a partial sync count while still running `onAfterSync` and cleanup.

## [0.4.0] - 2026-04-21

### Changed
- **BREAKING**: Renamed `AutoSyncOptions.writeFinishMs` → `AutoSyncOptions.debounceMs`. The semantics also shifted — it is now a per-path event coalescing debounce (default `50ms`), not a file-stability threshold (previously `200ms`). Any caller passing `writeFinishMs` will now be ignored silently under TypeScript's structural typing; update the field name.
- Replaced `chokidar` with [`@parcel/watcher`](https://www.npmjs.com/package/@parcel/watcher) for file watching. `autoSync.stop()` no longer hangs on teardown — native FSEvents/inotify/ReadDirectoryChangesW subscriptions unsubscribe promptly instead of draining per-file `awaitWriteFinish` polling timers.

### Fixed
- `startAutoSync` no longer leaks a subscription when one of the two `@parcel/watcher` subscribes rejects. If mount- or project-side setup fails, the successful side is now unsubscribed before the error surfaces.
- `AutoSyncHandle.stop()` now honors its "stopped means quiesced" contract. A `stopped` flag blocks new debounces from scheduling the moment `stop()` is called, and the `pendingDebounces` map is cleared *after* the watcher unsubscribes resolve. Previously, events delivered during the unsubscribe await could create timers that fired after `stop()` returned, running file ops against a mount `launchOnMount`'s `cleanup()` had already deleted.

## [0.3.0] - 2026-04-20

### Fixed
- Ignore mount-watcher echo writes so a mount→project propagation does not fire a spurious project→mount event. ([#52])

## [0.2.1] - 2026-04-20

### Fixed
- Preserve the local mount copy when the server denies a mount-sync write, instead of dropping it. ([#50])

## [0.2.0] - 2026-04-19

Initial release.

### Added
- `createSymlinkMount(projectDir, mountDir, options)` — copies files into a mount directory, honors `.agentignore` and `.agentreadonly` dotfiles, enforces mode `0o444` on readonly matches, and writes `_MOUNT_README.md` / `.relayfile-local-mount` markers. ([#47])
- `readAgentDotfiles(projectDir, options?)` — reads project-local `.agentignore`, `.agentreadonly`, and per-agent variants.
- `launchOnMount(options)` — creates a mount, spawns a CLI inside it, forwards `SIGINT` / `SIGTERM`, runs a final sync-back pass, and tears the mount down.
- `startAutoSync()` — bidirectional mount↔project sync with mount-wins conflict resolution, delete propagation, and a periodic full-reconcile safety net. ([#49])
- Directory-only ignore patterns (e.g. `cache/`) match directories without swallowing like-named files.
- README documenting the mount lifecycle, dotfile semantics, and auto-sync behavior. ([#48])

[Unreleased]: https://github.com/AgentWorkforce/relayfile/compare/v0.8.2...HEAD
[0.8.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.8.2
[0.8.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.8.1
[0.8.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.8.0
[0.7.40]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.40
[0.7.39]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.39
[0.7.38]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.38
[0.7.37]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.37
[0.7.36]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.36
[0.7.35]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.35
[0.7.34]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.34
[0.7.33]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.33
[0.7.32]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.32
[0.7.31]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.31
[0.7.30]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.30
[0.7.29]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.29
[0.7.28]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.28
[0.7.27]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.27
[0.7.26]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.26
[0.7.25]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.25
[0.7.24]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.24
[0.7.23]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.23
[0.7.22]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.22
[0.7.21]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.21
[0.7.20]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.20
[0.7.19]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.19
[0.7.18]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.18
[0.7.17]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.17
[0.7.16]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.16
[0.7.15]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.15
[0.7.14]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.14
[0.7.13]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.13
[0.7.12]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.12
[0.7.11]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.11
[0.7.10]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.10
[0.7.9]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.9
[0.7.8]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.8
[0.7.7]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.7
[0.7.6]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.6
[0.7.5]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.5
[0.7.4]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.4
[0.7.3]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.3
[0.7.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.2
[0.7.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.1
[0.7.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.7.0
[0.6.15]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.15
[0.6.14]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.14
[0.6.13]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.13
[0.6.11]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.11
[0.6.10]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.10
[0.6.9]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.9
[0.6.8]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.8
[0.6.7]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.7
[0.6.6]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.6
[0.6.5]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.5
[0.6.4]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.4
[0.6.3]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.3
[0.6.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.2
[0.6.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.1
[0.6.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.0
[0.5.3]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.3
[0.5.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.2
[0.5.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.1
[0.5.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.0
[0.4.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.4.0
[0.3.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.3.0
[0.2.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.2.1
[0.2.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.2.0
[#47]: https://github.com/AgentWorkforce/relayfile/pull/47
[#48]: https://github.com/AgentWorkforce/relayfile/pull/48
[#49]: https://github.com/AgentWorkforce/relayfile/pull/49
[#50]: https://github.com/AgentWorkforce/relayfile/pull/50
[#52]: https://github.com/AgentWorkforce/relayfile/pull/52
