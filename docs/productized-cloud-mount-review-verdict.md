# Productized Cloud Mount — Final Review Verdict

**Date:** 2026-05-02
**Reviewer scope:** `docs/productized-cloud-mount-contract.md` (v1 contract),
`docs/evidence/productized-cloud-mount-final-evidence.md`, the changed
relayfile files (`cmd/relayfile-cli/main.go`,
`cmd/relayfile-cli/productized_cloud_mount_e2e_test.go`,
`cmd/relayfile-cli/daemon_unix.go`/`daemon_windows.go`,
`cmd/relayfile-cli/main_test.go`, `internal/mountsync/syncer.go`,
`internal/mountsync/syncer_test.go`, `cmd/relayfile-mount/main.go`),
the cloud-side test slice referenced in the evidence, and the
verification output (Go, SDK typecheck, cloud test+typecheck) recorded
with `RELAYFILE_PRODUCTIZED_MOUNT_VERIFY_OK` and
`CLOUD_PRODUCTIZED_MOUNT_VERIFY_OK`.

**Verdict:** **Conditional pass.** The product vision (synced-mirror-first
guided setup with honest sync state, token rejoin, and conflict
visibility) is implemented and end-to-end provable. Several normative
contract surfaces (`pull`, `permissions`, `ops list/replay`, schema
validation artifacts, webhook-unhealthy status rendering, catalog
caching/409 revalidation, refresh-token-expiry degraded mode, A2/A6/A7/
A9/A11–A14/A16 acceptance tests) are still missing or only partially
covered. None of the gaps are blockers for an internal/early-access
launch, but they MUST be resolved before a public v1 launch.

---

## 1. Does the implementation satisfy the original product vision?

**Mostly yes.**

What landed and matches the contract:

- `relayfile` with no args dispatches to `runSetup`
  (`cmd/relayfile-cli/main.go:304-311`), and `setup` accepts the
  documented flags (`--cloud-api-url`, `--cloud-token`, `--workspace`,
  `--provider`, `--local-dir`, `--no-open`, `--skip-mount`, `--once`,
  `--login-timeout`, `--connect-timeout`) at `main.go:385-414`.
- The required ordered steps (intent print → cloud login → workspace
  name → provider → local dir → workspace create+join → integration
  connect → initial sync gate → mount) are all present
  (`runSetup`, `ensureCloudCredentials`, `ensureWorkspaceForSetup`,
  `joinWorkspaceViaCloud`, `connectCloudIntegration`,
  `waitForInitialSync`, `runMount`).
- Idempotency on re-run: `ensureWorkspaceForSetup` reuses the locally
  tracked id; `ensureCloudIntegration` skips connect when status
  already returns ready; the join is re-issued each setup (refreshing
  the VFS token). `setup` is therefore idempotent per §1.4.
- Multi-integration commands exist (`relayfile integration connect | list
  | disconnect`, `main.go:1075-1336`) and the mock E2E test exercises
  `connect notion` after a `setup --provider github` and confirms both
  appear in `integration list --json`.
- Synced-mirror-first banner (`runMountLoop` at `main.go:3579`) prints
  `Mirror started at <dir>. Sync interval <d> ±<n>%. Type 'relayfile
  status' for live state.` matching §3.1.
- `.relay/state.json`, `.relay/conflicts/`, `.relay/dead-letter/`, and
  `.relay/permissions-denied.log` directory layout is created by the
  syncer (`internal/mountsync/syncer.go:520-562`).
- Background daemon: `relayfile mount --background` spawns a detached
  child via `spawnBackgroundMountProcess` with `Setsid`
  (`daemon_unix.go`), writes `mount.pid` and `mount.log`, and `relayfile
  stop` SIGTERMs it (`main.go:1915-1939`). `relayfile logs` tails the
  log file. Per-platform detach is implemented in
  `daemon_unix.go`/`daemon_windows.go`.
- Cloud token refresh at 60s pre-expiry (`cloudAccessTokenExpiredSoon`,
  `main.go:600-608`); refresh `403 invalid_grant` is mapped to the
  documented "cloud session expired. Run 'relayfile login' to sign in
  again." (`refreshCloudCredentials`, `main.go:611-643`).
