# Workspace Primitive Skills PR 39 Gap Spec

Status: draft  
Created: 2026-05-15  
Source contracts:
- AgentWorkforce/skills#39: `activity-summary`, `daily-digest`, `writeback-as-files`, `workspace-layout`
- AgentWorkforce/relayfile#146 / commit `e4eaba3`: local copy of the same four skill contracts

## Purpose

The four skills in AgentWorkforce/skills#39 describe agent-facing relayfile
workspace primitives. This spec tracks which parts exist functionally across:

- `relayfile` — local mount, CLI, HTTP API, SDK contracts, self-host runtime
- `cloud` — hosted workspace runtime, digest regeneration, provider sync,
  hosted writeback execution
- `relayfile-adapters` — provider path mapping, layout manifests, digest
  handlers, writeback schemas and provider mutations

The goal is not to install the skills in these repos. The goal is to make the
runtime behavior match what the skills tell agents to rely on.

## Current Verdict

| Skill | Functional Coverage | Summary |
| --- | --- | --- |
| `activity-summary` | Partial | `today.md` and `yesterday.md` exist in cloud, but date-stamped and weekly digests, immutable closing windows, complete/no-cap coverage, and `by-edited` fallback do not. |
| `daily-digest` | Partial | Core rendering and adapter digest exports exist, but cloud does not invoke adapter `digest()` handlers and only supports UTC `today`/`yesterday`. CLI rebuild is stubbed. |
| `writeback-as-files` | Mostly implemented | File-native writeback, schemas, dead-lettering, retry, provider execution, and Notion markdown write-through exist. Gaps remain in agent-facing discovery consistency and `writeback list` state coverage. |
| `workspace-layout` | Partial / divergent | `relayfile` supports root `LAYOUT.md` and virtual `<provider>/.layout.md`; cloud/adapters commonly emit `<provider>/LAYOUT.md`; `by-edited/<date>` is absent. |

## Contract Requirements And Gaps

### 1. Activity Summary

Skill contract:

- Agents can read `<mount>/digests/yesterday.md` first for "what did I work on
  yesterday" and similar time-windowed activity questions.
- Digest directory includes `today.md`, `yesterday.md`, `YYYY-MM-DD.md`,
  `this-week.md`, and `last-week.md`.
- Digest files are deterministic and exhaustive over the requested window.
- Digest bullets link to canonical mount paths.
- Fallback for unusual windows is `by-edited/<date>/` indexes.

Implemented:

- `cloud` writes `/digests/today.md` and `/digests/yesterday.md`.
- `cloud` excludes `/digests/*` writes from digest source events to avoid
  recursion.
- `cloud` emits normal file events for digest file updates.
- `relayfile` has deterministic digest rendering and golden tests.
- `relayfile-adapters` has provider-level digest handlers in many packages.

Missing:

| Gap | Repo(s) | Notes |
| --- | --- | --- |
| Date-stamped daily digests: `/digests/YYYY-MM-DD.md` | `cloud`, `relayfile` | Required so agents can answer specific-date questions without raw provider crawling. |
| Weekly digests: `/digests/this-week.md`, `/digests/last-week.md` | `cloud`, `relayfile` | Required by both `activity-summary` and `daily-digest`. |
| Immutable closing windows | `cloud`, `relayfile` | `yesterday.md`, date-stamped files, and `last-week.md` must be produced at window close and not rewritten on later unrelated changes. |
| Workspace-local timezone windows | `cloud`, `relayfile` | Current cloud window resolution uses UTC. Contract says workspace configured timezone. |
| Exhaustive digest coverage | `cloud` | Current cloud digest implementation limits source events to 500. The skill says no pagination gaps. This needs either removal, pagination/chunking, or explicit warning semantics. |
| Header coverage fields matching contract | `cloud`, `relayfile` | Prior skill text called for `window` and `generated`; current implementations use `date`, `generated_at`, `covers`, `providers`, and `events`. Update skill contracts to the runtime shape. |
| `by-edited/<date>/` fallback indexes | `cloud`, `relayfile-adapters`, `relayfile` | Search found only a type comment, not emitted indexes. |
| Mounted skill-discovery file `/.skills/activity-summary.md` | `relayfile`, `cloud` | Evals reference it. If the runtime expects agents to read it from the mount, it must be materialized or the eval/spec should stop requiring it. |

Acceptance checks:

