# Agent Workspace Golden Path

**Status:** Partially Implemented  
**Builds on:** `docs/sdk-setup-client.md`  
**Affects:** `packages/sdk/typescript`, `cmd/relayfile-mount`, `../cloud`, `../relaycast`  
**Acceptance contract:** `docs/agent-workspace-golden-path-acceptance.md`

### V1 Caveats

The following decisions are binding for v1 and differ from the original open questions. See `docs/agent-workspace-golden-path-acceptance.md §1` for the full rationale.

- **Readiness:** `waitForNotion()` resolves when **OAuth is connected** (`ready: true` from the status endpoint). Initial sync completion is not required for v1; an empty `/notion` mount immediately after readiness is acceptable.
- **Method names:** `connectNotion`, `waitForNotion`, `mountEnv`, `agentInvite` are locked. The `AgentWorkspace` / `setup.agentWorkspace()` orchestrator (Layer 2) is **not** part of v1.
- **`relaycastBaseUrl`:** SDK-defaulted to `https://api.relaycast.dev`. Cloud join response may override it. Caller `options.relaycastBaseUrl` takes highest precedence.
- **Invite secrecy:** `agentInvite()` includes `relayfileToken` by default. Pass `includeRelayfileToken: false` to omit it. Never log the full invite payload.
- **Per-agent scoped tokens:** Out of scope for v1. The lead agent's `relayfileToken` is reused.

### Demo

```bash
# Run the golden-path demo against in-process mocks
npm run demo:agent-workspace --workspace=packages/sdk/typescript
```

The demo script (`packages/sdk/typescript/scripts/agent-workspace-demo.mjs`) prints a Notion connect link, waits for readiness, mounts the workspace through the deterministic harness, seeds and reads a `/notion` file, creates an invited agent with read-only scope, and confirms read-only denial — all against in-process mock servers. Relaycast message exchange is covered by the packaged E2E (`scripts/agent-workspace-golden-path-e2e.mjs`), not the demo.

E2E evidence: `docs/evidence/agent-workspace-golden-path-e2e.log`.

---

## Problem

The setup client gives an agent the primitives it needs to create or join a
relayfile workspace, request an OAuth integration link, poll for readiness, and
obtain relayfile and relaycast credentials. That is necessary, but it is not yet
the product experience.

The intended experience is simpler: a single agent should be able to give its
human one link to connect Notion, wait until the workspace is ready, mount that
workspace locally or inside a cloud sandbox, and invite other agents to
communicate through relaycast while working on the same relayfile filesystem.

Today that still requires the agent author to understand several boundaries:
cloud workspace setup, Nango connect sessions, relayfile mount environment
variables, relaycast API keys, agent naming, filesystem readiness, credential
handoff, and cleanup. The golden path should collapse those details into one
obvious flow.

---

## Goal

Make relayfile the easiest way for an agent to connect a human workspace,
navigate it as a filesystem, and bring other agents into the same working room.

The target user experience is:

1. A lead agent creates or joins a workspace.
2. The lead agent prints one Notion connect link for the human.
3. The human authorizes Notion.
4. The lead agent waits until Notion is confirmed ready.
5. The lead agent mounts the relayfile workspace at a local or cloud path.
6. The lead agent invites one or more trusted agents.
7. Invited agents join the same relaycast workspace and mount or access the
   same relayfile workspace.
8. All agents can coordinate over relaycast and read or write files through
   relayfile according to scoped permissions.

The TypeScript path should feel like this:

