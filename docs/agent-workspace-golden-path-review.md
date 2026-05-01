# Agent Workspace Golden Path — Final Review

**Reviewer scope:** current working-tree diffs in `relayfile`, `cloud`, and
`relaycast`, plus `docs/agent-workspace-golden-path.md`,
`docs/agent-workspace-golden-path-acceptance.md`, and the recorded evidence.

## Checklist

### 1. One-link Notion path is implemented and tested — PASS

`WorkspaceHandle.connectNotion()` (`packages/sdk/typescript/src/setup.ts:367`)
forces `allowedIntegrations: ['notion']` even when the caller passes other
values, returning a single `connectLink`. Unit test
`forces connectNotion to request only the notion integration`
(`setup.test.ts:353`) and the packaged E2E (`scripts/agent-workspace-golden-path-e2e.mjs:825`)
both assert the request body is exactly `{ allowedIntegrations: ['notion'] }`.
The lead-agent script in the E2E prints exactly one Notion connect link
(`leadSetup.connectLink`, asserted to equal `https://connect.mock.local/notion`).

### 2. `waitForConnection` is spec-correct — PASS

`waitForConnection` (`setup.ts:376`) honors `pollIntervalMs` (preferring it
over the deprecated `intervalMs` alias), invokes `onPoll(elapsedMs)` on every
iteration before the status check, and resolves on `ready === true`. Timeout
maps to `IntegrationConnectionTimeoutError` carrying provider, connectionId,
elapsedMs, and timeoutMs. `waitForNotion` is a thin delegate (`setup.ts:435`),
proven by the spy test at `setup.test.ts:374`.

### 3. Setup cloud calls send `X-Relayfile-SDK-Version` — PASS

Header added in `setup.ts:214` and asserted on the create-workspace, join,
connect-session, and Notion status requests in the E2E
(`scripts/agent-workspace-golden-path-e2e.mjs:807, 819, 833, 1025`).

### 4. Invite is documented as secret and may omit `relayfileToken` — PASS

- `docs/agent-workspace-golden-path.md` §V1 Caveats and §Security Model
  document invite secrecy.
- `docs/agent-workspace-golden-path-acceptance.md` §1.4 codifies the
  `includeRelayfileToken: false` opt-out and the redaction requirement.
- `WorkspaceHandle.agentInvite` returns a `compactObject`, so the property is
  fully absent (not just undefined). `setup.test.ts:481` asserts
  `'relayfileToken' in tokenlessInvite` is `false`. The E2E asserts the same
  on `tokenlessInvite` at `scripts/setup-e2e.mjs:993`.
- README warns "Never log invite — it contains credential material".

### 5. `mountEnv` carries every relayfile-mount + relaycast var — PASS

`mountEnv()` (`setup.ts:467`) emits:
`RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`, `RELAYFILE_WORKSPACE`,
`RELAYFILE_REMOTE_PATH` (default `/`), `RELAYFILE_LOCAL_DIR`,
`RELAYFILE_MOUNT_MODE` (when set), `RELAYCAST_API_KEY`/`RELAY_API_KEY`, and
`RELAYCAST_BASE_URL`/`RELAY_BASE_URL`. Empty values are omitted via
`compactStringRecord`. The new override-priority resolver
(`resolveRelaycastBaseUrl`, `setup.ts:568`) implements
caller → cloud-sourced → default in that order, with both new tests
(`setup.test.ts:393, 432`) and the E2E
(`scripts/setup-e2e.mjs:961, 970`) covering it. The mount harness contract
(`mount-harness.ts:73`) reads exactly these keys.

### 6. Cloud status is backward-compatible and richer state is optional — PASS

The Notion-specific `buildNotionIntegrationStatus`
(`cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts`)
only fires when both `ready` is true AND the provider is `notion`. Other
providers still receive `{ ready: true }`. The pre-webhook path returns just
`{ ready }`. The enriched payload normalizes `metadata.sync.state` into the
documented states and only adds the `sync` object when at least one detail
field is populated. New tests cover both `oauth_connected` and `failed`
flows. SDK consumers continue to read only `ready`
(`IntegrationStatusResponse` in `setup.ts:77`), satisfying the contract that
older callers must not regress. The cloud join endpoint adds
`relaycastBaseUrl` only when `RELAYCAST_URL`/`RELAYCAST_API_URL` is set
(`workspace-registry.ts:25-31`, `join/route.ts:121-129`), so existing
deployments without the env keep working.

### 7. Relaycast proof of lead/review-agent message exchange — PASS

`relaycast/packages/sdk-typescript/src/__tests__/invite-consumption.test.ts`
covers:
- joining from invite fields and exchanging "ready" + "ack" DMs between
  `lead-agent` and `review-agent`, with relayfile paths embedded in message
  text and per-agent token authorization verified;
- `registerOrRotate` returning the same agent id with a rotated token;
- default base-URL fallback when an invite omits `relaycastBaseUrl`;
- 401 surfacing without retry on an invalid `relaycastApiKey`.

The packaged E2E in relayfile additionally exercises register + 3-message
ready/review.ready/ack exchange against a mock relaycast server
(`scripts/agent-workspace-golden-path-e2e.mjs:1065-1145`).