- A provider change on 2026-05-12 writes or updates:
  - `/digests/today.md` when generated during 2026-05-12 local time.
  - `/digests/2026-05-12.md` after 2026-05-13 00:00 local.
  - `/digests/yesterday.md` after 2026-05-13 00:00 local.
- A provider change on 2026-05-13 does not mutate the already-closed
  `/digests/2026-05-12.md` or closed `yesterday.md` for the prior day.
- A workspace configured for a non-UTC timezone produces window boundaries in
  that timezone.
- A digest with more than 500 source events either includes all events or emits
  a machine-readable warning that agents can detect before trusting the body.
- For a Notion page edited on 2026-05-12, an agent can list
  `/notion/pages/by-edited/2026-05-12/` and find the canonical record.

### 2. Daily Digest Authoring

Skill contract:

- Adapters expose `digest(ctx: DigestContext): Promise<DigestSection | null>`.
- Digest sections are alphabetical by provider.
- Bullets are sorted by event time ascending.
- Bullets contain provider identifier, past-tense verb, and canonical link.
- Returning `null` means "ran successfully, no activity".
- Throws surface as warnings in the digest header.
- Rolling windows rebuild on every relevant change, coalesced to at most once
  per 30 seconds.
- Closing windows are built once at window close.
- `relayfile digest rebuild --window ...` can force regeneration.

Implemented:

- `relayfile-adapters` has `src/digest.ts` handlers across provider packages.
- `relayfile-adapters` documents and checks the adapter digest/layout contract.
- `relayfile` has a deterministic digest package and tests.
- `cloud` has hosted digest regeneration for `today` and `yesterday`.

Missing:

| Gap | Repo(s) | Notes |
| --- | --- | --- |
| Cloud invokes adapter `digest()` exports | `cloud`, `relayfile-adapters` | Current cloud rendering appears generic over workspace events. Adapter-specific `DigestSection` output is not wired into hosted digest production. |
| Warning propagation from adapter digest failures | `cloud`, `relayfile` | Contract says thrown adapter failures surface in digest header warnings. |
| 30-second coalescing for rolling windows | `cloud`, `relayfile` | Current implementation refreshes during write paths; no clear coalescing gate was found. |
| Closing-window scheduler | `cloud`, `relayfile` | Need wall-clock jobs for daily and weekly close events. |
| Force rebuild implementation | `relayfile`, `cloud` | `relayfile digest rebuild` exists but uses a stub rebuilder. Cloud needs an equivalent API/job or CLI bridge if hosted rebuild is supported. |
| Digest path taxonomy shared constants | `relayfile`, `cloud`, `relayfile-adapters` | SDK has `("digests/yesterday.md", "digests/today.md")`; it needs the expanded taxonomy when the runtime supports it. |

Acceptance checks:

- A test adapter digest handler returning a custom bullet is reflected verbatim
  in the hosted digest output.
- A test adapter digest handler returning `null` creates a provider section with
  `_no activity_` or a documented empty-provider behavior.
- A test adapter digest handler throwing adds a `warnings` entry but does not
  prevent other providers from rendering.
- Multiple current-day changes within 30 seconds produce no more than one
  digest rebuild per rolling window.
- `relayfile digest rebuild --window yesterday` regenerates the appropriate
  artifact and reports path and event count.

### 3. Writebacks As Files

Skill contract:

- Agents mutate providers by writing files, not by calling provider SDKs
  directly.
- Writable directories are discoverable next to read-side data via sibling
  `.schema.json` files.
- Agent-authored JSON files conventionally use `wb-<timestamp>.json`.
- The mount validates payloads, queues provider mutations, handles retries,
  idempotency, dead-lettering, and audit.
- Agents can monitor with `relayfile writeback list --state pending|dead` and
  `relayfile status`.
- Failed writebacks appear under `<mount>/.relay/dead-letter/` with a
  `.error.json` sidecar.
- Notion `content.md` is write-through for page markdown updates.

Implemented:

- `relayfile` queues writebacks and exposes pending writeback API surfaces.
- `relayfile` CLI has `writeback status`, `writeback retry`, and
  `writeback list`.
- `relayfile` writes and reads `.relay/dead-letter/*.json` plus
  `.error.json` sidecars with canonical error codes.
- `cloud` executes provider writebacks for Notion, Linear, GitHub, Slack, and
  others through hosted provider executors/bridges.
