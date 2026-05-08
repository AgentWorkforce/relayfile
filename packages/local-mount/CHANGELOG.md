# Changelog

All notable changes to `@relayfile/local-mount` are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_No unreleased changes._

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

[Unreleased]: https://github.com/AgentWorkforce/relayfile/compare/v0.6.14...HEAD
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
