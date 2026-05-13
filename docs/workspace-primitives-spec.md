# Workspace Primitives — Implementation Spec

**Status:** Proposed
**Affects:** `internal/mountfuse`, `internal/mountsync`, `cmd/relayfile-cli`, `packages/core`, `packages/sdk/typescript`, `packages/sdk/python`, `relayfile-adapters/packages/core`, `relayfile-adapters/packages/<adapter>`
**Pairs with:** `LAYOUT.md`, `onwrite-trigger-design.md`, `canonical-file-schema-ownership-boundary.md`
**Drives skills:** [`activity-summary`](https://github.com/AgentWorkforce/skills/tree/main/skills/activity-summary), [`daily-digest`](https://github.com/AgentWorkforce/skills/tree/main/skills/daily-digest), [`writeback-as-files`](https://github.com/AgentWorkforce/skills/tree/main/skills/writeback-as-files), [`workspace-layout`](https://github.com/AgentWorkforce/skills/tree/main/skills/workspace-layout)

---

## Problem

The "Just Give the Agent Files" blog post and the four `@agent-relay/*` workspace-primitive skills (skills#39) document seven workspace features as if they ship. An audit against `mount-verify/`, `internal/`, `cmd/`, `packages/`, and `relayfile-adapters/` found that **four of the seven do not exist in code**:

| Feature | mount-verify | Code |
|---|---|---|
| `<identifier>__<uuid>` filenames, `by-*` indexes, `_index.json`, `LAYOUT.md`, `.relay/dead-letter/`, `writeback retry --opId`, `writeback.{succeeded,failed}` events | ✅ ✅ | ✅ |
| `digests/yesterday.md` | ✅ (hand-authored) | ❌ no generator |
| Per-provider `.layout.md` files | ❌ | ⚠️ referenced by `internal/mountfuse/layout.go` but no writer |
| `<resource>/.schema.json` sibling writeback schemas | ❌ | ❌ only one central `relayfile/schemas/github/issue.schema.json` |
| `relayfile writeback list --state <state>` CLI | n/a | ❌ only `status` and `retry --opId` exist |
| `.error.json` sidecars in `.relay/dead-letter/` | ❌ | ❌ |
| Adapter `digest()` / `DigestContext` SDK contract | n/a | ❌ zero hits in `relayfile-adapters/packages/*` |

This spec defines the minimum implementation that closes the gap, in the order the skills currently assume them.

## Goal

After this spec lands, an agent following the four `@agent-relay/*` skills can:

1. Read `<mount>/digests/yesterday.md` and have it be a real, regenerated artifact (not a hand-authored example).
2. Read `<mount>/<provider>/.layout.md` for provider-specific shape, materialized on disk.
3. Read `<mount>/<provider>/<resource>/.schema.json` to discover writeback payload shape.
4. Run `relayfile writeback list --state pending|dead` to inspect queued and failed writes.
5. Inspect `<mount>/.relay/dead-letter/<file>.error.json` to diagnose a failed writeback.
6. Adapter authors can `export const digest = …` to opt into the daily-summary pipeline.

## Non-goals

- Multiple digest windows beyond `yesterday.md` and `today.md`. (`this-week.md`, `last-week.md`, archived `YYYY-MM-DD.md` are deferred — see _Future work_.)
- LLM-generated digest prose. Digests must be deterministic over the same change-event window.
- Schema validation enforcement at write time. M1 is discovery only — the daemon still validates against its internal registry.
- Cross-workspace digest aggregation.

---

## Work item 1 — Digest generator

### Current state

- `mount-verify/digests/yesterday.md` is a hand-authored Markdown file with YAML frontmatter (`date`, `generated_at`, `covers`).
- `mount-verify/digests/README.md` documents the intended shape.
- No code in `internal/`, `packages/`, `relayfile-adapters/`, or `cloud/` regenerates the file.

### Target state

A background job in the mount daemon produces `<mount>/digests/yesterday.md` and `<mount>/digests/today.md` from the change-event log.

**Files:**
- New: `internal/digest/digest.go` — generator entry point.
- New: `internal/digest/render.go` — Markdown renderer with the frontmatter schema below.
- New: `internal/digest/digest_test.go` — golden-file tests against `testdata/digest/`.
- Wire-up: `internal/mountfuse/syncer.go` (or wherever the post-sync hook lives) calls the digest generator at:
  - 00:00 in the workspace's configured timezone (close-of-day → `yesterday.md`).
  - Every 30s coalesced (rolling → `today.md`).

**Frontmatter contract (must match `mount-verify/digests/README.md`):**

```yaml
---
date: 2026-05-12          # the activity date covered
generated_at: 2026-05-13T00:00:00Z
covers: yesterday         # "today" | "yesterday" | "YYYY-MM-DD"
providers: [linear, github, notion, slack]
events: 47                # raw change-event count over the window
---
```

**Body contract:**
- One `## <provider>` H2 per provider that produced events, alphabetical.
- Bullets within a section sorted by event time ascending.
- Each bullet: `- <identifier> <past-tense verb phrase> — [<canonical path>]`.
- Empty-window file body is `_no activity_` under each provider header (file is always present so consumers can rely on it).

### Acceptance criteria

- [ ] `relayfile digest rebuild --window yesterday` regenerates `digests/yesterday.md` from the change log and produces byte-identical output on a second invocation over the same window.
- [ ] Daemon emits a `file.created` (first time) or `file.updated` event for the digest path so SDK consumers can subscribe.
- [ ] Empty-window case: file exists with frontmatter `events: 0` and body `_no activity_`.
- [ ] Unit test: golden-file diff against `testdata/digest/yesterday-fixture.md` after replaying a fixture change-event stream.
- [ ] Integration test: spin up a mock provider, feed three change events, assert `yesterday.md` renders the expected three bullets at the right paths.

---

## Work item 2 — Adapter `digest()` SDK contract

### Current state

Adapters in `relayfile-adapters/packages/<provider>/src/` export `sync` and `writeback` handlers. They do not export anything that contributes to digest content.

### Target state

Adapters opt into the digest pipeline by exporting a `digest()` function. The mount daemon's digest generator iterates connected adapters, calls each adapter's `digest()` with a windowed change-event slice, and renders the returned sections.

**Files:**
- New: `relayfile-adapters/packages/core/src/digest-contract.ts` — types below.
- New: `relayfile-adapters/packages/core/src/digest-contract.test.ts`.
- Update: each first-party adapter (`linear`, `notion`, `github`, `slack`, `jira`, `confluence`) under `relayfile-adapters/packages/<provider>/src/index.ts` adds a `digest()` export.

**Type contract:**

```typescript
export interface DigestContext {
  readonly provider: string;       // e.g. "linear"
  readonly window: {
    readonly from: string;         // ISO 8601, inclusive
    readonly to: string;           // ISO 8601, exclusive
  };
  changeEvents(filter?: {
    providers?: string[];
    paths?: string[];
  }): Promise<readonly ChangeEvent[]>;
}

export interface DigestBullet {
  readonly text: string;           // one line, past tense
  readonly canonicalPath: string;  // absolute mount-relative path
}

export interface DigestSection {
  readonly provider: string;
  readonly bullets: readonly DigestBullet[];
}

export type DigestHandler = (ctx: DigestContext) => Promise<DigestSection | null>;
```

Returning `null` means "ran successfully, no activity in the window". Throwing surfaces as a warning in the digest header (`warnings: [linear: …]`).

### Acceptance criteria

- [ ] `DigestHandler` type exported from `@relayfile/adapter-core`.
- [ ] Linear, Notion, and GitHub adapters export a real `digest()` (others can no-op to `null` for M1).
- [ ] Generator skips adapters that don't export `digest`; its providers don't appear in the frontmatter `providers` list.
- [ ] Determinism test: two runs over the same fixture produce byte-identical `DigestSection` outputs.

---

## Work item 3 — Per-provider `.layout.md` materialization

### Current state

- `internal/mountfuse/layout.go:56` references `<integration>/.layout.md` files.
- `internal/mountsync/syncer_test.go` tests `applyRemoteFile("/notion/.layout.md")`.
- No file exists in `mount-verify/notion/`, `mount-verify/linear/`, etc.
- The root `LAYOUT.md` is virtual (served in-memory by `layout.go`), not on disk.

### Target state

Each connected provider produces a real `<mount>/<provider>/.layout.md` describing:

- Top-level resource directories (e.g. `issues/`, `cycles/`, `projects/` for Linear).
- The canonical filename convention in use (`<identifier>__<uuid>.json`).
- Which `by-*` alias subtrees are populated (constants already in `internal/mountfuse/layout.go:14-44`: `aliasByTitleSegment`, `aliasByIDSegment`, `aliasByEditedSegment`, etc.).
- Writeback subdirectories and the canonical paths for `<resource>/.schema.json` (see work item 4).
- Materialization mode (eager vs lazy).

**Files:**
- Update: `internal/mountfuse/layout.go` — extend the existing root-layout generator to also produce per-provider layouts. Add `providerLayoutMarkdown(provider, manifest)` returning a string.
- Update: `internal/mountsync/syncer.go` — emit `<provider>/.layout.md` virtual entries at sync time, same in-memory pattern as `LAYOUT.md` at root.
- Adapter signal: adapters expose a `layoutManifest()` returning the resource list / alias indexes they populate. Add to `relayfile-adapters/packages/core/src/layout-contract.ts`.

### Acceptance criteria

- [ ] `cat $MOUNT/linear/.layout.md` returns a non-empty Markdown document describing Linear's tree.
- [ ] File is virtual (no disk write); served via the FUSE layer the same way `LAYOUT.md` is.
- [ ] Document includes every `by-*` segment from `aliasSegments` defined in `layout.go`.
- [ ] Document references `<resource>/.schema.json` for each writebackable resource (forward link to work item 4).

---

## Work item 4 — `<resource>/.schema.json` sibling files

### Current state

- One centralized schema at `relayfile/schemas/github/issue.schema.json`.
- `packages/core/src/writeback.ts` validates writebacks against an internal registry but does not surface schemas to agents.
- Skills currently document a sibling-file pattern that doesn't exist.

### Target state

For every writebackable resource, a virtual sibling file appears next to the writeback directory containing the JSON Schema the daemon enforces.

**Layout:**

```
<mount>/linear/issues/AGE-16__.../comments/
<mount>/linear/issues/AGE-16__.../comments/.schema.json   ← virtual
<mount>/linear/issues/AGE-16__.../state-transitions/
<mount>/linear/issues/AGE-16__.../state-transitions/.schema.json
```

**Source:**
- Schemas live in `relayfile/schemas/<provider>/<resource>.schema.json` (extend the existing convention; the GitHub issue schema is the seed).
- Mount layer serves them as virtual files at `<resource>/.schema.json` paths. Same in-memory pattern as `LAYOUT.md`.

**Files:**
- New: `relayfile/schemas/linear/comment.schema.json`, `relayfile/schemas/linear/state-transition.schema.json`, `relayfile/schemas/notion/page-update.schema.json`, `relayfile/schemas/slack/message.schema.json`. (One schema per first-party writeback action.)
- Update: `internal/mountfuse/` — register schema paths as virtual files.
- Update: `internal/mountfuse/layout.go` per-provider layout (work item 3) to reference these paths.

### Acceptance criteria

- [ ] `cat $MOUNT/linear/issues/AGE-16__.../comments/.schema.json` returns valid JSON Schema.
- [ ] Schema matches what the daemon actually validates against (no drift).
- [ ] Schema is read-only (write attempts return `EACCES`).
- [ ] Listed in the provider's `.layout.md` (work item 3).

---

## Work item 5 — `relayfile writeback list` CLI subcommand

### Current state

`cmd/relayfile-cli/main.go:2220` registers `writeback retry --opId OP`. No `list` subcommand exists. `writeback status` exists for a single op.

### Target state

```bash
relayfile writeback list --state pending|dead|succeeded|failed [--workspace WS] [--json]
```

**Files:**
- Update: `cmd/relayfile-cli/main.go` — register `list` subcommand inside the `writeback` switch.
- New: `cmd/relayfile-cli/writeback_list.go` — handler reusing the same store the existing `runWritebackStatus` reads from (look at `cmd/relayfile-cli/writeback_status_test.go:225` for the existing access pattern).
- Output: plain-text table by default; `--json` emits a JSON array of `WritebackItem` records (already exported from `@relayfile/sdk`).

### Acceptance criteria

- [ ] `relayfile writeback list --state pending` exits 0 with header + zero or more rows.
- [ ] `--state dead` lists items from `.relay/dead-letter/`.
- [ ] `--json` output validates against `WritebackItem[]` from `packages/sdk/typescript/dist/types.d.ts`.
- [ ] No flag = error with usage string, do not default silently.

---

## Work item 6 — Dead-letter `.error.json` sidecars

### Current state

`mount-verify/.relay/dead-letter/` directory exists but is empty. No code path writes structured error context next to a dead-lettered payload.

### Target state

When a writeback exhausts retries, the daemon writes both the original payload and a `<filename>.error.json` sibling capturing:

```json
{
  "code": "schema_violation | provider_4xx | provider_5xx_exhausted | timeout",
  "message": "<human-readable summary>",
  "providerStatus": 422,
  "providerResponse": { "...": "..." },
  "attempts": 4,
  "firstAttemptAt": "2026-05-13T14:30:01Z",
  "lastAttemptAt": "2026-05-13T14:32:07Z",
  "opId": "op_01HQ..."
}
```

**Files:**
- Update: `internal/writeback/dead-letter.go` (or wherever the dead-letter path is finalized — find via `grep -r "dead.letter\|deadLetter" internal/`).
- Update: `internal/mountfuse/` to allow read of the `.error.json` companion.

### Acceptance criteria

- [ ] Force a `schema_violation` in an integration test → both the original `<file>.json` and `<file>.error.json` land in `.relay/dead-letter/`.
- [ ] `.error.json` validates against the JSON Schema in `relayfile/schemas/relay/dead-letter-error.schema.json` (new file, part of this work item).
- [ ] `relayfile writeback list --state dead --json` includes the error fields inline (resolved from the sidecar).

---

## Work item 7 — `wb-<timestamp>.json` naming convention (documentation only)

### Current state

The skill recommends `wb-<unix-ts>.json` as the conventional agent-authored writeback filename. The codebase does not enforce or prefer this.

### Target state

Promote this from skill-doc-only to a documented convention in `relayfile/docs/LAYOUT.md` and the per-provider `.layout.md` files generated by work item 3. No code enforcement — but the convention makes dead-letter forensics easier.

### Acceptance criteria

- [ ] `docs/LAYOUT.md` lists `wb-<timestamp>.json` as the recommended filename pattern.
- [ ] Provider `.layout.md` files generated by work item 3 include the same recommendation.

---

## Sequencing

1. **Work item 1 + 2** (digests + adapter contract) can land together. Single PR.
2. **Work item 3** (per-provider `.layout.md`) is independent. Single PR.
3. **Work item 4** (`<resource>/.schema.json`) depends on work item 3 only for the layout doc that points at it.
4. **Work item 5** (CLI `list`) is independent.
5. **Work item 6** (`.error.json` sidecars) depends on work item 5 for the JSON output integration.
6. **Work item 7** (doc convention) is a one-line change blocked only on work item 3.

Reasonable shipping order: 5 → 1+2 → 6 → 3 → 4 → 7.

## Out-of-scope / future

- `digests/this-week.md`, `last-week.md`, archived `YYYY-MM-DD.md` (defer to M2 after `yesterday.md` is proven).
- Cross-provider summary section in digests (defer; agent's job in M1).
- Schema validation at write time (currently daemon-internal; surfacing schemas is M1, enforcing user-side validation is M2+).

## Companion skill changes

Once all seven items ship, the four `@agent-relay/*` skills already authored in [skills#39](https://github.com/AgentWorkforce/skills/pull/39) become accurate without changes. The cloud sandbox PR can land at that point.

If a work item is dropped or shipped differently, edit the corresponding skill before merging:

- Work item 1/2 drop → rewrite `activity-summary` to point at the `_index.json` + `jq` fallback only; scrap `daily-digest`.
- Work item 4 drop → remove `.schema.json` discovery from `writeback-as-files`.
- Work item 5 drop → remove `writeback list` examples from `writeback-as-files`.
- Work item 6 drop → remove `.error.json` examples from `writeback-as-files`.

## Risks

- **Determinism of digests** — provider sync ordering must not affect the bullet ordering in `yesterday.md`. Fixture-based golden tests are the mitigation; review carefully.
- **Schema drift** — surfacing schemas means agents will rely on them. Any change to the daemon's internal validation registry must update the served `.schema.json` in the same commit.
- **Virtual file overhead** — each new virtual path (`.layout.md`, `.schema.json`, root `LAYOUT.md` already) adds an in-memory entry. Bound to O(providers × resources). Acceptable.
