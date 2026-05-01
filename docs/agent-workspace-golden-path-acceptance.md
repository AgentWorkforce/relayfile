# Agent Workspace Golden Path — Implementation Contract

**Status:** v1 implemented and E2E verified  
**Builds on:** `docs/agent-workspace-golden-path.md`, `docs/sdk-setup-client.md`, `docs/sdk-setup-client-acceptance.md`  
**Affects repos:** `relayfile`, `cloud`, `relaycast`  
**E2E evidence:** `docs/evidence/agent-workspace-golden-path-e2e.log`

This document is the binding implementation contract for the v1 of the Agent
Workspace Golden Path. It resolves spec ambiguities, fixes file ownership by
repo, lists acceptance criteria, and names the exact commands required for
sign-off. Code changes that disagree with this document must update this
document first.

The relayfile-repo implementation (SDK, mount harness, demo, packaged E2E) is
complete as of SDK v0.6.0. The packaged golden-path E2E (`npm run -w
packages/sdk/typescript test:e2e:golden-path`) exits 0 and emits
`GOLDEN_PATH_E2E_READY`. Cloud and relaycast changes remain tracked in their
respective repos.

---

## 1. V1 Decisions

These decisions resolve the open questions in
`docs/agent-workspace-golden-path.md` for the purpose of v1 sign-off. Later
versions may extend behavior, but v1 is bound by what is below.

### 1.1 Method names

The v1 `WorkspaceHandle` golden-path surface is locked to these exact names.
Aliases will not be added in v1.

- `WorkspaceHandle.connectNotion(options?)`
- `WorkspaceHandle.waitForNotion(options?)`
- `WorkspaceHandle.mountEnv(options?)`
- `WorkspaceHandle.agentInvite(options?)`

`connectNotion` and `waitForNotion` are thin Notion-specific wrappers around
the existing `connectIntegration('notion', …)` and
`waitForConnection('notion', …)` primitives. The richer
`AgentWorkspace`/`setup.agentWorkspace()` orchestrator (Layer 2 in the spec)
is explicitly **not** part of v1.

### 1.2 Readiness semantics

For v1, "Notion ready" means **OAuth connected**, i.e. the cloud has persisted
the workspace integration row for `(workspaceId, 'notion')` and
`/api/v1/workspaces/:workspaceId/integrations/notion/status` returns
`ready: true`. Initial sync completion is not required for v1 readiness; an
empty `/notion` mount immediately after readiness is acceptable as long as the
status endpoint can later be enriched without breaking callers.

The fallback for richer sync state in v1:

- The status endpoint must keep returning at minimum `{ ready: boolean }`.
- It **may** also return `provider`, `connectionId`, `state`, `mountedPath`,
  and `sync` fields when the cloud has them. SDK callers must continue to
  function when only `{ ready: boolean }` is present.
- `waitForNotion()` resolves on `ready === true` and ignores any additional
  fields in v1. SDK consumers may read additional fields off the raw cloud
  response in their own code, but `waitForNotion()` itself does not surface
  them in v1.

This keeps cloud free to ship the richer status shape incrementally without
forcing a coordinated SDK release.

### 1.3 `relaycastBaseUrl` source

For v1, `relaycastBaseUrl` is **SDK-defaulted** to
`https://api.relaycast.dev`. The cloud join response **may** include
`relaycastBaseUrl`; when present and non-empty the SDK uses it, otherwise it
falls back to the default. This is backward-compatible: existing clouds
without the field continue to work unchanged.

`mountEnv()` and `agentInvite()` honor `options.relaycastBaseUrl` overrides
ahead of any cloud-sourced or default value, in that order:

1. `options.relaycastBaseUrl` (caller override)
2. cloud-sourced `relaycastBaseUrl` from the join response (when present)
3. `https://api.relaycast.dev` default

### 1.4 Invite secrecy

`agentInvite()` returns secret credential material. The contract for v1:

- The returned `AgentWorkspaceInvite` includes `relayfileToken` **by default**.
  This is the simplest trusted-agent handoff and matches the existing
  implementation tested in `setup-client` unit tests.