- Workspace rejoin at 10% of JWT lifetime / 5 min floor
  (`relayfileTokenNeedsRefresh`, `main.go:3162-3184`) and on
  401/403 (`isMountAuthError` + `withAuthRefresh`,
  `main.go:3205-3211`/`3526-3542`).

What is missing or only partially landed against the contract:

- **`relayfile pull [PATH]`** (§3.5) — not implemented. There is no
  `pull` subcommand in `run`'s dispatch table.
- **`relayfile permissions [PATH]`** (§8.1) — not implemented.
- **`relayfile ops list` / `relayfile ops replay <opId>`** (§8.4) —
  not implemented; the dead-letter directory is created but never
  written to or surfaced.
- **Schema validation surfacing** (§8.2): the syncer never recognizes
  `400 schema_validation_failed`, never moves the offending file to
  `.relay/conflicts/<path>.invalid.<ts>`, and never re-fetches the
  prior remote into the original path.
- **Webhook unhealthy warning row** (§7.4 / acceptance A11): the
  `webhookHealthy` field is plumbed into `syncProviderStatus`
  (`main.go:235`) but `runStatus` text output (`main.go:1888-1902`)
  never renders the warning. The JSON path passes the field through.
- **Catalog cache + 409 revalidation** (§6.1): `loadIntegrationCatalog`
  fetches `/api/v1/integrations/catalog` per call with no 1-hour cache
  and no special handling of `409 unknown_provider`.
- **Per-minute degraded read-only stderr cadence** (§5.4 / A9): when
  the refresh token itself expires the mount keeps running on the
  in-memory token and logs once per failed refresh, but there is no
  explicit "read-only degraded" mode with `cloud_session_expired`
  reason in `.relay/permissions-denied.log` and no rate-limited
  recovery message every minute.

Summary: the product **shape** is correct and a user can run
`relayfile setup` against a real Cloud and end up in the documented
state. The product **surface area** the contract promises (pull,
permissions, ops, schema-aware conflicts, webhook-aware status) is
not all there yet.

---

## 2. Are synced-mirror drawbacks mitigated and visible?

**Largely yes, with two visibility gaps.**

Working as advertised:

- The mount loop prints the synced-mirror banner with the cycle
  interval and jitter on every start (`main.go:3579`). `--mode=poll`
  is the default; FUSE remains opt-in.
- `.relay/state.json` is rewritten by `writeSnapshot` after every
  cycle, every local change, and on graceful exit. The schema in the
  snapshot (`syncStateFile` at `main.go:240-260`, `daemonPIDState` at
  `:275`) matches §3.2 (mode, lastReconcileAt, intervalMs, providers,
  pendingWriteback, pendingConflicts, deniedPaths, daemon block).
- `relayfile status` (`runStatus`, `main.go:1838-1912`) shows mode,
  per-provider status/lag/last-event/last-error, daemon running/not
  running, and pending writebacks/conflicts/denied counts. The E2E
  test asserts the conflict count surfaces correctly after the fix
  in `main.go` for nested `.relay/conflicts/<provider>/...` artifacts.
- Conflict artifacts (`.relay/conflicts/<path>.<rev>.local`) are
  produced by the syncer on 409 (proven by
  `TestBulkWrite_PerFileConflictCreatesArtifactAndRefreshesRemote` and
  by the productized E2E test).
- `.relay/permissions-denied.log` and `WriteDenied`/`DeniedHash`
  bookkeeping prevent retry storms after 403s
  (`syncer.go:1649,1734,1769,1829-1830`).

Visibility gaps:

- The contract's webhook-unhealthy banner row (§7.4) is **not** part
  of the human-readable `status` output. A user reading text output
  will miss a webhook-degraded provider unless they read JSON.
- `pendingDeadLetter` is not surfaced because no code path writes
  dead-letter records. `relayfile status` always shows zero, even if
  the Cloud returns `deadLetteredOps > 0` from `/sync`. The CLI does
  not mirror `state.providers[*].deadLetteredOps` from the Cloud
  response into a user-visible "dead-letter: N" row.

These are not invisible-by-default UX failures (JSON consumers can
read them), but they violate the §3.6/§7.4 promise that the synced
mirror's limitations are surfaced to the human user.

---

## 3. Is token refresh / rejoin trustworthy for long-lived mounts?

**Conditionally yes.** The mainline path is correct; a few hardening
items remain.

What is correct:

