# Changelog

All notable changes to `@relayfile/sdk` are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_No unreleased changes._

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

_No user-visible changes in this release._

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

_No user-visible changes in this release._

## [0.7.17] - 2026-05-14

_No user-visible changes in this release._

## [0.7.16] - 2026-05-14

_No user-visible changes in this release._

## [0.7.15] - 2026-05-14

_No user-visible changes in this release._

## [0.7.14] - 2026-05-14

### Added
- Add workspace primitive contract types for digest handlers, layout manifests, writeback schema references, and dead-letter writeback errors.
- Extend `WritebackItem` with optional list/detail fields used by writeback queue, CLI list rows, and dead-letter JSON output, with no behavioral change in `RelayFileClient`.

## [0.7.12] - 2026-05-13

### Added
- Document the proactive runtime relayfile contract in `docs/proactive-runtime-contract.md`, including the gateway DLQ path scheme `/_dlq/<event-id>.json`, the `subscribe(globs, onChange)` and `getResourceAtEvent(eventId)` contracts, the canonical `EventSummary` adapter shape, and the reserved change-stream transport bootstrap types.
- Ship the M2 proactive-runtime change surfaces on `RelayFileClient`: `subscribe(globs, onChange, options?)`, `open(options)`, `getResourceAtEvent(eventId)`, `listChangesSince(isoTimestamp)`, and `listLastNChanges(limit)` now use the shared WebSocket transport, retained change-log lookups, and event expansion hooks.
- Add configurable local per-workspace retained-change cache settings via `new RelayFileClient({ changeLog: { retentionMs, maxEntries } })` and preserve hydrated resource payloads when retained replay refreshes an already-cached event.

## [0.7.11] - 2026-05-12

### Added
- Document the proactive runtime relayfile contract in `docs/proactive-runtime-contract.md`, including the gateway DLQ path scheme `/_dlq/<event-id>.json`, the `subscribe(globs, onChange)` and `getResourceAtEvent(eventId)` contracts, the canonical `EventSummary` adapter shape, and the reserved change-stream transport bootstrap types.
- Ship the M2 proactive-runtime change surfaces on `RelayFileClient`: `subscribe(globs, onChange, options?)`, `open(options)`, `getResourceAtEvent(eventId)`, `listChangesSince(isoTimestamp)`, and `listLastNChanges(limit)` now use the shared WebSocket transport, retained change-log lookups, and event expansion hooks.
- Add configurable local per-workspace retained-change cache settings via `new RelayFileClient({ changeLog: { retentionMs, maxEntries } })` and preserve hydrated resource payloads when retained replay refreshes an already-cached event.

## [0.7.10] - 2026-05-12

_No user-visible changes in this release._

## [0.7.9] - 2026-05-12

_No user-visible changes in this release._

## [0.7.8] - 2026-05-11

_No user-visible changes in this release._

## [0.7.7] - 2026-05-11

_No user-visible changes in this release._

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

_No user-visible changes in this release._

## [0.6.15] - 2026-05-08

_No user-visible changes in this release._

## [0.6.14] - 2026-05-08

_No user-visible changes in this release._

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

[Unreleased]: https://github.com/AgentWorkforce/relayfile/compare/v0.7.35...HEAD
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
[0.3.2]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.3.2
[0.3.1]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.3.1
[0.1.8]: https://github.com/AgentWorkforce/relayfile/releases/tag/v0.1.8
[#41]: https://github.com/AgentWorkforce/relayfile/pull/41
[#57]: https://github.com/AgentWorkforce/relayfile/pull/57
[#58]: https://github.com/AgentWorkforce/relayfile/pull/58