### 8. E2E uses packed artifacts + asserts paths, messages, filesystem — PASS

`agent-workspace-golden-path-e2e.mjs` runs `npm pack` for both
`packages/core` and `packages/sdk/typescript`, installs the tarballs into a
temp consumer directory, writes ESM consumer scripts that import
`@relayfile/sdk` from the installed package (and the harness from
`@relayfile/sdk/dist/mount-harness.js`), and runs four spawned processes
(lead agent, lead harness, invited harness, invited agent). Concrete
assertions: HTTP method+pathname for cloud workspace create/join/connect/status,
authorization headers, search params (`connectionId`, `agentName`),
relayfile `fs/tree` and `fs/file` calls, mount harness file contents on disk
(`readFile(path.join(leadLocalDir, "research/brief.md"))`), the exact
relaycast message sequence, and a permission-denied event from the
read-only invited harness. End markers `AGENT_WORKSPACE_E2E_OK` and
`GOLDEN_PATH_E2E_READY` are emitted (matched in
`docs/evidence/agent-workspace-golden-path-e2e.log`).

### 9. Docs match shipped commands without overpromising real Notion — PASS

- `package.json` exposes `demo:agent-workspace`, `test:e2e:golden-path`,
  and `agent-workspace:e2e`. Each is referenced in the README and in
  `docs/agent-workspace-golden-path.md` exactly as named.
- The README's demo blurb explicitly says it "defaults to in-process mock
  cloud and relayfile servers... without requiring real Notion, Relaycast,
  or cloud credentials" and points readers at `RelayfileSetup.login()`,
  `RelayfileSetup.fromCloudTokens()`, or a raw `RELAY_ACCESS_TOKEN` plus a
  human-completed Notion OAuth flow only "for a real deployment".
- `docs/agent-workspace-golden-path.md` status header reads
  "Partially Implemented" with the V1 Caveats section calling out that
  Layer 2 `agentWorkspace()` is out of scope, and Phase 3 explicitly notes
  that real cloud changes are tracked separately. No claim of real Notion
  sync content is made — the readiness criterion is OAuth connection only.
- `docs/environment-variables.md` was updated in the same diff to move
  `RELAYFILE_MOUNT_MODE` out of the "intentionally excluded" list now that
  `mountEnv(options.mode)` actively sets it.

### 10. No unrelated churn — PASS AFTER REMEDIATION

The initial review found unrelated churn in `../cloud` and `../relaycast`.
That blocker has been remediated:

- `../cloud/packages/web/app/api/v1/cloud-agents/route.ts` was restored to
  HEAD.
- `../cloud/packages/web/app/api/v1/workspaces/[workspaceId]/integrations/slack/route.ts`
  was restored to HEAD.
- `../relaycast/package.json`, `../relaycast/package-lock.json`, and
  `../relaycast/packages/server/package.json` were restored to HEAD.
- The untracked relaycast `.relay/` and `packages/server/scripts/` artifacts
  were moved out of the repo to `/tmp/relaycast-out-of-scope-20260501181105`
  rather than included in this work.
- The workflow commit steps were narrowed so cloud and relaycast commits stage
  only files allowed by the acceptance contract.

Remaining unrelated local state, such as trajectory files and unrelated
untracked cloud work, must not be staged for this PR. The narrowed commit
commands enforce that for the workflow path.

## Spot-checks beyond the checklist

- The `WorkspaceInfo` type now carries `relaycastBaseUrl?` (`setup-types.ts:48`),
  and `validateJoinWorkspaceResponse` accepts it as an optional string while
  still requiring the original five fields — backward compatible with clouds
  that do not emit it.
- `agentInvite` produces a fresh `[...this._joinOptions.scopes]` copy and is
  asserted not to mutate the lead handle's scopes (`setup.test.ts:493`).
- The E2E asserts the `RELAYFILE_SDK_VERSION` constant (`0.6.0`) appears on
  every cloud request, which catches drift if the constant is bumped without
  also updating the E2E expectations.
- The mount harness exposes a typed `MountHarnessPermissionError` with
  `code`, `path`, and `scopes`, and the E2E asserts the exact denied path
  `/notion/review/notes.md` and the read-only scope set.
- The `relaycastBaseUrl` precedence is consistent across `mountEnv`,
  `agentInvite`, and the WorkspaceHandle (single resolver method), so the
  three call sites cannot drift.

## Remediation Verification

After scope cleanup, the following commands passed:

- `npm run test --workspace=packages/sdk/typescript -- src/setup.test.ts src/mount-harness.test.ts`
- `npm run typecheck --workspace=packages/sdk/typescript`
- `npx vitest run tests/sdk-setup-client-routes.test.ts tests/agent-workspace-golden-path-routes.test.ts` in `../cloud`
- `npx tsc -p packages/web/tsconfig.json --noEmit` in `../cloud`
- `npm run test --workspace=packages/sdk-typescript -- src/__tests__/invite-consumption.test.ts` in `../relaycast`
- `npm run agent-workspace:e2e --workspace=packages/sdk/typescript`

The substantive golden-path implementation is sound across all three repos,
the out-of-scope churn has been removed from the intended commit set, and the
E2E proof remains convincing.

REVIEW_APPROVED