- Pre-emptive rejoin at 10% of JWT lifetime with a 5-minute floor
  (`relayfileTokenNeedsRefresh` at `main.go:3162`) — exactly §5.3's
  trigger.
- Reactive rejoin on 401/403 with one retry, then surface the error
  (`withAuthRefresh` at `main.go:3526-3542`) — §5.3 requires "a single
  rejoin attempt before the call is retried."
- Cloud token refresh at access-token-expiry-minus-60s
  (`cloudAccessTokenExpiredSoon`).
- After a successful rejoin the in-memory `HTTPClient.Token()` is
  swapped via `httpClient.SetToken(joined.Token)` and the websocket
  reset (`syncer.ResetWebSocket()`), so the running process keeps its
  loop without restart — also §5.3.
- The rejoin uses the cloud access token (refreshing first via
  `refreshCloudCredentialsIfNeeded`) and then re-issues `/join`. This
  matches the documented order.
- The E2E proof test forces this exact sequence: it rejects the
  current Relayfile token, expires the cloud access token, and then
  asserts `cloud.RefreshCount() >= 1 && cloud.JoinCount() >= 3` and
  that the on-disk credentials file was updated to a new token. It
  also proves the loop continues to materialize a new remote file
  *after* the swap. This is the strongest part of the proof.

What needs hardening before public launch:

- **Concurrent refresh coalescing inside a single process** is not
  enforced. `refreshMountAuth` has no mutex around the
  load/refresh/join/save sequence; if a poll cycle and a file-watcher
  callback both observe a 401 simultaneously they will both call
  `/api/v1/auth/token/refresh` and `/join`. The contract (§5.2)
  explicitly requires coalescing.
