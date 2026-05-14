# Custom Digest Functions — Implementation Spec

**Status:** Proposed
**Affects:** `AgentWorkforce/relayfile` (`internal/digest`, `internal/wasmrun` (new), `cmd/relayfile-cli`, `packages/sdk/typescript`), `AgentWorkforce/cloud` (workspace function storage, compile/deploy API, admin scope), `AgentWorkforce/relayfile-adapters` only through the base digest contract dependency
**Depends on:** `workspace-primitives-spec.md` work items 1 + 2 (base digest pipeline + adapter `digest()` contract)
**Pairs with:** `proactive-runtime-contract.md`, `LAYOUT.md`

---

## Repo Routing for Implementers

This follow-on spec is explicitly cross-repo. Do not assign it to a relayfile-only agent.

| Repo | Owns in this spec | Local root |
|---|---|---|
| `AgentWorkforce/relayfile` | Local CLI commands, SDK types, mount-daemon digest invocation, `internal/wasmrun`, sandbox execution, deterministic host bridge, local `digest function test`, and daemon-side warnings. | `../relayfile` |
| `AgentWorkforce/cloud` | Deploy/list/show/disable/logs API backing the CLI, TypeScript-to-WASM compilation service, function storage tables, workspace-admin authorization scope, module signing, and daemon distribution channel. | `../cloud` |
| `AgentWorkforce/relayfile-adapters` | No direct custom-function work beyond the base `DigestHandler` contract from `workspace-primitives-spec.md` work item 2. | `../relayfile-adapters` |
| `AgentWorkforce/skills` | Follow-up `daily-digest` skill docs after M1 ships. | `../skills` |
| `AgentWorkforce/relay` | Follow-up blog/tutorial copy after M1 ships. | `../relay` |

**Worktree and PR rule:** for every repo an agent touches, create or use a dedicated worktree for that repo and open a separate PR from that repo. Use matching branch names where possible, and make every PR body cross-link the dependent PRs in the other repos. Do not mix changes for multiple repos into one working tree, one branch, or one PR.

**PR split to give agents:**

1. `relayfile-adapters` and `relayfile`: finish `workspace-primitives-spec.md` work items 1 + 2 first. This spec is blocked until those PRs merge or are available as dependency branches.
2. `cloud`: implement storage, compile/deploy APIs, signing, admin scope, and function distribution.
3. `relayfile`: implement CLI verbs, local test runner, `internal/wasmrun`, daemon pull/cache/invoke behavior, and digest renderer warning integration.
4. `relayfile` + `cloud`: run the cross-repo integration proof that deploys a fixture function through cloud, pulls it into the daemon, and renders a digest section.
5. `skills` / `relay`: update docs only after the runtime behavior is proven.

**Ricky cloud-slice instruction:** when running PR split 2 from the `AgentWorkforce/cloud` worktree, include this instruction verbatim in the Ricky kickoff:

> Implement only the `AgentWorkforce/cloud` slice of `custom-digest-functions-spec.md`: workspace function storage, TypeScript-to-WASM compile/deploy APIs, deploy/list/show/disable/logs endpoints, workspace-admin authorization scope, module signing, and daemon distribution. Do not edit `relayfile`, `relayfile-adapters`, `skills`, or `relay`; reference those repos only as dependency context and produce a separate cloud PR.

If a single agent is coordinating the whole feature, tell it to preserve that same per-repo worktree and PR split while coordinating evidence across the dependency branches. The acceptance criteria should cite evidence from both `relayfile` and `cloud`.

## Problem

The base digest pipeline (see `workspace-primitives-spec.md` work items 1 + 2) lets first-party adapter authors contribute per-provider activity bullets to `digests/yesterday.md`. That covers the generic case — "what changed in Linear yesterday" — but every team that builds on relayfile has organization-specific rollups they'd want a digest line for:

- Rollup of a custom Notion database (`OKRs`, `Customer Health`, `Eng Roadmap`) by a property the first-party adapter doesn't know about.
- Salesforce opportunities crossing a stage threshold last week, scoped to the requesting agent's account list.
- GitHub PRs that touched a particular CODEOWNERS-defined surface.

Today these have to live in agent prompts — the agent runs the query at inference time, every time. That defeats the whole point of pre-computing the digest. We want **customers' own engineers** to write the digest logic once, deploy it, and have it run alongside the first-party adapter digests for free at the agent's perspective.

