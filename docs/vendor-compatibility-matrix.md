# Relayfile runtime compatibility matrix

This matrix is a qualification plan, not a claim that unavailable vendor credentials or fault controls were executed. Run details and immutable versions belong in the generated evidence. The reference application SHA used to design the suite was `aa95991113ec78a6c2ee04460ba401d0c0853640`.

Status legend: **CI** runs in this repository; **configured** has a checked-in target descriptor but needs environment credentials; **adapter** requires the documented control adapter; **risk** is a known semantic gap that the invariant is designed to detect.

| Contract area | Invariant IDs | Go durable-local oracle | Hosted Cloudflare DO | TerseAI durable-actors v0.7.9 |
| --- | --- | --- | --- | --- |
| Public auth and tenant/path isolation | `RF-AUTH-001`–`RF-AUTH-005`, `RF-MODE-001` | public cases CI; `RF-AUTH-005` explicit N/A | public cases configured, black-box; `RF-AUTH-005` explicit N/A | configured; gateway must derive actor ID from verified claim; `RF-AUTH-005` verifies the shared-secret hop fails closed |
| Webhook idempotency and ordering | `RF-ING-*` | CI | configured, black-box | configured; adapter provider stub for full fault cases |
| Retry, backpressure, DLQ/replay, poison isolation | `RF-QUEUE-*` | API baseline; time/fault cases skip explicitly | adapter | **expected FAIL** until actor alarms ship or an external durable scheduler exists |
| File/digest projection and echo suppression | `RF-PROJ-*`, `RF-TIMER-*` | `RF-ING-*` cover ingest projection; digest/echo/timer cases skip explicitly | adapter | **expected FAIL** until actor alarms ship or an external durable scheduler exists |
| Optimistic concurrency and replay-safe receipts | `RF-CAS-*`, `RF-SER-*` | CI for 20-way CAS and content identity | configured; held-provider case needs adapter | risk: `@Reentrant` methods can race shared state |
| Read/write/mirror mount transitions | `RF-MODE-001`, `RF-MOUNT-001` | read-only API CI; lifecycle case skips explicitly | adapter on a mount-capable runner | adapter on a mount-capable runner |
| Cursor reconnect, restart, and large effect batches | `RF-WS-*`, `RF-DUR-001`, `RF-LIMIT-001` | cursor and process-restart CI | eviction adapter for full profile | risk: sockets do not survive eviction; 512-effect/24 MiB invocation cap |
| Crash atomicity | `RF-CRASH-001` | skip unless a controlled target supplies the hook | adapter | risk: commit-on-success/rollback-on-throw differs from Cloudflare per-write durability |
| Migration and failover fencing | `RF-MIG-001`, `RF-FAILOVER-001` | skip unless a controlled target supplies the hook | adapter/export source | adapter/import destination; replica switching fence must be implemented |
| OpenAPI and error envelopes | `RF-OAS-*` | CI, every harness response validated | configured, public gateway only | configured, public gateway only; actor-internal errors are not the contract |
| Evidence and redaction | harness self-tests | CI | generated per run | generated per run |

The plain `cloudflare-hosted` target intentionally declares only observable public capabilities, so it can qualify the current hosted edge without private runtime hooks. `cloudflare-controlled` declares every backend-neutral capability that applies to Cloudflare and fails unless all corresponding adapter controls are present. Every target registers `RF-AUTH-005`; non-Terse runtimes record it as not applicable rather than silently omitting it. The `runtime-auth-probe` behavior itself applies only to Terse's shared-secret actor hop. The `npm run test:conformance:terse` command selects the full profile; missing scheduler, lifecycle, migration, failover, or mount behavior is then a failure rather than an optimistic pass.

## Terse implementation gap

The checked-in Terse target is a qualification contract, not an implemented or qualified adapter. At pinned durable-actors revision [`19af4b48`](https://github.com/TerseAI/durable-actors/tree/19af4b48e6f148b6edd11ee243c79c30c17dc30e), `actor.ts` has no `setAlarm`, `getAlarm`, or `deleteAlarm` surface. The stacked changes [#147](https://github.com/TerseAI/durable-actors/pull/147) through [#151](https://github.com/TerseAI/durable-actors/pull/151), including the one-off alarm change [#150](https://github.com/TerseAI/durable-actors/pull/150), were closed without merge; durable cron [#111](https://github.com/TerseAI/durable-actors/pull/111) remains open. The [v0.7.9 release](https://github.com/TerseAI/durable-actors/releases/tag/v0.7.9) therefore does not supply the alarm API this port needs.

A production Terse target requires all of the following before qualification:

- durable alarms or a separately durable external scheduler for retry/DLQ progression, idempotency expiry, and digest rollover;
- a scheduler integration that advances work without an inbound actor request;
- a single-writer fence during replica switching;
- effect batching/chunking that respects the 512-effect and 24 MiB invocation limits;
- a port of the Relayfile Durable Object runtime, which currently lives in the private cloud repository at `packages/relayfile/src/durable-objects`, not this OSS repository.

The PR author verified read access to that private runtime while scoping this gap. Implementation is tracked separately in [relayfile-cloud#275](https://github.com/AgentWorkforce/relayfile-cloud/issues/275) and needs an assigned owner plus the scheduler architecture decision. Until it lands, `RF-QUEUE-*`, `RF-PROJ-*`, and `RF-TIMER-*` are expected to fail on Terse rather than being treated as compatibility claims.

See [backend-neutral-conformance.md](backend-neutral-conformance.md) for commands, environment variables, exact adapter behavior, and evidence format.
