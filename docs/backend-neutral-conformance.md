# Backend-neutral runtime conformance

This suite verifies Relayfile at its public HTTP and WebSocket boundary. It does not import, mock, or inspect a Durable Object/actor implementation. The same scenarios therefore apply to the current hosted Cloudflare target, the OSS Go server, and a TerseAI/durable-actors port.

The executable entry point is `test/conformance/run.ts`. Target descriptors live in `test/conformance/targets/` and conform to `test/conformance/target.schema.json`.

## Profiles and commands

The `core` profile runs capabilities declared by a target and reports every unavailable case as a skip. The `full` profile turns any missing capability into a failure. An unknown target, missing environment variable, or unsupported capability is a configuration error; it never becomes a pass.

```bash
# Self-tests, legacy API conformance, and the durable-local Go oracle.
npm run test:conformance:local

# Public-edge coverage against the current hosted Cloudflare runtime.
npm run test:conformance:cloudflare

# All runtime and fault-injection gates against a controlled Cloudflare environment.
npm run test:conformance:cloudflare:full

# Full vendor qualification against a TerseAI port.
npm run test:conformance:terse
```

Set `RELAYFILE_CONFORMANCE_SEED` to a stable CI run identifier, or pass `--seed`. IDs, paths, delivery IDs, and correlation IDs are derived from that seed. Tests never depend on a model, random input, or fixed wall-clock sleeps. Asynchronous state is polled with bounded deadlines; time-driven behavior advances an adapter clock without sending unrelated traffic.

The local command starts the Go server with a durable-local backend, strong internal HMAC secret, ephemeral RS256 issuer, two isolated workspaces, and scoped tokens. Its data directory survives the suite's server restart and is removed afterward.

### PostgresSync applicability

`PostgresSync` is not a separate runtime contract or an implicitly qualified fourth target. Whether the name refers to the Postgres provider integration or to a Go deployment using `PostgresStateBackend` plus the Postgres envelope/writeback queues, Relayfile still exposes the same public HTTP/WebSocket contract, so every backend-neutral case ID applies unchanged.

The checked-in `go-local` target deliberately uses `durable-local`; this PR therefore does not claim that the conformance suite has qualified Postgres-backed sync. Existing Go integration tests exercise the Postgres state and queue implementations when `RELAYFILE_TEST_POSTGRES_DSN` is configured, while the provider catalog documents `postgres` as path-mapping/writeback-only with no shipped sync definition or automatic backfill. Those checks do not replace this public-boundary suite. End-to-end PostgresSync qualification is deferred until CI provisions a disposable Postgres instance and runs the same core profile through a dedicated target; controlled retry, crash, migration, failover, and mount claims additionally require the full-profile adapter controls documented below.

## Hosted target environment

The Cloudflare and Terse descriptors read secrets only from the environment:

| Variable | Purpose |
| --- | --- |
| `RELAYFILE_BASE_URL` | Public Relayfile HTTP origin; WebSocket origin is derived from it. |
| `RELAYFILE_WORKSPACE_PRIMARY` | Disposable primary qualification workspace. |
| `RELAYFILE_WORKSPACE_SECONDARY` | Disposable tenant-isolation workspace. |
| `RELAYFILE_TOKEN_PRIMARY` | Full-scope token for the primary workspace. |
| `RELAYFILE_TOKEN_SECONDARY` | Full-scope token for the secondary workspace. |
| `RELAYFILE_TOKEN_PATH_SCOPED` | Primary-workspace token restricted to `/conformance/scoped/**`. |
| `RELAYFILE_TOKEN_READ_ONLY` | Primary-workspace token with `fs:read` and no write scope. |
| `RELAYFILE_RUNTIME_VERSION` | Deployed runtime version or immutable runtime SHA. |
| `RELAYFILE_SHA` | Relayfile application SHA under test. |
| `RELAYFILE_CONFORMANCE_CONTROL_URL` | Full-profile control adapter origin. |
| `RELAYFILE_CONFORMANCE_CONTROL_TOKEN` | Bearer token for that adapter. |
| `RELAYFILE_CONFORMANCE_DISPOSABLE` | Must be exactly `1` for a remote full-profile run; acknowledges that both workspaces are dedicated and disposable. |
| `RELAYFILE_TERSE_SHARED_SECRET` | Non-empty Terse runtime shared secret; target loading fails closed when absent. |
| `RELAYFILE_TERSE_RUNTIME_URL` | Terse control-plane/runtime origin used for runtime-native verification. |
| `RELAYFILE_TERSE_ADMIN_KEY` | Terse administrative bearer key; redacted from every evidence artifact. |
| `RELAYFILE_TERSE_PROJECT_ID` | Terse project containing the Relayfile workspace actor. |
| `RELAYFILE_TERSE_ACTOR_NAME` | Published Terse actor class name for the Relayfile workspace runtime. |
| `RELAYFILE_TERSE_ACTOR_ID` | Actor ID derived from the verified workspace claim; defaults to the primary workspace ID. |