- Callers must be able to omit the relayfile token by passing
  `includeRelayfileToken: false`. Behavior in that case: the
  `relayfileToken` property is not present on the returned object.
- Documentation, demo scripts, and examples must avoid printing the full
  invite payload. Logs in v1 must redact `relayfileToken` and
  `relaycastApiKey` by default.
- v1 keeps using the lead agent's `relayfileToken`. Per-agent scoped tokens
  minted server-side are explicitly out of scope for v1 and tracked under
  open question Q2 in the spec.

---

## 2. File Ownership by Repo

Each repo owns a defined set of paths. The implementation must not modify
files outside its repo's ownership block. Tooling-generated changes (build
artifacts, dist outputs, lockfile churn) must be reverted unless they are part
of the listed paths.

### 2.1 Relayfile (`/Users/khaliqgant/Projects/AgentWorkforce/relayfile`)

SDK source and tests:
- `packages/sdk/typescript/src/setup.ts`
- `packages/sdk/typescript/src/setup-types.ts`
- `packages/sdk/typescript/src/setup-errors.ts`
- `packages/sdk/typescript/src/setup.test.ts`
- `packages/sdk/typescript/src/index.ts` (only golden-path exports)
- `packages/sdk/typescript/scripts/setup-e2e.mjs` and any new
  golden-path E2E scripts in that directory
- `packages/sdk/typescript/CHANGELOG.md`
- `packages/sdk/typescript/README.md`
- `packages/sdk/typescript/AGENTS.md`
- `packages/sdk/typescript/package.json` (version + scripts only when needed)

Mount and harness:
- `cmd/relayfile-mount/` (existing files only; no new top-level binaries
  unless this contract is updated)
- `internal/mountsync/`
- `packages/local-mount/src/` and `packages/local-mount/dist/` build output
- **Actual harness location (v1):** The deterministic mount harness was
  implemented as `packages/sdk/typescript/src/mount-harness.ts` (and
  `mount-harness.test.ts`) rather than under `packages/local-mount/` or
  `cmd/relayfile-mount/`. This deviation was intentional — keeping the harness
  in the SDK package allows `npm pack` consumers to import it without a
  separate package install. The harness is exported from the SDK's
  `dist/mount-harness.js` entry point.

Demo:
- A new demo script under `packages/sdk/typescript/examples/` or
  `packages/sdk/typescript/scripts/`. The exact path is the implementer's
  choice, but it must be referenced from the README and from
  `docs/agent-workspace-golden-path.md`.

Docs:
- `docs/agent-workspace-golden-path.md` (updated status + caveats)
- `docs/agent-workspace-golden-path-acceptance.md` (this file)
- `docs/sdk-setup-client.md` (cross-link only, no duplication)

### 2.2 Cloud (`/Users/khaliqgant/Projects/AgentWorkforce/cloud`)

Routes:
- `packages/web/app/api/v1/workspaces/route.ts`
- `packages/web/app/api/v1/workspaces/[workspaceId]/join/route.ts`
- `packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts`
- `packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts`

Libs:
- `packages/web/lib/workspace-registry.ts`
- `packages/web/lib/relay-workspaces.ts` (only fields used by the join shape)
- `packages/web/lib/integrations/*` (Notion / Nango handling required to
  satisfy the connect/status contract)
- `packages/cloud-core/src/workspace/registry.ts`

Tests:
- `tests/sdk-setup-client-routes.test.ts`
- `tests/orchestrator/workspace-registry.test.ts`
- `tests/unified-workspace.test.ts`
- New `tests/agent-workspace-golden-path.*.test.ts` files as needed; they
  must live under `tests/` and use existing helpers in `tests/helpers/`.

### 2.3 Relaycast (`/Users/khaliqgant/Projects/AgentWorkforce/relaycast`)

SDK and types:
- `packages/sdk-typescript/src/setup.ts`
- `packages/sdk-typescript/src/setup-types.ts`
- `packages/sdk-typescript/src/setup-errors.ts`
- `packages/sdk-typescript/src/index.ts` (only invite-consumption exports)
- `packages/types/src/` (only fields required for the golden path)

