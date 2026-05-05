# Changelog

All notable changes to `relayfile` (CLI) are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_No unreleased changes._

## [0.6.3] - 2026-05-05

_No user-visible changes in this release._

## [0.6.2] - 2026-05-05

_No user-visible changes in this release._

## [0.6.1] - 2026-05-01

_No user-visible changes in this release._

## [0.6.0] - 2026-04-30

_No user-visible changes in this release._

## [0.5.3] - 2026-04-24

_No user-visible changes in this release._

## [0.5.2] - 2026-04-24

_No user-visible changes in this release._

## [0.5.1] - 2026-04-23

_No user-visible changes in this release._

## [0.5.0] - 2026-04-21

_No user-visible changes in this release._

## [0.4.0] - 2026-04-21

_No user-visible changes in this release._

## [0.2.2] - 2026-04-20

### Fixed
- Publish workflow now attaches `relayfile-mount` binaries to GitHub releases alongside `relayfile`. ([#51])

## [0.2.0] - 2026-04-19

### Added
- Ship the `@relayfile/local-mount` package via the monorepo publish workflow. ([#47])

## [0.1.13] - 2026-04-17

### Added
- `relayfile observer` — launch the file-observer dashboard from the CLI. ([#46])

### Fixed
- Default the observer to the live router path.
- Reduce WebSocket polling overhead in mount mode. ([#45])

## [0.1.12] - 2026-04-17

### Fixed
- Default to the hosted relayfile API so first-run no longer requires `--host`. ([#44])
- Speed up mount bootstrap by seeding from an export snapshot. ([#44])

## [0.1.11] - 2026-04-17

### Added
- Support a default workspace selection so repeat invocations skip the picker. ([#43])

## [0.1.10] - 2026-04-17

### Fixed
- Package native Go binaries into the npm distribution. ([#42])

## [0.1.7] - 2026-04-11

### Fixed
- Upload raw binaries to GitHub releases. ([#34])

[Unreleased]: https://github.com/AgentWorkforce/relayfile/compare/v0.6.3...HEAD
[0.6.3]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.3
[0.6.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.2
[0.6.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.1
[0.6.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.6.0
[0.5.3]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.3
[0.5.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.2
[0.5.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.1
[0.5.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.5.0
[0.4.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.4.0
[0.2.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.2.2
[0.2.0]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.2.0
[0.1.13]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.13
[0.1.12]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.12
[0.1.11]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.11
[0.1.10]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.10
[0.1.7]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.7
[#34]: https://github.com/AgentWorkforce/relayfile/pull/34
[#42]: https://github.com/AgentWorkforce/relayfile/pull/42
[#43]: https://github.com/AgentWorkforce/relayfile/pull/43
[#44]: https://github.com/AgentWorkforce/relayfile/pull/44
[#45]: https://github.com/AgentWorkforce/relayfile/pull/45
[#46]: https://github.com/AgentWorkforce/relayfile/pull/46
[#47]: https://github.com/AgentWorkforce/relayfile/pull/47
[#51]: https://github.com/AgentWorkforce/relayfile/pull/51