Use dedicated disposable workspaces. A remote full run fails closed unless `RELAYFILE_CONFORMANCE_DISPOSABLE=1`; `reset` is invoked before its scenarios. Source fixtures are namespaced under `/conformance/<seed>/` (or `/conformance/scoped/<seed>/` for the path-token check), but product-defined digest paths (`/digests/today.md` and `/digests/yesterday.md`) are intentionally fixed and may be regenerated or rolled over. `RF-PROJ-001` also writes a sentinel directly to `/digests/today.md` and proves a later clock tick leaves it unchanged while emitting exactly the sentinel write's one event. This destructive fixed-path check is why full runs require a disposable workspace.

## Narrow control adapter contract

The adapter exposes only deterministic test controls that cannot be expressed at the public edge. It is not a second Relayfile API. Keep it unreachable from production traffic and require `RELAYFILE_CONFORMANCE_CONTROL_TOKEN`.

Each operation is:

```text
POST <control-origin>/v1/conformance/<operation>
Authorization: Bearer <control-token>
Content-Type: application/json
X-Correlation-Id: <seed>-control-<sequence>

{"workspaceId":"...","seed":"...", ...operation fields}
```

A successful operation returns JSON and a 2xx status. A non-2xx response fails the active invariant and is retained in redacted evidence.

| Operation | Required behavior and response |
| --- | --- |
| `reset` | Clear the dedicated qualification workspace, including fixed digest artifacts, plus adapter state for `workspaceId` + `seed`; return `{}`. A failure is recorded as `RF-SETUP-001` with evidence and stops the run. |
| `provider.configure` | Configure/release deterministic provider faults: finite `ingestFailures`, `permanentIngestFailurePath`, `ingestBackpressure`, `echoWritebackWebhook`, `holdIngestPath`/`releaseIngestPath`, `crashBarrier:{matchPath,phase}` (`before-commit` or `after-commit`), or `seedMigrationState`. The adapter must use `seedMigrationState.deadLetterId` verbatim as the exported manifest ID and the envelope ID accepted by `/sync/dead-letter/{envelopeId}/replay`; remapping it is a contract failure. Return the applied configuration. |
| `provider.calls` | For an optional `matchPath`, return applicable `attempts`, retry `state` (`retrying` while a poison item still has budget), `writebackAttempts`, `echoDeliveries`, `held`, `crashReady`, and `crashPhase` fields. Counts and barrier state must survive until the requested crash/restart. |
| `state.inspect` | Read durable backing state directly without routing to or waking the actor. For requested `paths`, identities, operations, or delivery IDs, return `files`, `eventCounts`, `identityActive`, `backpressureActive`, `operations`, `deadLetters`, and `servingRuntime` as applicable. Returned operations are restricted to requested paths/IDs. On Terse, the harness independently fences the actor's unfiltered runtime trace before and after this read, so any actor invocation fails the no-wake proof regardless of its request ID. This is the proof point immediately after clock advance. |
| `clock.advance` | Advance the target's injected UTC clock by `milliseconds`, run all due work, and return only after the runtime is quiescent. It must not synthesize public traffic. |
| `runtime.evict` | Evict the workspace actor/DO without deleting durable state, then return when routing can create a new instance. |
| `runtime.crash` | After `provider.calls` exposes the configured `crashReady` barrier, accept the matching `{matchPath,phase}`, terminate the active instance without a graceful disconnect, and return `{terminated:true}`. The paused public write must lose its response. `before-commit` leaves no file/event/op; `after-commit` preserves all three exactly once. |
| `runtime.restart` | Restart the runtime/gateway while retaining durable state; return when `/health` is ready. |
| `runtime.failover` | Three-phase barrier: `{phase:"begin",pause:true}` starts switching and returns `{switchId,state:"fenced"}` only after the old writer is fenced; `{phase:"await-writers",switchId,count:2}` waits until both public writes are held and returns `{pendingWriters}`; `{phase:"release",switchId}` releases them and completes switching. |
| `state.export` | Return `{artifact, manifest}`. The manifest includes `pendingOutboxIds` and `deadLetterIds`; the artifact also preserves files, revisions, event cursors, idempotency records, and retry metadata. |
| `state.import` | Import `{artifact,destinationRuntime}`, switch subsequent public requests to the imported state, and return the imported `manifest`. |
| `mount.start` | Start the real mount client with `mode` and optional `resetAfterClobber`; return `{id,root,maxReconnectDelayMs}`. The root is local to the harness host. |
| `mount.stop` | Stop the mount identified by `id` and wait for teardown. |