The function has to be **pre-compiled and sandboxed** because:

- It runs in the mount daemon process (or close to it) — arbitrary customer code can't have raw FS / network access.
- Digest generation is on a hot path (every 30s for `today.md`, daily for `yesterday.md`); cold-start interpreters are too slow.
- Output must be deterministic over the same change-event window so the digest is byte-stable across rebuilds.

## Goal

A customer engineer writes a TypeScript function, pushes it via `relayfile digest function deploy`, and the function:

1. Compiles to WASM at deploy time, cached by content hash.
2. Runs in a sandboxed runtime inside the mount daemon, alongside first-party adapter `digest()` calls.
3. Reads from the same `DigestContext` API adapters use, plus a customer-defined namespace for org-specific data.
4. Emits a `DigestSection` that gets merged into `<mount>/digests/yesterday.md` and `<mount>/digests/today.md` under a customer-specified heading.
5. Is scoped to the workspace it was deployed in. Other workspaces' digests are unaffected.

```ts
// customer-side: digests/eng-roadmap.ts
import type { DigestHandler } from '@relayfile/sdk';

export const digest: DigestHandler = async (ctx) => {
  const events = await ctx.changeEvents({
    paths: ['/notion/databases/eng-roadmap/*'],
  });

  const flipped = events.filter(
    (e) => e.summary?.fieldsChanged?.includes('status'),
  );

  if (flipped.length === 0) return null;

  return {
    provider: 'Eng Roadmap',
    bullets: flipped.map((e) => ({
      text: `${e.summary?.title ?? e.resource.id} moved to ${e.summary?.status}`,
      canonicalPath: e.resource.path,
    })),
  };
};
```

```bash
relayfile digest function deploy ./digests/eng-roadmap.ts
# uploads, compiles to WASM, registers under workspace rw_xxxxxxxx
```

## Non-goals

- Arbitrary side-effects (network, FS writes, secrets access). Customer digest functions are pure over `DigestContext` only.
- Cross-workspace digest functions. A function deployed to workspace A cannot be invoked from workspace B.
- Streaming or partial output. A function returns a complete `DigestSection | null`.
- Real-time function development against production digests. Functions are tested locally first; deploy is a deliberate step.
- Multiple language frontends in M1 — TypeScript only (compiles via QuickJS-to-WASM).

---

## Architecture

```
                ┌────────────────────────────────┐
                │  relayfile digest function     │
                │  deploy ./eng-roadmap.ts       │
                └────────────────────────────────┘
                                │
                                ▼
                ┌────────────────────────────────┐
                │  control plane (cloud)         │
                │  • compile TS → JS → WASM      │
                │  • content-hash, sign          │
                │  • store per workspace_id      │
                └────────────────────────────────┘
                                │  pull via long-poll / WS
                                ▼
        ┌──────────────────────────────────────────────┐
        │  mount daemon                                │
        │                                              │
        │  digest scheduler                            │
        │      │                                       │
        │      ├──► first-party adapter digest()       │
        │      │      (Go, in-process)                 │
        │      │                                       │
        │      └──► wasmrun (wazero sandbox)           │
        │             customer digest() WASM modules   │
        │             • timeout: 5s                    │
        │             • memory: 64MB                   │
        │             • deterministic clock            │
        │             • no host syscalls except        │
        │               ctx.changeEvents() bridge      │
        │                                              │
        │  renderer: merge all DigestSections          │
        │            → digests/yesterday.md            │
        └──────────────────────────────────────────────┘
```

## Compilation pipeline

**Primary repo:** `AgentWorkforce/cloud`.
**Supporting repo:** `AgentWorkforce/relayfile` for the CLI request/response contract and local `digest function test`.

**Source:** TypeScript (`.ts`) or JavaScript (`.js`) implementing the `DigestHandler` interface.

