# Changelog

All notable changes to `@relayfile/sdk` are documented here.
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

## [0.3.2] - 2026-04-21

### Added
- `relayfile.fork` client method — fork a workspace at a specific commit. ([#58])

## [0.3.1] - 2026-04-21

### Added
- Expose `ContentIdentity` on write types so callers can opt into server-side deduplication. ([#57])

## [0.1.8] - 2026-04-16

### Fixed
- Bind `fetch` to `globalThis` so the SDK runs on Cloudflare Workers without `TypeError: Illegal invocation`. ([#41])

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
[0.3.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.3.2
[0.3.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.3.1
[0.1.8]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.8
[#41]: https://github.com/AgentWorkforce/relayfile/pull/41
[#57]: https://github.com/AgentWorkforce/relayfile/pull/57
[#58]: https://github.com/AgentWorkforce/relayfile/pull/58