- `relayfile-adapters` ships `.schema.json`, `.create.example.json`,
  `pathPattern`, and `idPattern` discovery for writable resources.
- Notion markdown `content.md` write-through is implemented in both adapters
  and cloud provider execution.

Missing or divergent:

| Gap | Repo(s) | Notes |
| --- | --- | --- |
| `relayfile writeback list --state pending|succeeded|failed` row coverage | `relayfile` | Current local list only returns per-op rows for `dead`; other states error because only aggregate counters are tracked. |
| Consistent mounted discovery location | `cloud`, `relayfile`, `relayfile-adapters` | Skills say sibling `.schema.json`; older docs also mention `_PERMISSIONS.md`. Pick one canonical agent-facing discovery surface and make mounts emit it consistently. |
| Validation timing and local failure artifact contract | `relayfile`, `cloud` | Skill says mount validates before provider delivery. Productized mount docs mention conflicts for schema validation. Need one current contract and tests. |
| `.tmp` / `.partial` ignore semantics for writeback payloads | `relayfile`, `cloud` | Watcher ignores some mount-state temp files, but the explicit writeback ignore contract needs coverage. |
| Provider mutation within approximately 30 seconds | `cloud`, `relayfile` | Need an SLO test/metric or remove the promise from the skill. |
| Agent-safe replay guidance | `relayfile`, `cloud` | Skill says "fix and re-drop"; CLI has `writeback retry`. Decide which is canonical and document both if both remain. |

Acceptance checks:

- Writing a valid JSON payload to a discovered writeback path creates a pending
  row visible via `relayfile writeback list --state pending`.
- Successful provider delivery moves the row to `succeeded` or removes it per a
  documented lifecycle, and emits `writeback.succeeded`.
- Permanent schema failure creates a local/hosted artifact with:
  - Original payload.
  - `.error.json` sidecar.
  - `code`, `message`, `attempts`, `firstAttemptAt`, `lastAttemptAt`, `opId`.
- `relayfile writeback list --state dead --json` returns the sidecar fields.
- A file named `.tmp`, `*.tmp`, `*.partial`, or an agreed ignored suffix in a
  writeback directory does not enqueue provider mutation until final rename.
- Editing `/notion/.../content.md` results in a Notion markdown PATCH and an
  acked writeback op.

### 4. Workspace Layout

Skill contract:

- Every mount has root `LAYOUT.md`.
- Every provider has `<provider>/.layout.md`.
- Layouts describe top-level directories, filename conventions, populated
  `by-*` indexes, writeback directories, schemas, and pagination/materialization.
- Agents use `by-title/`, `by-id/`, `by-name/`, `by-edited/<date>/`, and
  `by-state/` indexes instead of recursive search.
- Canonical filenames use `<identifier>__<uuid>.<ext>` where possible.

Implemented:

- `relayfile` mount FUSE layer exposes virtual `LAYOUT.md` and
  `<provider>/.layout.md`.
- `relayfile` registers provider layout manifests from snapshots.
- `cloud` writes root `LAYOUT.md` and provider `LAYOUT.md` files in Nango sync
  record writer paths.
- `relayfile-adapters` has provider layout prompt files for priority providers
  and layout manifests/tests.
- `by-title`, `by-id`, `by-name`, and `by-state` aliases exist in multiple
  adapters and hosted sync paths.
- `<identifier>__<uuid>` is broadly implemented, with some legacy `--`
  compatibility in adapters.

Missing or divergent:

| Gap | Repo(s) | Notes |
| --- | --- | --- |
| Provider layout path standardization | `relayfile`, `cloud`, `relayfile-adapters` | Skill says `<provider>/.layout.md`; cloud/adapters often emit `<provider>/LAYOUT.md`. Pick one canonical path or support both with equivalent content. |
| Root layout mentions digests and skills directory | `relayfile`, `cloud` | Skill says root `LAYOUT.md` lists connected providers, digests directory, skills directory, and conventions. Current content is thinner. |
| `by-edited/<date>/` indexes | `cloud`, `relayfile-adapters`, `relayfile` | Not functionally implemented. |
| Layout declares actual available alias indexes per workspace/provider | `cloud`, `relayfile-adapters` | Some provider layouts are static or generic. Contract requires they reflect populated indexes and materialization mode. |
| Symlink/alias semantics | `relayfile`, `cloud` | Skill says aliases are symlinks or directory listings on filesystems without symlink support. Current implementation mostly materializes alias files. Document this explicitly or implement symlink behavior where available. |
| `__` reserved enforcement | `relayfile-adapters`, `cloud` | Filename convention says provider data must not produce `__` in identifier portion. Need tests for slug sanitation. |