Tests:
- `packages/sdk-typescript/src/__tests__/` for invite consumption tests
- `packages/server/` and `packages/cli/` are **not** modified for v1.

Docs:
- `docs/sdk-setup-client.md` (cross-link to relayfile golden path doc only)
- `docs/sdk-setup-client-acceptance.md` (no edits required for v1)

### 2.4 No unrelated changes

No other files in any of the three repos may be touched as part of this work.
This includes:

- No drive-by formatting, import reordering, or rename refactors.
- No edits to unrelated docs, READMEs, schemas, migrations, or workflows.
- No changes to `.trajectories/`, `.agent-relay/`, `dist/` outputs, or
  generated `drizzle/` files unless they are the direct, mechanical product
  of an in-scope change.
- No new packages, lockfile rewrites beyond what's needed for in-scope
  dependencies, or version bumps to other packages.

If a change outside these blocks appears necessary, this acceptance contract
must be updated first in the same PR.

---

## 3. SDK Acceptance (`@relayfile/sdk`)

### 3.1 `connectNotion`

- Calls
  `POST /api/v1/workspaces/:workspaceId/integrations/connect-session` with
  body `{ allowedIntegrations: ['notion'] }`. No other allowed integration is
  permitted in the request, even if the caller passes
  `options.allowedIntegrations`.
- Returns the existing `ConnectIntegrationResult` shape:
  `{ alreadyConnected, connectLink, sessionToken, expiresAt, connectionId }`.
- When the response omits `connectionId`, the workspace ID is used as the
  fallback connection ID, mirroring `connectIntegration`'s current behavior.
- Stores the resolved `connectionId` in the handle's pending-connections map
  under the `notion` provider so `waitForNotion()` can omit it.

### 3.2 `waitForNotion`

- Delegates to `waitForConnection('notion', options)`.
- Honors `pollIntervalMs` and the deprecated `intervalMs` alias.
  The `intervalMs` alias must continue to work in v1 with a runtime warning
  is **not** required, matching the current setup-client behavior.
- Honors `timeoutMs`, `signal`, and `onPoll(elapsed)` exactly as documented
  in `setup-types.ts`. `onPoll` is invoked on every iteration before the
  status check, including the first iteration with elapsed `0`.
- Times out with `IntegrationConnectionTimeoutError` carrying
  `provider: 'notion'`, the resolved `connectionId`, `elapsedMs`, and
  `timeoutMs`.

### 3.3 `mountEnv`

- Returns a `Record<string, string>` with these keys, all required when their
  source value is non-empty:
  - `RELAYFILE_BASE_URL` — `info.relayfileUrl`
  - `RELAYFILE_TOKEN` — current handle token
  - `RELAYFILE_WORKSPACE` — `workspaceId`
  - `RELAYFILE_REMOTE_PATH` — `options.remotePath` (defaulting to `/`)
  - `RELAYFILE_LOCAL_DIR` — `options.localDir` when provided
  - `RELAYFILE_MOUNT_MODE` — `options.mode` when provided
  - `RELAYCAST_API_KEY` and `RELAY_API_KEY` — `info.relaycastApiKey`
  - `RELAYCAST_BASE_URL` and `RELAY_BASE_URL` — resolved per §1.3
- Empty/undefined values are omitted from the returned record.
- `relayfile-mount` flag mapping — every key above must be consumable via
  the existing `cmd/relayfile-mount` flags; this contract does not require
  adding new flags.

### 3.4 `agentInvite`

- Returns `AgentWorkspaceInvite` with: `workspaceId`, `cloudApiUrl`,
  `relayfileUrl`, `relaycastApiKey`, `relaycastBaseUrl` (resolved per §1.3),
  `agentName`, `scopes`, optional `relayfileToken`, optional `createdAt`,
  optional `name`.
- Does not mutate `_joinOptions.scopes`. When `options.scopes` is omitted,
  the returned `scopes` array is a **copy** of the join scopes.
- `includeRelayfileToken: false` produces a payload without
  `relayfileToken`; otherwise the property is present and equals the current
  handle token.