```ts
import { RelayfileSetup } from '@relayfile/sdk'

const setup = new RelayfileSetup({
  accessToken: process.env.RELAY_ACCESS_TOKEN,
})

const workspace = await setup.createWorkspace({
  name: 'acme-notion-room',
  agentName: 'lead-agent',
  scopes: ['fs:read', 'fs:write', 'relaycast:write'],
})

const notion = await workspace.connectNotion()
if (notion.connectLink) {
  console.log(`Connect Notion: ${notion.connectLink}`)
  await workspace.waitForNotion({
    timeoutMs: 5 * 60 * 1000,
    onPoll: (elapsed) => {
      process.stdout.write(`\rWaiting for Notion... ${Math.round(elapsed / 1000)}s`)
    },
  })
}

const mountEnv = workspace.mountEnv({
  localDir: '/workspace/notion',
  remotePath: '/notion',
})

const reviewer = workspace.agentInvite({
  agentName: 'review-agent',
  scopes: ['fs:read', 'relaycast:write'],
})
```

---

## Non-goals

This spec does not replace `docs/sdk-setup-client.md`. The setup-client spec is
the primitive SDK and cloud contract. This spec composes those primitives into a
complete agent workflow.

This spec does not require implementing a new Notion sync engine. It assumes the
existing integration ingestion path can project Notion content into relayfile
under a stable provider root.

This spec does not require untrusted public sharing. Agent invites are scoped
handoffs for trusted agents in the same user workflow, not public links.

This spec does not require relaycast to become a file transport. Relaycast is
for coordination, presence, messages, and workflow events. Relayfile remains the
filesystem transport.

---

## Terms

**Lead agent** is the first agent in the workflow. It creates or joins the
workspace, asks the human to connect Notion, and invites other agents.

**Human connector** is the user who opens the Notion OAuth link and grants
access.

**Invited agent** is another trusted agent process that receives an invite
payload from the lead agent.

**Agent workspace** is the combined relayfile workspace plus relaycast workspace
and integration readiness state.

**Mount** is a local directory backed by relayfile, usually created by
`relayfile-mount`.

---

## System Shape

```text
Human
  |
  | opens Notion connectLink
  v
Cloud / Nango
  |
  | webhook confirms connection
  v
Cloud workspace registry
  |
  | join returns relayfile JWT + relaycast key
  v
Lead agent
  |                    |
  | relayfile-mount    | relaycast invite/message
  v                    v
Relayfile filesystem   Relaycast workspace
  |                    |
  +---------+----------+
            |
            v
      Invited agents
```

The lead agent should not need to call Nango, relayauth, or relaycast setup APIs
directly. It should use `@relayfile/sdk` for workspace setup, then hand the
returned mount environment or invite payload to local processes, cloud sandboxes,
or other agents.

---

## User Journey

### 1. Create or Join

The lead agent creates a workspace when starting a new task and joins an
existing workspace when resuming. The agent should persist the `workspaceId` in
the task state if it needs resumability.

```ts
const workspace = savedWorkspaceId
  ? await setup.joinWorkspace(savedWorkspaceId, { agentName: 'lead-agent' })
  : await setup.createWorkspace({ name: 'acme-notion-room', agentName: 'lead-agent' })
```

### 2. Connect Notion

The lead agent calls `workspace.connectNotion()`. If Notion is not already
connected, the method returns a `connectLink`. The agent shows only that URL to
the user.

```ts
const notion = await workspace.connectNotion()
if (notion.connectLink) {
  await sendToUser(`Connect Notion: ${notion.connectLink}`)
}
```

The agent does not ask the user for tokens, integration IDs, Nango details, or
manual callback handling.

### 3. Wait for Readiness

After showing the link, the lead agent calls `workspace.waitForNotion()`.
Readiness means the cloud has received the provider webhook, persisted the
workspace integration row, and the relayfile provider root can be listed or is
known to be in a queued sync state.

The readiness result should distinguish:

- OAuth connected, initial sync not started.
- Initial sync queued.
- Initial sync running.
- Initial sync complete.
- Initial sync failed.

The minimum v1 behavior may resolve when OAuth is connected, but the product
flow should expose enough status to avoid a mount that looks empty without
explanation.

### 4. Mount

