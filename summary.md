# PR summary

## What changed

- Replaced the TypeScript path/TTL cache with a configurable 32 MiB byte-capped content-addressed LRU, and added equivalent sync/async Python caching. Cached reads send `If-None-Match` using `contentHash` and serve `304` responses locally; caching can be disabled.
- Added a verified, atomic, permission-restricted mount object store at `~/.relayfile/cache/objects/<sha256>`. Authorized bootstrap tree hashes are checked before body reads, so later mounts can materialize identical content without transferring it again.
- Reduced bootstrap and incremental point-read concurrency from 16 (previous environment ceiling 64) to a hard ceiling of 4.
- Changed SDK and Go transport retry delay calculation to full jitter while treating `Retry-After` as a minimum, including HTTP-date parsing already supported by each client.
- Made `relayfile listen` retry initial 429/503 handshakes, retain the last processed event cursor, and reconnect from that cursor with full jitter. Applied the same handshake `Retry-After`/jitter behavior to mount and FUSE WebSocket reconnects.
- Added overload `details.reason` propagation to Go `HTTPError`, updated SDK parity metadata, tests, and configuration/changelog documentation.

## Scope decisions

- Included `internal/mountfuse/wsinvalidate.go` because it is a shipped `/fs/ws` reconnect loop with the same fleet lockstep risk.
- Deferred SDK setup/control-plane retries, `packages/agents` reconnects, mount outbox writes, and CLI polite polling: they are one-shot, write-path, or polling flows rather than the file-read fan-in and event reconnect paths addressed here.
- No server handler or provider mutation path changed. The digest runtime contract therefore does not apply; filesystem-event emission and digest regeneration behavior are unchanged.

## Validation

- TypeScript SDK build and typecheck pass. All 113 relevant `client.test.ts` assertions pass; one pre-existing environment assertion expects Node to lack global `ErrorEvent`, which is false on the installed Node 25 runtime.
- Python SDK: 97 tests pass.
- Targeted Go cache/retry/WebSocket/listen tests pass for `internal/mountsync`, `internal/mountfuse`, and `cmd/relayfile-cli`.
- `scripts/check-contract-surface.sh` passes, including SDK parity.
- The complete TypeScript package suite additionally requires the downloaded Go toolchain to be on the subprocess `PATH`; unrelated launcher timing tests remain environment-sensitive.

## Contract note

The clients deliberately use the response body's `contentHash` for object identity. The currently documented server `ETag` is a revision identifier; the separate server change must redefine it to the quoted content hash and add `If-None-Match`/`304` handling before conditional requests can save network bodies against older servers.