- Defaults `agentName` to the lead handle's `agentName`.

### 3.5 Setup-client compatibility

These existing behaviors must remain intact:

- `waitForConnection` accepts `pollIntervalMs` and the deprecated
  `intervalMs` alias and supports `onPoll(elapsed)`.
- All cloud requests carry the `X-Relayfile-SDK-Version` header set to
  `RELAYFILE_SDK_VERSION` from `setup.ts`.
- `joinWorkspaceResponse` body includes `agentName`, `scopes`, and
  `permissions` exactly as the existing tests assert.
- Token refresh flow continues to use the original join options.

### 3.6 Public exports

The package's public entry must export:

- `RelayfileSetup`, `WorkspaceHandle`
- `AgentWorkspaceInvite`, `AgentWorkspaceInviteOptions`
- `WorkspaceMountEnv`, `WorkspaceMountEnvOptions`
- `ConnectIntegrationResult`, `ConnectIntegrationOptions`
- `WaitForConnectionOptions`
- All existing setup-client errors

No source-internal types may be added to the public surface beyond what is
listed here.

### 3.7 Packaged consumer E2E

The packaged E2E test that proves the journey end-to-end must:

- Build the SDK via `npm pack` (or equivalent) and install the resulting
  tarball into a temporary consumer project.
- Import `@relayfile/sdk` from that installed package, never from source
  files in `packages/sdk/typescript/src`.
- Run the entire 14-step journey listed in §“Required Packaged E2E” of
  `docs/agent-workspace-golden-path.md`.
- Assert concrete HTTP paths, request bodies, response fields, headers,
  relaycast messages, and filesystem observations — not merely that
  promises resolved.

---

## 4. Cloud Acceptance

### 4.1 Workspace join

`POST /api/v1/workspaces/:workspaceId/join` must:

- Continue to return `workspaceId`, `token`, `relayfileUrl`, `wsUrl`, and
  `relaycastApiKey`.
- Include `relaycastBaseUrl` **when configured** by the deployment
  environment. When not configured, the field may be omitted; SDK fallback
  per §1.3 applies. Backward compatibility with existing SDK versions must
  not regress.
- Reject unknown scopes only once cloud begins minting scope-specific
  tokens. v1 may continue to accept the documented scopes
  (`fs:read`, `fs:write`, `relaycast:read`, `relaycast:write`, plus the
  existing `sync:read` default) and ignore unknown ones, matching today.

### 4.2 Notion `connect-session`

- Accepts a relayfile JWT whose workspace claim matches the URL
  `:workspaceId`.
- Returns `403` when the JWT workspace claim does not match.
- Returns `401` when no auth or an unsigned JWT is provided.
- Accepts `body.allowedIntegrations: ['notion']` and any equivalent Nango
  config keys (e.g. `notion-sage`) without rejecting them.
- Returns `connectionId` in the response body. When the upstream provider
  does not supply one, the cloud falls back to the workspace ID, matching
  the SDK fallback in §3.1.

### 4.3 Notion status

`GET /api/v1/workspaces/:workspaceId/integrations/notion/status`:

- Returns `{ ready: true }` once the workspace integration row for
  `(workspaceId, 'notion')` is present and matches the supplied
  `connectionId` (or the workspace polling key).
- Returns `{ ready: false }` before the webhook persists the row.
- May additionally return `provider`, `connectionId`, `state`,
  `mountedPath`, and `sync` fields. When present, these must be
  syntactically valid per the spec and must not break SDK callers that
  only read `ready`.
- `DELETE` continues to disconnect the integration via the existing Nango
  + workspace integration teardown path.

### 4.4 Error coverage

The route tests must cover:

- `401` when no credentials are provided.
- `403` when relayfile JWT workspace claim does not match the route.
- `404` when the provider is not in the allow-list.
- Pre-webhook `{ ready: false }` for both connect-session and status.
- Expired or unsigned JWT rejected on connect-session.
- Unknown provider on connect-session and status.

Existing tests in `tests/sdk-setup-client-routes.test.ts` already cover
several of these; new tests must add the Notion-specific assertions and any
gaps surfaced while implementing §4.1–§4.3.