Once Notion is ready, the lead agent calls `workspace.mountEnv()` and passes the
result to `relayfile-mount` or an equivalent cloud sandbox launcher.

```ts
const env = workspace.mountEnv({
  localDir: '/workspace/notion',
  remotePath: '/notion',
})
```

The mount process must use:

- `RELAYFILE_BASE_URL`
- `RELAYFILE_TOKEN`
- `RELAYFILE_WORKSPACE`
- `RELAYFILE_REMOTE_PATH`
- `RELAYFILE_LOCAL_DIR`
- `RELAYFILE_MOUNT_MODE` when provided

The same env block may also include relaycast variables for agent processes:

- `RELAY_API_KEY`
- `RELAYCAST_API_KEY`
- `RELAY_BASE_URL`
- `RELAYCAST_BASE_URL`

### 5. Invite Agents

The lead agent calls `workspace.agentInvite()` for each trusted agent that
should join the room.

```ts
const invite = workspace.agentInvite({
  agentName: 'review-agent',
  scopes: ['fs:read', 'relaycast:write'],
})
```

The invite must contain enough information for the invited agent to:

- Identify the relayfile workspace.
- Access the relayfile VFS or mount process.
- Join the relaycast workspace.
- Know its intended agent name and scopes.

The invite is secret credential material. It must not be logged in full by
default.

### 6. Coordinate and Work

Invited agents use relaycast for status, handoffs, questions, and task
coordination. They use relayfile for shared files.

The successful end state is not merely "the SDK returned objects". The end
state is:

- The human connected Notion.
- The lead agent can list files under `/notion`.
- A mount process can expose the same files at a local path.
- An invited agent can join relaycast.
- The lead and invited agents can exchange at least one relaycast message.
- The invited agent can read the same mounted or VFS-backed file state.

---

## SDK Surface

The golden path can be delivered in two layers.

### Layer 1: Existing `WorkspaceHandle` Convenience Methods

These methods already belong on `WorkspaceHandle` because the handle owns the
workspace ID, relayfile URL, relayfile token, relaycast API key, and join
options.

```ts
workspace.connectNotion(options?)
workspace.waitForNotion(options?)
workspace.mountEnv(options?)
workspace.agentInvite(options?)
```

Layer 1 is sufficient for agents that launch their own mount process or have
their own agent orchestration.

### Layer 2: Optional `AgentWorkspace` Orchestrator

A later SDK can add a higher-level orchestrator that composes setup, Notion
connection, mount launch, and relaycast registration.

```ts
const room = await setup.agentWorkspace({
  name: 'acme-notion-room',
  agentName: 'lead-agent',
  integrations: ['notion'],
  mount: {
    localDir: '/workspace/notion',
    remotePath: '/notion',
  },
})

const connect = await room.connect()
if (connect.userActionRequired) {
  console.log(connect.links.notion)
}

await room.ready()
await room.mount()
await room.invite({ agentName: 'review-agent' })
```

Layer 2 is not required for v1, but the Layer 1 method names should leave room
for it.

---

## Cloud Contract

The cloud contract from `docs/sdk-setup-client.md` remains the base contract.
This spec adds product-level expectations around the response shape and status
semantics.

### Workspace Join Response

`POST /api/v1/workspaces/:workspaceId/join` should return:

```ts
interface JoinWorkspaceResponse {
  workspaceId: string
  token: string
  relayfileUrl: string
  wsUrl?: string
  relaycastApiKey: string
  relaycastBaseUrl?: string
}
```

`relaycastBaseUrl` is optional for backward compatibility, but cloud should send
it once staging and production environments need clean separation. The SDK may
default to `https://api.relaycast.dev` when omitted.

### Integration Connect Response

`POST /api/v1/workspaces/:workspaceId/integrations/connect-session` returns:

```ts
interface ConnectIntegrationResponse {
  token: string
  expiresAt: string
  connectLink: string
  connectionId: string
}
```