- **Credential file atomicity** is required by §5.2 ("write temp
  file, rename"). `saveCredentials` and `saveCloudCredentials` need
  to be audited (and tests added) to confirm they use a temp+rename
  pattern. The current code path was not exercised under crash/kill
  in the E2E test.
- **Refresh-token expiry → read-only degraded** (§5.4) is not
  implemented. The mount continues serving cached reads (because the
  Go process never crashes), but it does not actively refuse local
  writes with a `cloud_session_expired` reason in
  `.relay/permissions-denied.log` and does not emit the rate-limited
  per-minute recovery hint. Acceptance A9 is not present in the
  test suite.

---

## 4. Is Cloud/Nango provider readiness enough for many integrations?

**Adequate for the v1 catalog, not yet for "many".**

Strengths:

- The Cloud is the only Nango secret holder; the CLI never sees a
  Nango secret directly. `connect-session` returns a `connectionId`
  the CLI persists and reuses (per §6.3, observed in
  `productizedCloudMock.serveConnectSession`).
- `loadIntegrationCatalog` calls
  `GET /api/v1/integrations/catalog` and falls back to the v1 list
  hardcoded in `fallbackIntegrationCatalog` (`main.go:689-698`).
  This proves the catalog endpoint is the intended source of truth.
- The provider list under §6.2 (github, notion, linear, slack-sage,
  slack-my-senior-dev, slack-nightcto) is honored by the fallback,
  and `slack` is normalized to `slack-sage` in `normalizeProviderID`.
- The Cloud-side route and readiness slice (`tests/sdk-setup-client-routes.test.ts`,
  `tests/nango-sync-relayfile.test.ts`,
  `tests/nango-webhook-router-fanout.test.ts`,
  `tests/relayfile-writeback-bridge.test.ts`) all pass.

Gaps:

- **No 1-hour CLI/SDK cache** (§6.1): every call hits the catalog
  endpoint. Not a correctness bug, but it doesn't match the
  contract and makes the CLI noisier than necessary.
- **No 409 unknown_provider revalidation** (§6.1): the CLI does not
  bust its catalog cache on a 409 from `connect-session`. With no
  cache today this is moot; the moment a cache is added it has to
  ship with the bust path.
- **Provider deprecation rendering** (§6.4): `relayfile integration
  list` does not render a warning row for deprecated providers. The
  catalog response shape has space for `deprecated: true` but the CLI
  ignores it.
- **Webhook-readiness signal** (§7.4): plumbed into the struct but
  not rendered. See §2 above.

Net: adding new providers in v1.x via a single `providers.ts` PR
will work, but the agent-visible degradation signals (deprecated,
unhealthy webhook, catalog drift) are not yet wired through the CLI.

---

## 5. Is writeback safe enough for agents?

**Mostly safe for the cases it covers; partial for the cases it does
not.**

Safe by design:

- All `WriteFile`/`DeleteFile` carry `If-Match: <baseRevision>`
  (`syncer.go:198,223`). Bulk writes carry per-file revisions.
- 409 responses produce an artifact at
  `.relay/conflicts/<path>.<localRev>.local`, the remote is re-fetched
  in place, and `pendingConflicts` increments — confirmed both in the
  unit test (`TestBulkWrite_PerFileConflictCreatesArtifactAndRefresh
  esRemote`) and the productized E2E test.
- 403/permission denial sets `WriteDenied` + `DeniedHash` so retries
  only fire after the file content actually changes
  (`syncer.go:1734`). Denials are appended to
  `.relay/permissions-denied.log`.
- `X-Correlation-Id` is included on every call; mountsync's HTTP
  client and the syncer write the same id through the bulk path.

Not yet safe enough:

- **Schema validation feedback (§8.2)** is missing. A
  `400 schema_validation_failed` from the Cloud will surface as a
  generic error rather than the documented `<path>.invalid.<ts>`
  artifact + remote-restored-in-place behavior. Agents writing JSON
  payloads (e.g. PR review files) will not see structured field-level
  errors, only text from a generic HTTP error.
- **Dead-letter visibility (§8.4)** is missing. The directory is
  created, but `relayfile ops list` and `relayfile ops replay` are not
  implemented, and no `.relay/dead-letter/<id>.json` records are ever
  written by the syncer. A non-retryable 422 will currently fail the
  cycle silently (logged, not surfaced as a record).
- **Idempotency-key derivation** (§8.5: `(workspaceId, path,
  baseRevision, sha256(content))`): not visibly enforced by the syncer
  beyond `X-Correlation-Id`. The Cloud's `200 already_applied`
  short-circuit may not be exercised correctly by the current client.
- **`relayfile permissions [PATH]`** (§8.1) is missing — agents have
  no programmatic way to check whether a path is writeback-allowed
  short of trying and waiting for the denial.

For benign integrations (markdown notes, Linear comments) this is
acceptable. For schema-strict integrations (GitHub PR reviews, Slack
message formatting) this is a real gap.

---

## 6. Is the E2E proof truly end to end?

**End-to-end for the golden path; not end-to-end for the contract as
a whole.**

Strong:

- `TestProductizedCloudMountE2EProof` (~250 LOC, single test
  function) chains: `setup --cloud-token --provider github --once` →
  remote file appearance in mirror → `integration connect notion` →
  `integration list --json` asserts both providers → start the real
  `runMountLoop` → write a remote file, observe locally → write a
  local file, observe remote round-trip via the bulk endpoint →
  force a 409 conflict, observe `.relay/conflicts/<provider>/<path>
  .<rev>.local` → `relayfile status --json` reports the conflict and
  daemon metadata → expire cloud creds + reject the current Relayfile
  token → assert `/auth/token/refresh` and `/join` are called and the
  on-disk credential file flips → cancel the loop → re-run
  `relayfile mount demo <dir> --once --websocket=false` against the
  persisted config and prove a newly added remote file is picked up.
- Both Cloud and Relayfile sides are real `httptest.Server`s, not
  in-process stubs. The CLI under test calls them through the real
  `runSetup`/`runMount`/`runIntegration*`/`runStatus` code paths.
- Cloud-side mocks for Nango/webhook/writeback are exercised by the
  passing slice (`tests/sdk-setup-client-routes.test.ts`,
  `tests/nango-sync-relayfile.test.ts`,
  `tests/nango-webhook-router-fanout.test.ts`,
  `tests/relayfile-writeback-bridge.test.ts`).

Weak:

- The contract enumerates 16 acceptance scenarios A1–A16 in §10.2.
  The proof effectively covers A1, A4, A5, A8, A10 (partially) in a
  single test. **A2** (oauth state mismatch → exit 10), **A6**
  (schema validation invalid artifact), **A7** (dead-letter +
  ops replay), **A9** (refresh-token expiry degraded mode),
  **A11** (webhook unhealthy banner), **A12** (catalog refresh +
  409 revalidation), **A13** (mount help text + banner contents),
  **A14** (background pidfile + `relayfile stop` lifecycle),
  **A15** (SDK `RelayfileSetup` parity), and **A16** (multi-agent
  coordination through bulk writes) are not covered.
- The CI gate requirement in §10.4 (block merge on any A1–A16
  failure) is not implementable today because most of A1–A16 do not
  exist as separate tests. The single E2E test is the entire gate.
- Run-restart resilience is proved with `--once`, not with
  `--background` + `relayfile stop` lifecycle, so the production
  daemon path is not exercised in the proof.
- The E2E test does not assert structured log lines under
  `RELAYFILE_LOG=debug` (a §10.3 requirement).

---

## 7. Residual risks before public launch

In rough priority order:

1. **Missing CLI commands required by the contract**: `relayfile pull`,
   `relayfile permissions`, `relayfile ops list`, `relayfile ops replay`.
   Until these ship, the contract's writeback recovery and force-pull
   stories are not user-reachable. This is the largest gap.
2. **Dead-letter pipeline is empty**: the directory exists but nothing
   writes records or replays them. A 4xx from the Cloud during
   writeback will be silently lost from the user's perspective.
3. **Schema-validation conflict path missing**: structured 400s do not
   become `<path>.invalid.<ts>` artifacts. Agents writing JSON to
   schema-validated paths get poor failure UX.
4. **`webhookHealthy` not rendered in human status output**: silent
   webhook degradation slips through. Easy fix.
5. **Catalog 1-hour cache + 409 revalidation not enforced**: violates
   §6.1, will become visible the moment the catalog is iterated.
6. **Refresh-token expiry → degraded read-only (A9) not implemented**:
   long-lived mounts after a 7-day session will keep accepting writes
   that silently fail rather than refusing them with a documented
   reason and a per-minute recovery hint.
7. **Concurrent refresh coalescing not enforced**: under
   websocket-reconnect plus a poll cycle racing on a 401, two
   simultaneous `/auth/token/refresh` + `/join` round-trips can fire.
   Adds a soft race on Cloud and a brief window where two valid
   workspace tokens exist.
8. **Credential-file write atomicity** (`saveCredentials` /
   `saveCloudCredentials`) needs an explicit temp+rename audit and a
   crash-safety test. The contract requires it; the E2E test does
   not exercise it.
9. **Acceptance scenarios A2/A6/A7/A9/A11/A12/A13/A14/A15/A16 do not
   exist as standalone tests**, so the §10.4 CI gate is effectively
   one-test-fails-the-build rather than the contract-wide gate that
   was promised.
10. **SDK parity** (§9 / A15 / A16): `WorkspaceHandle.refreshToken`
    auto-invocation at 55 minutes, `agentInvite()` carrying
    `vfsRoot`, and the multi-agent disjoint-path bulk-write proof are
    not part of this evidence package and need to be confirmed
    separately before the SDK is considered launch-ready.
11. **Mount stall detection** logs "mount stalled: …" after 10
    minutes but does not include the stall reason in
    `state.json.stallReason` for the no-progress branch (only the
    failed-cycle branch). A consumer relying on `state.json` to detect
    stalls will not see the 10-minute idle case until the next cycle
    error overwrites the field.
12. **Trajectory and evidence files are checked-in noise**
    (`.trajectories/active/`, multiple `traj_*` files) — not a
    correctness risk, but should be cleaned up or moved out of
    tracked branches before tagging.

None of items 1–12 individually block an internal beta where users
are willing to read JSON, watch logs, and tolerate missing
recovery commands. Items 1, 2, 3, 6, 7, 8, 9 should all be closed
before a public v1 launch.

---

## Recommendation

- **Internal / early-access launch:** **go**. The golden path is
  proven end-to-end, the rejoin loop works under hostile token
  conditions, and the synced-mirror story is honest and testable.
- **Public v1 launch:** **no-go yet.** Land `pull`/`permissions`/
  `ops {list,replay}`, ship the schema-validation conflict path,
  render `webhookHealthy` in text status output, implement the
  refresh-token-expired degraded mode, add the missing A2/A6/A7/
  A9/A11–A16 acceptance tests, and audit credential-file atomicity
  and refresh coalescing. With those in, the v1 contract is
  defensible against a public release.

PRODUCTIZED_CLOUD_MOUNT_REVIEW_COMPLETE