---

## 5. Relaycast Acceptance

### 5.1 Invite consumption

Given an `AgentWorkspaceInvite` produced by the lead agent, the relaycast
SDK must let an invited agent:

1. Construct a Relaycast client from the invite's `relaycastApiKey` and
   `relaycastBaseUrl`.
2. Register or assume the agent identity named by `invite.agentName`
   without colliding with the lead agent.
3. Send a "ready" message to a channel or DM that the lead agent can
   observe.
4. Receive at least one message from the lead agent.
5. Reference relayfile paths inside messages as plain strings; relaycast
   does not need to resolve them.

The exact message API is the existing `RelayCast` SDK surface; no new
top-level methods are required for v1.

### 5.2 Failure modes

- An invalid `relaycastApiKey` results in a deterministic auth error
  surfaced from the existing `RelayCast` client (no silent retry loop).
- A missing `relaycastBaseUrl` falls back to `https://api.relaycast.dev`,
  matching §1.3.
- An agent name conflict surfaces the existing `RelayError` /
  `name_conflict` (status 409) path.

### 5.3 Tests and docs

- New invite-consumption tests live in
  `packages/sdk-typescript/src/__tests__/` and exercise the four steps
  above against a mock Relaycast server.
- `docs/sdk-setup-client.md` adds a one-line cross-link to the relayfile
  golden-path doc; it does not duplicate the journey.

---

## 6. Mount Acceptance

### 6.1 Launching from `mountEnv()`

`relayfile-mount` (or the deterministic test harness, see §6.2) must start
purely from the env block produced by `WorkspaceHandle.mountEnv()`:

```bash
relayfile-mount \
  --base-url "$RELAYFILE_BASE_URL" \
  --token "$RELAYFILE_TOKEN" \
  --workspace "$RELAYFILE_WORKSPACE" \
  --remote-path "$RELAYFILE_REMOTE_PATH" \
  --local-dir "$RELAYFILE_LOCAL_DIR"
```

`RELAYFILE_MOUNT_MODE` is honored when present; default mode remains
`poll`.

### 6.2 Deterministic mount harness

Because FUSE is unreliable in CI, the v1 packaged E2E must use a
deterministic mount harness rather than relying on host FUSE. The harness
must:

- Live in-tree (see §2.1) and be runnable as a Node or Go binary from
  `npm pack`-installed artifacts and from `go run ./cmd/...`.
- Accept the same env block as `relayfile-mount`.
- Mirror `RELAYFILE_REMOTE_PATH` to `RELAYFILE_LOCAL_DIR` once at startup
  and on each polling cycle.
- Surface read-only denial as a non-zero exit code or a clearly identified
  error event when an invited agent with `fs:read`-only scope attempts to
  write.
- Exit cleanly on `SIGINT` / `SIGTERM`.

The harness is a test artifact; it does not replace `relayfile-mount` for
production use.

### 6.3 Proven behaviors

The mount E2E must prove:

1. The harness starts from `mountEnv()` output.
2. The local directory exists after startup.
3. A file seeded on the relayfile server under
   `RELAYFILE_REMOTE_PATH` is readable at the corresponding path under
   `RELAYFILE_LOCAL_DIR`.
4. Writes from a `fs:read`-only agent fail with a permission-specific
   error.
5. The harness exits cleanly on shutdown with no orphaned processes or
   stale files in the local directory beyond the synced content.

---

## 7. Docs and Demo Acceptance

### 7.1 `docs/agent-workspace-golden-path.md`

- Status header is updated from `Proposed` to either `Implemented` or
  `Partially Implemented`. `Partially Implemented` is acceptable only when
  paired with a clearly named caveats list.
- The Open Questions section is updated to reflect the v1 decisions in
  §1, with explicit pointers from each closed question to this acceptance
  doc.
- Every command, env var, and method name in the doc matches the SDK and
  cloud as shipped.

### 7.2 `docs/sdk-setup-client.md`

- Adds a cross-link to `docs/agent-workspace-golden-path.md` in a single
  paragraph or list item.