For Notion, `allowedIntegrations` must be restricted to `['notion']` when using
`connectNotion()`.

### Integration Status Response

The existing boolean status is enough for setup-client primitives:

```ts
interface IntegrationStatusResponse {
  ready: boolean
}
```

The golden path should evolve this into a richer, backward-compatible shape:

```ts
interface IntegrationStatusResponse {
  ready: boolean
  provider: 'notion' | string
  connectionId: string
  state:
    | 'not_connected'
    | 'oauth_connected'
    | 'sync_queued'
    | 'syncing'
    | 'ready'
    | 'failed'
  mountedPath?: string
  sync?: {
    startedAt?: string
    completedAt?: string
    itemCount?: number
    error?: string
  }
}
```

The SDK must continue to work when only `{ ready: boolean }` is returned.

---

## Relayfile Mount Contract

The mount process should be launchable from the env returned by
`workspace.mountEnv()`.

```bash
relayfile-mount \
  --base-url "$RELAYFILE_BASE_URL" \
  --token "$RELAYFILE_TOKEN" \
  --workspace "$RELAYFILE_WORKSPACE" \
  --remote-path "$RELAYFILE_REMOTE_PATH" \
  --local-dir "$RELAYFILE_LOCAL_DIR"
```

Mount readiness must be testable. A caller should be able to confirm that:

1. The mount process started.
2. The local directory exists.
3. Listing the local directory maps to the requested relayfile remote path.
4. Reads and writes either succeed or fail with permission-specific errors.
5. The process exits cleanly when stopped.

For cloud sandboxes, the same env block should be sufficient to start the mount
inside the sandbox without local-machine assumptions.

---

## Relaycast Contract

Relaycast is the coordination room for the agent workspace.

An invited agent should be able to use the invite to:

1. Join the relaycast workspace identified by `workspaceId`.
2. Register or assume the requested `agentName`.
3. Send a ready message.
4. Receive messages from the lead agent.
5. Include relayfile paths in messages as stable references.

The minimal invite fields for relaycast are:

```ts
interface RelaycastInviteFields {
  workspaceId: string
  relaycastApiKey: string
  relaycastBaseUrl: string
  agentName: string
}
```

If relaycast introduces scoped agent tokens later, `agentInvite()` should prefer
short-lived scoped tokens over raw workspace API keys.

---

## Security Model

The default path optimizes for trusted agent collaboration under one user
workflow. It is not a public share-link system.

### Credentials

`agentInvite()` may include the current relayfile JWT by default because that is
the simplest trusted-agent handoff. However:

- The invite must be documented as secret material.
- Examples must avoid printing the full invite payload.
- Callers must be able to omit the relayfile token with
  `includeRelayfileToken: false`.
- Cloud should eventually issue per-agent, scoped, short-lived relayfile tokens
  rather than reusing the lead agent token.

### Scopes

Invites should carry explicit scopes. The first supported scopes are:

- `fs:read`
- `fs:write`
- `relaycast:read`
- `relaycast:write`

The cloud join endpoint should reject unknown scopes when it begins minting
per-agent tokens from invites.

### Revocation

The v1 system can revoke access by deleting or rotating workspace credentials.
The planned system should support:

- Revoking one invited agent.
- Expiring one invite.
- Auditing invite creation and use.
- Listing active agents for a workspace.

---

## Observability

The lead agent needs simple, actionable state.

SDK and cloud should expose enough information for these user-facing messages:

- "Waiting for Notion authorization."
- "Notion connected. Sync is queued."
- "Notion sync is running."
- "Notion is ready at /notion."
- "Mount started at /workspace/notion."
- "Review agent joined relaycast."
- "Review agent can read /notion."

Cloud logs and workflow evidence should correlate by:

- `workspaceId`
- `connectionId`
- `provider`
- `agentName`
- relaycast workspace ID or API key ID
- relayfile operation correlation ID

---

## Failure Modes

