# Custom Digest Functions

Custom digest functions let a workspace add its own digest section beside the first-party provider sections. Use them when a team has workspace-specific rules for "what changed" that should be computed before an agent starts work.

The M1 flow is:

1. Write a TypeScript or JavaScript `digest(ctx)` function.
2. Test it locally with a fixture.
3. Deploy the source bundle to Agent Relay Cloud with the CLI or SDK.
4. Let the hosted digest runtime invoke the function during digest generation.

## Function Contract

A digest function exports either `digest(ctx)` or a default handler. It receives a `DigestContext` and returns a `DigestSection` or `null`.

```ts
// digests/eng-roadmap.ts
import type { DigestHandler } from "@relayfile/sdk"

export const digest: DigestHandler = async (ctx) => {
  const events = await ctx.changeEvents({
    providers: ["notion", "github", "linear"],
    paths: [
      "/notion/databases/eng-roadmap/*",
      "/github/repos/acme/app/pulls/*",
      "/linear/issues/*",
    ],
  })

  return {
    provider: "Eng Roadmap",
    bullets: events.map((event) => ({
      text: event.summary?.title ?? event.resource?.id ?? event.id,
      canonicalPath: event.resource?.path ?? event.path,
    })),
  }
}
```

`ctx.changeEvents()` is the only data access API in M1. The runtime filters events by workspace, provider, and path scope before returning them to the function.

The returned section renders as:

```text
## Eng Roadmap

- Pull request #42 moved to review - [/github/repos/acme/app/pulls/42]
- Roadmap page updated - [/notion/databases/eng-roadmap/rollout]
```

## Local Test Loop

Use `relayfile digest function test` before deploying:

```bash
relayfile digest function test ./digests/eng-roadmap.ts --fixture ./events.json
```

The fixture is a JSON array of change events:

```json
[
  {
    "id": "evt_1",
    "provider": "notion",
    "path": "/notion/databases/eng-roadmap/rollout",
    "resource": {
      "id": "rollout",
      "path": "/notion/databases/eng-roadmap/rollout"
    },
    "summary": {
      "title": "Roadmap page updated"
    }
  }
]
```

Local testing does not contact the control plane. JavaScript files run with `node`; TypeScript files require a local `tsx` binary, usually from `npm install` in this repo or in the TypeScript SDK workspace.

Network APIs such as `fetch`, `XMLHttpRequest`, `WebSocket`, and `EventSource` are blocked. Custom digest functions are pure over `DigestContext`.

## Deploy With The CLI

Authenticate the CLI with Agent Relay Cloud, select or pass a workspace, then deploy:

```bash
relayfile digest function deploy ./digests/eng-roadmap.ts \
  --workspace my-agent \
  --name eng-roadmap
```

The CLI uploads the source bundle to the workspace digest-function endpoint. Cloud compiles, signs, stores, and distributes the function for digest generation.

Useful commands:

```bash
relayfile digest function list --workspace my-agent
relayfile digest function show eng-roadmap --workspace my-agent
relayfile digest function logs eng-roadmap --workspace my-agent
relayfile digest function disable eng-roadmap --workspace my-agent
```

Use JSON output when automation needs the function id or sha:

```bash
relayfile digest function deploy ./digests/eng-roadmap.ts \
  --workspace my-agent \
  --name eng-roadmap \
  --json
```

## Deploy With The SDK

The TypeScript SDK exposes the same workspace Cloud API. This is the preferred path for CI or agent-owned setup flows that already have a Cloud access token.

```ts
import {
  RelayfileSetup,
  digestFunctionSourceFromText,
} from "@relayfile/sdk"
import { promises as fs } from "node:fs"

const setup = new RelayfileSetup({ accessToken: process.env.RELAYFILE_CLOUD_TOKEN })
const workspace = await setup.joinWorkspace("my-agent")

const source = digestFunctionSourceFromText({
  path: "digests/eng-roadmap.ts",
  contents: await fs.readFile("digests/eng-roadmap.ts", "utf8"),
})

const deployed = await workspace.deployDigestFunction({
  slug: "eng-roadmap",
  displayName: "Eng Roadmap",
  source,
})

console.log(deployed.digestFunctionId, deployed.sha256)
```

Read and operate the deployed function through the same handle:

```ts
const functions = await workspace.listDigestFunctions({ limit: 20 })
const detail = await workspace.getDigestFunction(deployed.digestFunctionId)
const logs = await workspace.getDigestFunctionLogs(deployed.digestFunctionId, {
  since: new Date(Date.now() - 60 * 60 * 1000),
  limit: 50,
})

await workspace.disableDigestFunction(deployed.digestFunctionId)
```

## Runtime Behavior

- Functions are workspace-scoped. A function deployed to one workspace cannot run in another workspace.
- A failed function does not fail the whole digest. Relayfile renders the rest of the digest and records a warning for the custom section.
- Timeouts and memory failures are isolated per function.
- Custom sections render after first-party provider sections.
- Logs are retrieved through the Cloud digest-function logs API, surfaced by both the CLI and SDK.

## M1 Limits

- Runtime metadata is `node20`, but deployed code runs inside the hosted signed digest-function runtime.
- Source bundles are TypeScript or JavaScript only.
- The only host bridge is `ctx.changeEvents(filter)`.
- No network calls, filesystem writes, secrets access, timers for side effects, or cross-workspace reads.
- Local `.ts` testing requires `tsx`; use `.js` fixtures when running Go-only CI jobs that do not install Node dependencies.

## When To Use This

Use custom digest functions for summary logic that is stable enough to run before the agent prompt, such as:

- Grouping changed Notion pages, GitHub PRs, and Linear issues into one roadmap section.
- Filtering noisy provider events to only customer-owned paths.
- Emitting digest bullets that match a team's internal labels or release process.

Keep one-off investigation logic in the agent prompt. Move repeated, workspace-specific digest logic into a custom digest function.