- Does **not** duplicate journey content. Setup-client docs continue to
  describe the primitive contract; the golden-path doc owns the user
  journey.

### 7.3 Demo

- A demo script is documented in the SDK README and in
  `docs/agent-workspace-golden-path.md`.
- The script either:
  - Runs end-to-end against in-process mocks (cloud, relayfile, relaycast),
    or
  - Clearly identifies the real credentials it requires (`RelayfileSetup.login()`
    Cloud sign-in or a raw `RELAY_ACCESS_TOKEN`, Notion OAuth completion by the
    human, etc.) and what each one is for.
- The README invocation matches the implementation (e.g.
  `npm run demo:agent-workspace --workspace=packages/sdk/typescript`).

---

## 8. Required Commands

The implementation is signed off only when these commands all pass on a
clean checkout, in their respective repo roots, with no skipped tests
relevant to the golden path.

### 8.1 Relayfile (`/Users/khaliqgant/Projects/AgentWorkforce/relayfile`)

```bash
# SDK unit tests (includes mount-harness.test.ts)
npm run -w packages/sdk/typescript test

# SDK type check + build
npm run -w packages/sdk/typescript build

# SDK packaged setup E2E (existing)
npm run -w packages/sdk/typescript test:e2e

# SDK packaged golden-path E2E (new for this work)
# Alias: npm run -w packages/sdk/typescript agent-workspace:e2e
npm run -w packages/sdk/typescript test:e2e:golden-path

# Local mount package tests (separate from mount harness in SDK)
npm run -w packages/local-mount test
npm run -w packages/local-mount build

# Mount + sync tests (Go)
go test ./cmd/relayfile-mount/... ./internal/mountsync/...

# Demo smoke (must exit 0; mocks acceptable)
npm run demo:agent-workspace --workspace=packages/sdk/typescript
```

### 8.2 Cloud (`/Users/khaliqgant/Projects/AgentWorkforce/cloud`)

```bash
# Targeted route tests
npx vitest run tests/sdk-setup-client-routes.test.ts
npx vitest run tests/orchestrator/workspace-registry.test.ts
npx vitest run tests/unified-workspace.test.ts
# Plus any new tests/agent-workspace-golden-path*.test.ts files

# Typecheck
npm run -w packages/web typecheck
npm run -w packages/cloud-core typecheck

# Web regressions
npm run -w packages/web test
```

### 8.3 Relaycast (`/Users/khaliqgant/Projects/AgentWorkforce/relaycast`)

```bash
# Targeted SDK tests
npm run -w packages/sdk-typescript test

# Typecheck
npm run -w packages/sdk-typescript build

# Repo-wide regressions
npm test
```

### 8.4 Full packaged golden-path E2E

The single command that proves the cross-repo journey end-to-end:

```bash
# From the relayfile repo
npm run -w packages/sdk/typescript test:e2e:golden-path
```

This script must spin up mock or local cloud, relayfile, and relaycast
servers, run the 14-step journey from
`docs/agent-workspace-golden-path.md`, and shut everything down cleanly. It
must use `npm pack` artifacts for the SDK under test.

---

## 9. Commit Policy

- Each repo gets its own commit(s). No cross-repo commits.
- Within a repo, related changes may be squashed but unrelated dirty files
  must not be staged. In particular:
  - Do not stage `.trajectories/`, `.agent-relay/`, or local cache
    directories unless this contract explicitly says so.
  - Do not include drive-by formatting changes.
  - Do not stage build outputs (`dist/`, `.next/`, `tsbuildinfo`) unless
    they are the direct mechanical product of an in-scope source change.
- Commit messages reference the spec (`docs/agent-workspace-golden-path.md`)
  and this contract (`docs/agent-workspace-golden-path-acceptance.md`) so a
  reviewer can trace the change to its acceptance criteria.
- The relayfile commit lands first (it owns the contract). Cloud and
  relaycast commits land independently and may interleave with each other,
  but the cross-repo E2E in §8.4 must pass with all three landed before the
  feature is considered shipped.

AGENT_WORKSPACE_ACCEPTANCE_READY