The golden path must have specific errors and recovery guidance.

### User never opens Notion link

`waitForNotion()` times out with an integration timeout error. The caller can
show the same link again if the connect session is still valid or call
`connectNotion()` again to mint a new link.

### OAuth succeeds but sync is delayed

The status response should report `oauth_connected`, `sync_queued`, or
`syncing`. The mount can still start, but the agent should understand why
`/notion` is empty or partial.

### Mount cannot start

The launcher should surface whether the failure is missing binary, bad env,
bad token, unreachable relayfile URL, permission denied, or local path conflict.

### Invited agent cannot join relaycast

The invite consumer should distinguish expired invite, invalid relaycast key,
unknown workspace, agent name conflict, and network failure.

### Invited agent cannot access relayfile

The invited agent should distinguish missing relayfile token, expired token,
scope denial, workspace mismatch, and relayfile server failure.

---

## Acceptance Criteria

The implementation is complete only when a packaged consumer can prove the
entire journey end to end with mock or local servers.

### Required Unit Tests

`@relayfile/sdk` tests must cover:

- `connectNotion()` calls the connect-session endpoint with
  `allowedIntegrations: ['notion']`.
- `waitForNotion()` delegates to `waitForConnection('notion')`.
- `mountEnv()` includes relayfile mount vars and relaycast vars.
- `mountEnv()` honors `localDir`, `remotePath`, `mode`, and
  `relaycastBaseUrl`.
- `agentInvite()` includes workspace ID, cloud URL, relayfile URL, relaycast
  credentials, agent name, scopes, and relayfile token by default.
- `agentInvite({ includeRelayfileToken: false })` omits the relayfile token.
- `agentInvite()` does not mutate the handle's original scopes.

`../cloud` tests must cover:

- Notion connect session creation from a relayfile JWT whose workspace claim
  matches the route.
- Status polling for Notion before and after webhook persistence.
- Rich status fields when sync state is available.
- Join response includes relaycast credentials and, when configured,
  `relaycastBaseUrl`.
- Unauthorized, wrong-workspace, unknown-provider, and expired-token paths.

`../relaycast` tests must cover:

- Joining with the relaycast credentials returned by cloud.
- Registering the requested agent name.
- Sending and receiving a message between lead and invited agents.
- Rejecting invalid relaycast credentials.

`relayfile-mount` tests must cover:

- Starting from `workspace.mountEnv()`.
- Mounting the requested `remotePath` at `localDir`.
- Listing a seeded Notion file through the mount.
- Permission-denied behavior for read-only invited agents.
- Clean shutdown.

### Required Packaged E2E

Create a temporary consumer project that installs packed local artifacts rather
than importing source files. The E2E must run the following flow:

1. Start mock or local cloud, relayfile, and relaycast servers.
2. Create a workspace with `RelayfileSetup`.
3. Call `connectNotion()` and assert the cloud received only Notion as the
   allowed integration.
4. Simulate the Notion webhook.
5. Call `waitForNotion()` and assert it reaches readiness.
6. Seed the relayfile server with at least one file under `/notion`.
7. Call `mountEnv()` and start `relayfile-mount` or a deterministic mount
   harness from those variables.
8. Read the seeded file through the mount or mount harness.
9. Call `agentInvite({ agentName: 'review-agent' })`.
10. Start an invited-agent consumer from the invite.
11. Have the invited agent join relaycast.
12. Send a ready message from invited agent to lead agent.
13. Have the invited agent read the same `/notion` file through relayfile or the
    mount harness.
14. Shut down mount, relaycast connection, and servers cleanly.

The E2E must assert concrete HTTP paths, request bodies, response fields,
headers, relaycast messages, and filesystem observations. It is not sufficient
to assert that promises resolved.

### Manual Smoke Test

A human should be able to run one script locally:

```bash
npm run demo:agent-workspace --workspace=packages/sdk/typescript
```

