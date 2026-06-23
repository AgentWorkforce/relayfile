<p align="center">
  <img src="assets/banner.png" alt="Relayfile — reactive agents without the plumbing" width="900">
</p>

**Reactive agents without the plumbing.**

A Linear issue lands in Triage. `relayfile listen` fires. Your agent reads `/linear/issues/ENG-123.md` — already synced, no API call — scans open triage items, finds an available assignee, and patches the issue by writing the file back. Event arrives, context is there, agent acts.

That's the loop relayfile closes. Provider webhooks are normalized and delivered as file events. Your SaaS stack is a live filesystem the agent reads with `cat` and writes with file saves. No ingestion code. No per-provider API clients in the agent. No context window full of tool schemas.

## The reactive loop

```bash
# Issue lands in Triage → agent reads context, assigns it, patches back
relayfile listen --path "/linear/issues/by-state/triage/**" --event file.created \
  --run "claude --print 'New issue at {{path}}. Read the file, check open triage items and available assignees, assign it.'"
```

When the event fires, `/linear/issues/ENG-123.md` is already there. So is `/linear/issues/by-state/triage/` (all open triage items), `/linear/users/` (who's available), `/linear/cycles/` (current sprint). The agent reads what it needs with `cat` and writes the result back — a file save, not an API call.

Filter as deeply as the provider tree allows:

```bash
# PRs labeled needs-review on a specific repo
relayfile listen --path "/github/repos/acme/api/pulls/by-label/needs-review/**" --event file.created \
  --run "claude --print 'New PR at {{path}}. Read the diff context and write a review summary.'"

# Deal moved to a new HubSpot stage
relayfile listen --path "/hubspot/deals/**" --event file.updated \
  --run "claude --print 'Deal updated at {{path}}. Draft a follow-up for the new stage.'"

# New Shortcut story under a specific epic
relayfile listen --path "/shortcut/stories/by-epic/payments/**" --event file.created \
  --run "claude --print 'New payments story at {{path}}. Suggest an implementation plan.'"

# Meeting notes → extract action items
relayfile listen --provider granola --event file.created \
  --run "claude --print 'New notes at {{path}}. Extract action items and owners.'"
```

Run in the foreground, detach with `--background`, or use `relayfile dev` for a zero-friction entry point that checks your auth and integration status first.

## Read, write, coordinate

```bash
$ ls mount/
github  linear  notion  slack

$ cat mount/linear/issues/AGE-12.json
{ "identifier": "AGE-12", "title": "Fix login bug", "state": "Todo", ... }

$ echo '{"state":"In Review","description":"PR #42"}' > mount/linear/issues/AGE-12.json
# ↑ PATCHes back to Linear

$ grep -l '"state":"Todo"' mount/linear/issues/*.json
```

Write-back is a file save on the same path the agent read from. No separate write API, no schema to learn.

Multiple agents share the same mount. When agent A writes, agent B sees it within a second — no push, no merge. Reviewer agents watch implementer agents in real time. **The filesystem is the coordination protocol.**

Every provider tree has alias views for granular reads: `by-state/`, `by-label/`, `by-epic/`, `by-name/`, `by-id/`. Run `relayfile tree / --depth 3` to explore what's available for your connected providers.

## vs. MCP and webhook forwarders

**vs. MCP.** MCP gives the agent a typed tool surface: `linear.search_issues(query)` returns what the API ranks, and each connected server loads schemas into the context window. Relayfile gives `ls /linear/issues/by-state/triage/` — exhaustive enumeration, zero schema overhead. The two compose: relayfile for reads and ambient context, MCP for typed writes that need server-side validation.

**vs. webhook forwarders** (Hookdeck, Svix). A forwarder delivers the event. It doesn't deliver the context. The agent still has to make API calls to understand what to do. Relayfile delivers both: the event fires, and the files were already there before it arrived.

## Cloud agents (SDK)

For agents running in a sandbox — E2B, Daytona, an ephemeral container, your own runtime — the TypeScript SDK mounts the workspace as local files in one call. The agent gets `ls`, `cat`, `grep` against live provider data; no manual setup, no separate auth flow.

```ts
import { RelayfileSetup } from "@relayfile/sdk"

const setup = new RelayfileSetup({ accessToken })

// Mount and wait for the provider to be ready before the agent starts.
const handle = await setup.ensureMountedWorkspace({
  workspaceId: "rw_…",
  provider: "notion",
  localDir: "/workspace",
})

// /workspace is now a live, writable mount of the workspace.
// The agent reads with cat/ls/grep and writes back by saving files.
await handle.stop()
```

`ensureMountedWorkspace` throws `ProviderNotConnectedError` if the provider isn't connected and `ProviderNotReadyError` if the initial sync hasn't finished. Pass `verifyProvider: false` to skip the probe. Full details in [docs/guides/post-auth-mount-session.md](docs/guides/post-auth-mount-session.md).

## Quick start

Fastest path — hosted, zero infrastructure:

```bash
npx relayfile setup --provider linear --workspace my-agent --local-dir ./mount
```

Local OSS:

```bash
cd docker && docker compose up --build
TOKEN="$(docker compose logs seed | awk '/token/ {print $NF}' | tail -1)"
RELAYFILE_TOKEN="$TOKEN" go run ./cmd/relayfile-mount \
  --base-url http://localhost:9090 --workspace ws_demo --local-dir ./mount
```

See [getting-started.md](docs/guides/getting-started.md) for the full local dev path and CLI reference.

## Ecosystem

This repo is the file server and mount layer.

- [relayfile-adapters](https://github.com/AgentWorkforce/relayfile-adapters): webhook normalization, path mapping, writeback behavior per provider
- [relayfile-providers](https://github.com/AgentWorkforce/relayfile-providers): provider auth, API proxy, webhook subscriptions, connection health

Hosted Agent Relay runs all of this for you. For end-to-end self-hosting, run relayfile plus the adapter and provider repos for the integrations you need.

> **Want this running headlessly for your whole team** — turning issues into reviewed PRs automatically?
> See [AgentWorkforce/factory](https://github.com/AgentWorkforce/factory).

## Docs

- [Getting started](docs/guides/getting-started.md)
- [Cloud integration](docs/guides/cloud-integration.md)
- [Pricing](docs/guides/pricing.md)
- [API reference](docs/api-reference.md)
- [OpenAPI spec](openapi/relayfile-v1.openapi.yaml)
