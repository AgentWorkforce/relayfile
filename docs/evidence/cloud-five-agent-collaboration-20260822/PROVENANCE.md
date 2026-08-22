# Relayfile Cloud five-agent provenance

Measured: 2026-08-22

Relayfile source base: `6c74cf8bb9eacfec17541c3ffb18ce4a534a3774`
plus the working changes merged through `AgentWorkforce/relayfile#435`

Relayfile Cloud source base: `a654709` plus the product-path changes retained
on branch `codex/cloud-realtime-parity`

Linux mount binary SHA-256:
`2beaa77658ed60491954b2ee8310d1c658e83ea3fd02349775e178dae9faaaec`

Cloud API deployment:

- worker: `relayfile-api-bench-20260822`;
- native-limiter API version: `e0d8a23c-ac00-421a-a7c6-9b6b8b8d5e9e`;
- final reviewed API version: `ca314797-4c34-4581-9858-1df6a37dcb76`;
- archive consumer: `relayfile-inline-archive-consumer-bench-20260822`;
- final reviewed archive consumer version:
  `a299f81b-738b-40c8-832e-8704f831f577`;
- workspace: `rw_7ccfea89`; and
- clock worker: `relayfile-clock-bench-20260822`.

Provider-neutral fleet:

| Role | Provider | Sandbox ID | Mounted remote prefix |
| --- | --- | --- | --- |
| a | E2B | `ivufwfdsl2b1c3dn5cl3k` | `/benchmark/mixed` |
| b | E2B | `i0u336h2onvz9zh1alnc8` | `/benchmark/mixed` |
| c | Daytona | `4d3482d3-9435-4c0e-a335-1c9b51e758ba` | `/benchmark/mixed` |
| d | Daytona | `b12946e5-9770-4826-b599-fda79a86330e` | `/benchmark/mixed` |
| e | Daytona | `2b678e12-ff59-4efd-8b6b-4c1b9d5058a2` | `/benchmark/mixed` |

The two providers ran the identical binary. The command adapter translated
only provider CLI invocation and home-directory paths; no transport, protocol,
or conflict behavior changed.

Retained evidence:

- `qualified/cloud-final-qualifier-2/` — 85 writers, 340 peer observations,
  aggregate summary;
- `qualified/cloud-direct-hot-read-conflict-1/` — five writers, five read-only
  inspections, aggregate summary;
- `qualified/mixed-e2b-daytona-qualifier-1/` — mixed-provider writer and peer
  observations; and
- `qualified/mixed-e2b-daytona-conflict-1/` — mixed-provider collision
  inspections;
- `qualified/cloud-native-rate-limit-qualifier-1/` — complete cold-start
  post-deployment run;
- `qualified/cloud-native-rate-limit-qualifier-2/` — complete warm
  post-deployment run; and
- `qualified/cloud-native-rate-limit-conflict-1/` — post-review conflict
  writers and inspections; and
- `qualified/cloud-final-reviewed-qualifier-1/` — exact reviewed deployment,
  with all product synchronization gates passing and only the external
  process-release spread gate failing.

Credential files, access tokens, and delegated secrets are intentionally not
retained in this evidence package.