The script prints a Notion connect link, waits for readiness against in-process
mocks, mounts the workspace through the deterministic harness, reads a seeded
`/notion` file from the mount, creates an invited agent with read-only scope,
reads the same file through the invited harness, and asserts that a write from
the read-only harness is denied. Relaycast message exchange is verified by
`test:e2e:golden-path`, not the demo.

---

## Implementation Plan

All v1 phases are complete as of SDK v0.6.0. The packaged E2E passes end-to-end.
Evidence: `docs/evidence/agent-workspace-golden-path-e2e.log`.

### Phase 1: Spec Lock — **Complete**

Naming resolved in `docs/agent-workspace-golden-path-acceptance.md §1`:
`agentInvite`, `mountEnv`, SDK-defaulted `relaycastBaseUrl`, and OAuth-connected
readiness are locked.

### Phase 2: SDK Completion — **Complete**

`WorkspaceHandle.connectNotion()`, `waitForNotion()`, `mountEnv()`, and
`agentInvite()` are implemented in `packages/sdk/typescript/src/setup.ts` with
unit tests in `setup.test.ts` and packaged E2E coverage.

### Phase 3: Cloud Readiness — **Mocked in E2E; real cloud changes separate**

The mock cloud server in `scripts/agent-workspace-mocks.mjs` satisfies the
acceptance contract. Real cloud changes (`relaycastBaseUrl` in join response,
richer integration status fields) are tracked in the cloud repo.

### Phase 4: Relaycast Invite Consumption — **Complete**

The packaged E2E proves a full relaycast message exchange (lead → invited →
lead ACK) using the `AgentWorkspaceInvite` payload.

### Phase 5: Mount Harness — **Complete**

`packages/sdk/typescript/src/mount-harness.ts` provides a deterministic harness
that mirrors remote paths to a local directory and enforces scope-based
read-only access without requiring host FUSE.

### Phase 6: Demo Script — **Complete**

`packages/sdk/typescript/scripts/agent-workspace-demo.mjs` runs the golden path
against in-process mocks and is the entry point for `npm run demo:agent-workspace`.

---

## Open Questions

These were open at spec time. V1 decisions are noted inline; see
`docs/agent-workspace-golden-path-acceptance.md §1` for full context.

1. ~~Should `waitForNotion()` resolve at OAuth connection, initial sync start, or
   initial sync completion?~~ **Closed (§1.2):** Resolves at OAuth connection
   (`ready: true`). Sync state fields may appear in the status response but
   `waitForNotion()` ignores them in v1.

2. Should cloud mint per-agent relayfile JWTs directly from `agentInvite()`, or
   should invited agents call `joinWorkspace()` themselves with a separate
   access token? **Open — post-v1.** V1 reuses the lead agent's token.
   Tracked as Q2 in the acceptance doc.

3. Should relaycast expose short-lived scoped agent tokens so invites do not
   contain workspace API keys? **Open — post-v1.**

4. Should Notion always mount at `/notion`, or should provider roots be
   configurable per workspace? **Open — post-v1.** V1 defaults to `/notion`.

5. ~~Should the mount process be launched by the SDK, returned as env only, or
   offered as both `mountEnv()` and `launchMount()`?~~ **Closed (§1.1):**
   `mountEnv()` returns env vars only. The SDK does not launch the mount
   process directly in v1.

6. What is the minimum useful Notion file projection for v1: page markdown,
   database rows, attachments, comments, or all of the above? **Open.**
   V1 readiness criterion only requires OAuth connection, not content quality.

---

## Definition of Done

This feature is done when a single agent can truthfully tell a human:

"Open this Notion link. Once you approve it, I will mount your workspace here
and bring in the other agents."

And the implementation proves that statement by connecting Notion, mounting the
relayfile workspace, inviting another agent into relaycast, exchanging a
message, and reading the same filesystem content from both agents in an
end-to-end test.
