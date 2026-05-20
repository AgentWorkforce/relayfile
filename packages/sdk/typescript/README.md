# @relayfile/sdk

TypeScript SDK for relayfile — real-time filesystem for humans and agents.

## Install

```bash
npm install @relayfile/sdk
```

## Agent Workspace — Golden Path

The recommended way to use `@relayfile/sdk` in an agent workflow is through
`RelayfileSetup`. This handles workspace creation, Notion authorization,
environment variable generation for `relayfile-mount`, and agent invites.

```ts
import { RelayfileSetup } from '@relayfile/sdk/cli'

const setup = await RelayfileSetup.login({
  onLoginUrl: (url) => console.log(`Sign in to Relayfile Cloud: ${url}`),
})

// Create or resume a workspace
const workspace = await setup.createWorkspace({
  name: 'notion-research-room',
  agentName: 'lead-agent',
})

// Connect Notion — returns a link for the human to open
const notion = await workspace.connectNotion()
if (notion.connectLink) {
  console.log(`Connect Notion: ${notion.connectLink}`)
  await workspace.waitForNotion({ timeoutMs: 5 * 60_000 })
}

// Env vars for relayfile-mount (local dir or cloud sandbox)
const mountEnv = workspace.mountEnv({
  localDir: '/workspace/notion',
  remotePath: '/notion',
})

// Secret invite payload for a trusted co-worker agent — same scopes as this
// workspace's token (no per-invite downscoping).
const invite = workspace.agentInvite({ agentName: 'review-agent' })

// To grant a strictly narrower set of scopes, mint a fresh JWT via the cloud
// API. The cloud rejects requests that exceed the calling token's grant.
const scopedInvite = await workspace.agentInviteScoped({
  agentName: 'notion-summarizer',
  scopes: ['relayfile:fs:read:/notion/pages/*'],
})
// Never log invites — they contain credential material
```

For the full end-to-end journey, failure modes, and acceptance criteria see
[`docs/agent-workspace-golden-path.md`](../../../docs/agent-workspace-golden-path.md).

Run the demo locally:

```bash
npm run demo:agent-workspace --workspace=packages/sdk/typescript
```

The demo defaults to in-process mock cloud and relayfile servers, seeds a `/notion`
file, mounts it through the deterministic harness, and proves read-only invited-agent
behavior without requiring real Notion, Relaycast, or cloud credentials. For a real
deployment, use `RelayfileSetup.login()` to send the human one Cloud sign-in URL,
or use `RelayfileSetup.fromCloudTokens()` with previously persisted Cloud tokens.
You can still pass `accessToken` directly to `new RelayfileSetup()` in CI or
advanced hosts that already provide a valid Cloud bearer token. After Cloud auth,
complete the Notion OAuth flow as described in
[`docs/agent-workspace-golden-path.md`](../../../docs/agent-workspace-golden-path.md).

The default `@relayfile/sdk` entry point is safe for Worker-style bundles and
does not statically import the CLI-only mount launcher or local browser login
helpers. Import interactive login and local mount launcher utilities from
`@relayfile/sdk/cli`, `@relayfile/sdk/cloud-login`, or
`@relayfile/sdk/mount-launcher`.

---

## Low-Level Client Example

```ts
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({
  baseUrl: "https://api.relayfile.com",
  token: process.env.RELAYFILE_TOKEN ?? "",
  changeLog: {
    retentionMs: 7 * 24 * 60 * 60 * 1000,
    maxEntries: 10_000
  }
});

const workspaceId = "workspace_123";

const tree = await client.listTree(workspaceId, {
  path: "/",
  depth: 2
});

console.log(tree.entries.map((entry) => entry.path));

const file = await client.readFile(workspaceId, "/notes/todo.md");

console.log(file.content);

await client.writeFile({
  workspaceId,
  path: "/notes/todo.md",
  baseRevision: file.revision,
  content: `${file.content}\n- Follow up with SDK publish`,
  contentType: "text/markdown"
});
```

Use a relayfile JWT whose claims include `workspace_id`, `agent_name`, and `aud: ["relayfile"]`. The SDK adds `X-Correlation-Id` automatically for API calls.

The optional `changeLog` block configures the SDK's local per-workspace retained-change mirror used by `subscribe()`, `open({ replayOnStart })`, and `getResourceAtEvent(eventId)`. Durable retention still lives on the Relayfile backend.

## Full Docs

Full documentation is available in the [relayfile docs](https://github.com/AgentWorkforce/relayfile/tree/main/docs).
