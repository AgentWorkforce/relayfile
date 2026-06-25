# Relayfile SDK Parity

`packages/sdk/parity.json` is the source of truth for the public TypeScript
and Python SDK parity contract. Each capability is classified as:

- `both`: portable SDK surface that must be exported by TypeScript and Python.
- `ts-only`: intentionally TypeScript-only, usually because it depends on Node
  process, filesystem, native mount binary, or browser-login callback behavior.
- `py-only`: intentionally Python-only.
- `planned`: accepted parity gap that should not silently become a documented
  shared surface until the implementation exists.

Run the drift guard with:

```bash
node scripts/check-sdk-parity.mjs
```

The existing contract check (`scripts/check-contract-surface.sh`) also runs
this guard. Release workflows for the TypeScript and Python SDKs run the same
check before publishing.

The guard is bidirectional for the TypeScript default entrypoint: it validates
that every symbol exported from `packages/sdk/typescript/src/index.ts`,
including local `export *` re-exports, appears in `parity.json`. A PR that adds
a new default-entrypoint export must classify it in the parity contract.

Additional integrity checks:

- An empty/truncated `parity.json` (no `capabilities`) fails fast instead of
  passing a no-op gate.
- Every `both` capability's `tsExports` must be reachable from the default
  entrypoint, so removing a shared symbol from `index.ts` while leaving its
  source declaration intact is reported before publishing. (`ts-only` CLI
  surface such as the mount launcher and workspace seeder lives on CLI-only
  subpaths and is validated against the source tree rather than the default
  entrypoint.)

## Current Contract

| Capability | Status | Notes |
| --- | --- | --- |
| Data-plane client | `both` | Filesystem, bulk write, query, events, sync/writeback operation, webhook ingest, and admin HTTP client. |
| `on_write` subscriptions | `both` | Python uses snake_case names (`on_write`, `path_matches`). |
| Provider helpers | `both` | Path helpers, digest helpers, webhook inputs, provider base class. |
| Setup/control plane | `both` | Python now exposes `RelayfileSetup`/`WorkspaceHandle` for create/join, connect/adopt, readiness waits, metadata/resource helpers, and peer-agent invites. |
| Setup errors | `both` | Python mirrors the portable cloud/setup error classes. |
| Connection provider contract | `both` | Python uses snake_case helper names but preserves wire payload keys. |
| Self-host connect | `both` | Portable connect-session helper. |
| Writeback consumer | `both` | Polls pending writebacks and acknowledges success/failure. |
| Integration adapter | `both` | Abstract provider adapter hooks and result shapes. |
| Client read cache | `planned` | TypeScript has `RelayFileReadCacheOptions`; Python implementation remains a planned parity item. |
| Sync socket manager | `ts-only` | Python subscription flows use `on_write` today. |
| Interactive cloud login | `ts-only` | Depends on a local callback server in the TypeScript CLI entrypoint. |
| Workspace seeder | `ts-only` | Depends on Node filesystem/tar helpers. |
| Mount launcher | `ts-only` | Starts and probes the native `relayfile-mount` daemon. Python exports placeholders that raise `ts_only_sdk_feature`. |

## Release Policy

Shared SDK surface changes must update `packages/sdk/parity.json` in the same
PR as the implementation. Mark a capability `both` only when both packages
export the documented surface and tests cover the added behavior.

When a shared capability changes, release the TypeScript and Python SDKs in the
same release window and use matching semantic intent even if package versions
remain numerically independent. `publish.yml` and `publish-python.yml` both run
the parity guard so one package cannot publish a documented shared surface that
the other package lacks.