Acceptance checks:

- On a live mount, both root layout and provider layout are readable at the
  canonical path(s), have markdown content type, and are read-only.
- Layout content lists the provider's actual alias segments and writeback
  resource schemas.
- For a title lookup, an agent can read layout, list the relevant `by-title/`
  directory, and open the referenced canonical record without recursive search.
- For an edited-date lookup, an agent can list `by-edited/YYYY-MM-DD/` for at
  least Notion pages and issue-tracking records.
- A provider object whose title contains `__` is sanitized so the stable ID can
  still be recovered from the last `__` segment.

## Cross-Repo Work Plan

### Execution Model

Each work item should be implemented in repo-specific worktrees, not by editing
the three repos from one shared checkout. These gaps cross runtime boundaries,
but each repo has its own tests, release cadence, and local dirty state. Use one
worktree per repo/layer so changes stay reviewable and can land independently.

Recommended worktree layout:

| Layer | Repo | Suggested branch/worktree purpose |
| --- | --- | --- |
| Core mount/API/CLI | `relayfile` | `codex/pr39-workspace-primitives-core` |
| Hosted runtime/sync/writeback | `cloud` | `codex/pr39-workspace-primitives-cloud` |
| Provider contracts/adapters | `relayfile-adapters` | `codex/pr39-workspace-primitives-adapters` |

For each work item below:

- Start with the repo that owns the contract decision or source of truth.
- Keep generated/shared type updates in the repo that owns generation.
- Do not make cross-repo behavior changes in a repo without adding or updating
  that repo's local tests.
- When a work item requires coordinated changes, land adapter contract changes
  first, then hosted/runtime consumption, then local mount/CLI affordances.
- Record any contract decision that affects multiple repos in this spec before
  implementing it elsewhere.

Suggested dependency order:

1. `relayfile-adapters`: provider digest/layout/writeback source contracts.
2. `cloud`: hosted digest, sync, and provider writeback consumption.
3. `relayfile`: local mount, CLI, SDK, OpenAPI, and agent-facing docs.

### Work Item A: Canonicalize The Contract

Owner: `relayfile` with `cloud` and `relayfile-adapters` review.

Decisions (resolved 2026-05-15):

- Provider layout path: `<provider>/LAYOUT.md` is canonical. Update the skill
  contracts to match the current runtime names; do not migrate runtime to
  `.layout.md`.
- Digest header shape: keep the existing YAML-style frontmatter
  (`date`, `generated_at`, `covers`, `providers`, `events`). Update the skill
  contracts to use those fields rather than the skill's blockquote header.
- Digest windows in v1: ship `today.md` and `yesterday.md` immediately;
  date-stamped `YYYY-MM-DD.md`, `this-week.md`, and `last-week.md` follow in
  the same work item per the acceptance checks above.
- Writeback discovery surface: sibling `.schema.json` (plus
  `.create.example.json` where present) is the canonical agent-facing surface.
  `_PERMISSIONS.md` is not a runtime requirement.
- Replay model: keep `relayfile writeback retry` as the supported path.
  Document re-drop of a fixed payload as a manual fallback only; do not
  promise both as first-class.
- Activity-summary skill discovery: `/.skills/activity-summary.md` is a
  mounted runtime artifact. Materialize it in `relayfile` and `cloud` so the
  evals can read it from the mount.

Done when:

- AGENTS/rules/docs in all three repos agree on the decisions above.
- Tests assert the chosen paths and header fields.

### Work Item B: Complete Digest Runtime

Owners:

- `cloud`: hosted digest scheduler, adapter digest invocation, timezone windows.
- `relayfile`: self-host/local digest generation, CLI rebuild implementation.
- `relayfile-adapters`: provider digest handler conformance and exports.

Required changes:

- Add `YYYY-MM-DD`, `this-week`, and `last-week` windows.
- Make closing windows immutable after close.
- Add workspace timezone support.
- Replace or paginate the 500-event cap.
- Keep digest rendering generic over workspace events in `cloud`; do not
  require adapter `digest()` handlers to own provider-specific bullet
  rendering. Remove the unused adapter-handler expectation from the skill
  contracts and adapter docs accordingly.