**Stage 1 — bundle.** `esbuild` (already in the relayfile-cli toolchain via Composio adapter pipeline) compiles + tree-shakes to a single CommonJS module. Strip imports of `@relayfile/sdk` types (they're erased at runtime).

**Stage 2 — embed.** Bundle the JS into a QuickJS engine pre-compiled to WASM. We ship a stable QuickJS-WASM blob (`internal/wasmrun/quickjs.wasm`, ~600 KB) and the customer module is loaded into it at instantiation.

**Stage 3 — content hash.** SHA-256 of `(bundled JS) + (sdk-version)`. Re-deploying identical source is a no-op.

**Stage 4 — sign.** Control plane signs the module with the workspace's deployment key (already used for writeback ack tokens).

**Stage 5 — store.** Module + manifest stored under `workspace_digest_functions(workspace_id, function_id, content_hash, source, …)`.

Alternative considered: ship per-customer WASM directly (no JS engine). Rejected for M1 — requires a TS-to-WASM frontend (AssemblyScript) that's awkward for the kind of dataflow customers will actually write. QuickJS-in-WASM gives us JS ergonomics at a fixed ~3× startup cost over native, which is fine for a 30s-coalesced job.

## Runtime: `internal/wasmrun`

**Primary repo:** `AgentWorkforce/relayfile`.
**Supporting repo:** `AgentWorkforce/cloud` only for the signed module envelope and distribution protocol consumed by the daemon.

**Engine:** [wazero](https://github.com/tetratelabs/wazero) — pure-Go, zero CGO, AOT-cacheable. Adds to `go.mod` as a single dependency; no toolchain change.

**Per-invocation contract:**

| Resource | Limit |
|---|---|
| CPU wall time | 5s hard, 1s soft warning |
| Memory | 64 MB |
| Module instantiation | once per digest cycle, reused across providers if applicable |
| Host syscalls | None directly. Bridged calls listed below. |

**Host functions exposed to the WASM module (via wazero import object):**

- `host_change_events(filter_json) -> events_json` — backing for `ctx.changeEvents()`. Filtered server-side by the daemon; module only sees events for paths it's scoped to.
- `host_log(level, message)` — appears in daemon logs tagged with the function id. Not in the digest output.
- `host_now_ms() -> int64` — returns `window.to` epoch ms (deterministic; same value across replays of the same window).

That's it. No `fetch`, no `fs`, no `crypto.randomUUID()` (replaced by a deterministic counter derived from `eventId`).

**Determinism enforcement:**

- `Date.now()` and `performance.now()` patched in the QuickJS bootstrap to return `host_now_ms()`.
- `Math.random()` replaced with a seeded PRNG keyed by `(workspace_id, window.from)`.
- `JSON.stringify` is fine; QuickJS is single-threaded.

## API surface seen by the customer module

**Primary repo:** `AgentWorkforce/relayfile`.
**Supporting repo:** `AgentWorkforce/relayfile-adapters` only if the base `DigestContext` type remains sourced from adapter-core instead of being re-exported from the relayfile SDK.

Same `DigestContext`, `DigestBullet`, `DigestSection` types defined in `workspace-primitives-spec.md` work item 2 — re-exported from `@relayfile/sdk` so customer code imports them naturally.

Additional context fields available to customer modules but not first-party adapters:

```ts
interface DigestContext {
  // … existing fields from work item 2
  readonly workspaceId: string;
  readonly functionId: string;   // for self-identification in logs
}
```

The `provider` field on `DigestSection` is customer-controlled — they pick the heading. Sections from custom functions render under their chosen name in the digest body, sorted after first-party adapter sections (alphabetical within each group).

## Storage & lifecycle

**Primary repo:** `AgentWorkforce/cloud`.
**Supporting repo:** `AgentWorkforce/relayfile` for daemon cache semantics and status/log display.

**Control plane tables** (new migration on the cloud-side `workspace_digest_functions` table):

```sql
create table workspace_digest_functions (
  function_id     text primary key,
  workspace_id    text not null references workspaces(id),
  name            text not null,
  content_hash    text not null,
  source_bytes    bytea not null,        -- raw TS, for audit and re-compile
  wasm_bytes      bytea not null,        -- pre-compiled module
  manifest_json   jsonb not null,        -- {entryPoint, sdkVersion, …}
  deployed_at     timestamptz not null,
  deployed_by     text not null,         -- agent_name or user email
  status          text not null,         -- "active" | "disabled" | "errored"
  unique (workspace_id, name)
);
```

**Lifecycle:**

- `deploy` → compile, store, immediately fetchable by daemon on next sync tick.
- `disable` → status flips to `disabled`, daemon stops invoking it on next reload.
- Daemon caches compiled modules under `~/.relayfile/cache/wasm/<content_hash>.wasm`. Eviction by LRU.

## CLI

**Primary repo:** `AgentWorkforce/relayfile`.
**Supporting repo:** `AgentWorkforce/cloud` for every command except `digest function test`, which must run locally without cloud.

```bash
relayfile digest function deploy <path>           # compile + upload
relayfile digest function list                    # workspace-scoped
relayfile digest function show <name>             # source, manifest, last run
relayfile digest function disable <name>
relayfile digest function logs <name> [--tail]    # daemon logs from this function
relayfile digest function test <path> [--fixture <events.json>]
                                                  # run locally against fixtures,
                                                  # no upload, no workspace touched
```

`test` is the developer's main loop: write the function, run it against a fixture event stream, see the rendered Markdown section before deploying.

## Acceptance criteria

- [ ] `relayfile digest function deploy ./eng-roadmap.ts` succeeds and the function appears in `relayfile digest function list`.
- [ ] Next digest tick includes a `## Eng Roadmap` section (or whatever heading the function returns) in `digests/today.md` / `yesterday.md`.
- [ ] Determinism: deploying the same source twice produces the same `content_hash` and the same `DigestSection` output on identical fixture data.
- [ ] Timeout: a function that loops forever is killed at 5s, its section is omitted, a warning lands in the digest header `warnings: ["eng-roadmap: timeout"]`.
- [ ] Memory: a function that allocates >64 MB is killed with `oom`, same warning treatment.
- [ ] Isolation: a function that tries to `fetch('https://…')` fails with a clear error; no actual network call is made.
- [ ] First-party adapter digests are unaffected by customer function failures.
- [ ] `relayfile digest function test ./eng-roadmap.ts --fixture events.json` runs locally without contacting the control plane.

## Trust model

- Customer functions are sandboxed; the only data they see is the change-event slice scoped to their workspace, filtered to the paths their `DigestContext` provides.
- Functions can't read secrets, OAuth tokens, or other workspace data not surfaced via `DigestContext`.
- Deployment requires a workspace-admin scope (`relayauth:digest-function:manage:*`, new scope).
- Source code is stored alongside the compiled WASM for audit.

## Quotas

Per workspace, M1 defaults:

- Max active functions: 32.
- Max compiled WASM size per function: 4 MB.
- Max wall-time per digest cycle (sum over all functions): 30s. Functions are run sequentially in name-sorted order until the budget exhausts; later functions are skipped with a warning.

## Risks

- **Sandbox escape.** wazero has a clean security record but the QuickJS layer adds attack surface. Mitigation: standard wazero memory limits, no syscalls exposed beyond the listed host functions, and fuzz testing the bridge layer before launch.
- **Slow customer code.** A 5s timeout is generous for digest work but a customer function that habitually hits it will starve later functions. Mitigation: per-function CPU budget tracked and surfaced in `function show`; auto-disable after N consecutive timeouts.
- **Non-determinism leak.** Any path that exposes wall-clock time, randomness, or unstable ordering breaks digest byte-stability. Mitigation: golden-file tests over the host-function bridge; reject deploys whose test-run output diverges between two cold starts.
- **Source vs binary drift.** Storing source alongside WASM lets us rebuild if the runtime changes. Required because WASM binaries are not forward-portable across QuickJS version bumps.
- **Customer support burden.** Customer-authored code that fails will generate support load. Mitigation: structured error surfacing in `function logs` and inline `warnings` in the digest header.

## Sequencing

This spec is **blocked on `workspace-primitives-spec.md` work items 1 + 2.** Without the base digest pipeline and adapter `digest()` contract, there's nothing for customer functions to plug into.

Once those land, custom functions land in three milestones:

1. **M1 — TS + QuickJS-in-WASM, wazero runtime, single-function deploy.** This spec.
2. **M2 — AssemblyScript / Rust direct-to-WASM frontend** for customers who want lower overhead than QuickJS.
3. **M3 — Cross-function composition** (one function consumes another's `DigestSection` to do aggregation). Probably not.

## Companion blog/skill changes

When M1 ships, add to the `daily-digest` skill (rewritten per `workspace-primitives-spec.md`): a "Custom digest functions" subsection pointing at the CLI verbs and the `digest function test` local-dev loop.

The `Just Give the Agent Files` blog already teases the capability at the end of the digest section; a follow-up post should walk through deploying a real customer function once M1 is shippable.