Clock control is mandatory for retries, DLQ exhaustion/replay, idempotency expiry, and digest day rollover. This catches actor ports that appear healthy but only make progress when another request arrives. Runtime eviction/crash controls distinguish durable cursor/state recovery from socket continuity.

## Runtime-native Terse verification

Terse checks do not trust the control adapter to report whether it woke or invoked the actor. Against the API pinned at [`19af4b48`](https://github.com/TerseAI/durable-actors/blob/19af4b48e6f148b6edd11ee243c79c30c17dc30e/docs/reference/openapi.yaml), the harness captures the actor-filtered `GET /v1/projects/{project}/observe/requests` epoch, cursor, and highest retained sequence before every adapter `state.inspect`, reads `GET /v1/projects/{project}/observe/state`, then reads the unfiltered actor trace again. The epoch and cursor must be unchanged, no record may appear above the captured sequence, and both pages must report `dropped: 0`, `evicted: 0`, `persistenceFailed: false`, and `reset: false`; otherwise the inspection cannot prove no-wake behavior. This trace fence uses no wall-clock window or request-ID convention.

`RF-AUTH-005` posts directly to `/v1/projects/{project}/actors/{actorName}/{actorId}/invoke` once without a bearer and once with an invalid bearer. Both requests must return 401 or 403, and the same runtime-native trace query must show that neither reached actor application code. The old adapter `auth.probe` self-report is not part of the contract. Cloudflare has no reachable equivalent runtime-native observer, so cases backed by its control adapter are labeled `adapter-attested` in `summary.json` and the GitHub step summary; Terse-native proofs are labeled `runtime-native`.

## Terse porting boundary

This repository has read access to the existing Relayfile Durable Object implementation in the private cloud repository at `packages/relayfile/src/durable-objects`, but that implementation is not present here and is not ported by this conformance PR. The adapter/port is tracked in [relayfile-cloud#275](https://github.com/AgentWorkforce/relayfile-cloud/issues/275).

At the pinned durable-actors revision, no actor alarm API is available. The upstream alarm stack [#147](https://github.com/TerseAI/durable-actors/pull/147)–[#151](https://github.com/TerseAI/durable-actors/pull/151), including [#150](https://github.com/TerseAI/durable-actors/pull/150), closed without merge; [#111](https://github.com/TerseAI/durable-actors/pull/111) remains open, and [v0.7.9](https://github.com/TerseAI/durable-actors/releases/tag/v0.7.9) does not expose the required alarm surface. Consequently `RF-QUEUE-*`, `RF-PROJ-*`, and `RF-TIMER-*` are expected to fail on Terse until upstream alarms ship or a durable external scheduler is built. Qualification also requires a replica-switching writer fence and effect batching below Terse's 512-effect/24 MiB boundary. These are implementation prerequisites, not exclusions from the full profile.

## Invariants and evidence

Case IDs are stable and appear in console output, `summary.json`, and JUnit:

- `RF-AUTH` / `RF-MODE`: authentication, tenant/path isolation, traversal resistance, and read-only enforcement.
- `RF-ING`: delivery deduplication, out-of-order convergence, and independent concurrency.
- `RF-QUEUE`: bounded retry without traffic, backpressure, DLQ/replay, and poison-record isolation.
- `RF-PROJ` / `RF-TIMER`: source and digest projection, terminal/delete semantics, echo suppression, rollover, and TTL expiry.
- `RF-CAS` / `RF-SER`: conflict-safe writeback, receipt replay, serialized same-object mutation, and cross-object progress.
- `RF-WS` / `RF-DUR` / `RF-LIMIT`: exclusive cursors, eviction/reconnect, restart durability, durable operations, and the Terse 512-effect boundary.
- `RF-CRASH` / `RF-MIG` / `RF-FAILOVER`: commit atomicity, state portability, and single-writer fencing.
- `RF-MOUNT`: read/write/mirror transitions, capped reconnect, eviction recovery, reset-after-clobber, and conflict artifacts.
- `RF-OAS`: repository contract-surface check plus runtime OpenAPI status/body validation.

Every public response is matched to its OpenAPI operation and status, then its JSON body is validated against `openapi/relayfile-v1.openapi.yaml`. Failures name the invariant and carry target ID, public base URL, runtime kind/version, Relayfile SHA, timings, correlation IDs, and redacted request/response pairs.

Artifacts are written beneath `artifacts/conformance/<target>/<seed>/`:

- `summary.json`: target metadata and per-case result.
- `requests.jsonl`: public and control exchanges with timings.
- `junit.xml`: CI-native case results.

Authorization, cookies, tokens, secrets, API/workspace keys, and known secret values are recursively redacted. `test/conformance/harness.test.ts` asserts that evidence cannot contain configured credentials. CI uploads evidence even when a gate fails.

## TerseAI implementation notes

The qualification pin used while designing this contract is `TerseAI/durable-actors@19af4b48e6f148b6edd11ee243c79c30c17dc30e` (`v0.7.9`). The port needs a public HTTP gateway because this Terse release exposes RPC/WebSocket actors rather than a `fetch` handler. That gateway owns status/error mapping, correlation IDs, JWT workspace verification, and actor-ID derivation from the verified workspace claim.

Do not mark a handler reentrant unless revision compare-and-swap remains atomic. A `202` write acknowledgement must atomically preserve file state, its event, and the pending writeback operation even though Terse commits persisted state only after a method succeeds. Mount recovery must use durable event cursors because sockets and disconnect callbacks do not survive every restart. Time-based behavior needs an external durable scheduler because this Terse release has no alarm API.

The Terse descriptor refuses to load without `RELAYFILE_TERSE_SHARED_SECRET`. The gateway must also fail closed when its actor shared secret is empty; a request path must never select an actor independently of the verified workspace claim. `RF-AUTH-005` is registered for every target so reports have the same stable case-ID set. It runs on Terse and is recorded as an explicit not-applicable skip, with the runtime kind in the reason, elsewhere because Cloudflare Durable Objects and the Go oracle have no equivalent shared-secret hop.

## tester-army/e2e decision

`tester-army/e2e@f7c075666672d128f79ef2ec347fda67ea9e42ce` was evaluated and is intentionally not a conformance dependency. It is a model-driven browser/mobile recorder, does not improve these API/runtime invariants, is pre-1.0, and enables telemetry by default. The deterministic gate sets `E2E_TELEMETRY_DISABLED=1` regardless. A future dashboard-only smoke may pin this exact revision and must keep telemetry disabled, but it cannot replace any invariant above.