- Surface provider warnings.
- Implement `relayfile digest rebuild`.

Done when:

- Unit tests cover all windows, timezone boundaries, immutability, warnings,
  and more-than-500-event behavior.
- Integration tests prove provider changes regenerate digest artifacts and emit
  digest file events without recursion.

### Work Item C: Add Edited-Date Indexes

Owners:

- `relayfile-adapters`: emit provider-specific `by-edited/YYYY-MM-DD/` aliases.
- `cloud`: preserve and serve those aliases in hosted sync output.
- `relayfile`: ensure local mount sync/FUSE treats the aliases as ordinary
  readable files/directories and documents them in layouts.

Required changes:

- Scope `by-edited` to resources used by activity-summary fallbacks (Notion
  pages, Linear issues, GitHub issues/PRs, Jira/Confluence priority paths).
  Other resources may opt in but are not required.
- Define date source per in-scope provider/resource (`updated_at`,
  `last_edited_time`, merged/closed timestamps where appropriate).
- Emit stable alias filenames that point to current canonical records.
- Clean up stale aliases when edited date changes.
- Add layout manifest support for `by-edited`.

Done when:

- Notion, Linear, GitHub, Jira/Confluence priority paths have `by-edited`
  tests or documented exclusions.
- Activity-summary evals use `by-edited` fallback successfully.

### Work Item D: Finish Agent-Facing Writeback Discovery

Owners:

- `relayfile-adapters`: schema/example/resource metadata source of truth.
- `cloud`: materialize the discovery files in hosted mounts.
- `relayfile`: local validation, CLI list state coverage, dead-letter UX.

Required changes:

- Use sibling `.schema.json` (plus `.create.example.json` where present) as
  the canonical discovery path; emit it consistently in hosted and local
  mounts.
- Ensure `.schema.json` and `.create.example.json` are visible beside every
  writable resource in hosted and local mounts.
- Expose only `pending` and `dead` states to agents via `writeback list`.
  Remove `succeeded` and `failed` from the documented contract; surface
  long-form history through metrics/logs rather than the CLI.
- Add ignore coverage for partial writeback filenames.
- Align schema-validation failure artifacts with current docs.

Done when:

- `relayfile writeback list --state pending|dead --json` works for both
  documented states; `succeeded`/`failed` are removed from the contract and
  help text.
- Dead-letter sidecars round-trip through CLI, FUSE, cloud, and SDK types.
- Provider writeback E2E covers at least Notion markdown, Linear comment,
  GitHub issue/comment, and Slack message.

### Work Item E: Align Layout Runtime And Docs

Owners:

- `relayfile`: mount-level virtual layout behavior.
- `cloud`: hosted sync layout artifacts.
- `relayfile-adapters`: provider-specific layout content.

Required changes:

- Support the chosen provider layout path(s) everywhere.
- Expand root layout to include connected providers, digests, skills if
  materialized, filename convention, and writeback discovery.
- Ensure provider layouts describe actual aliases and writeback resources.
- Add `__` sanitation tests.

Done when:

- A mounted workspace passes a layout conformance test that reads root layout,
  provider layout, index files, alias directories, schemas, and one canonical
  record for each priority provider.

## Suggested Validation Commands

Run these after each implementation slice, adjusting package filters per repo.

In `relayfile`:

```bash
go test ./internal/digest ./internal/mountfuse ./internal/mountsync ./cmd/relayfile-cli
scripts/check-contract-surface.sh
```

In `cloud`:

```bash
npx vitest run packages/relayfile/test/digest.test.ts packages/relayfile/test/writeback-list.test.ts
npx vitest run tests/nango-sync-record-writer.test.ts tests/relayfile-provider-writeback.e2e.test.ts
```

In `relayfile-adapters`:

```bash
npm run test:digest-contracts
npm run test:writeback-discovery
npm test -- --runInBand
```

## Execution Mode And Approval Boundary

Execution preference: local/BYOH first. Cloud promotion is a follow-up once
the local/BYOH path passes its acceptance checks.

Approval boundary for the generated workflow:

- Run validation, render artifacts, write test fixtures, and read provider
  state automatically.
- Pause and ask for user approval before any destructive provider mutation
  (writeback deletes, dead-letter purges, cache resets), before commits or
  pushes to git, before opening or merging pull requests, and before any
  deploy or publish step. The default is non-destructive.
