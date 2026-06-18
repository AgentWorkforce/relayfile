# Vercel AI SDK × Linear writeback (via Relayfile)

A runnable Vercel AI SDK agent that creates, updates, and deletes Linear
labels through Relayfile — no Linear API client, no OAuth, no provider glue.
The agent reads the labels schema, builds a schema-valid payload, writes a
draft, polls for the adapter to materialise the canonical record, then
can read/update/delete it.

## What this proves

| | |
|---|---|
| Bootstrap | Cloud token → `joinWorkspace` → `client()`, identical template to the Notion read example |
| ID handling | App-UUID in, `rw_` ID out — every data-plane call uses `workspace.workspaceId` |
| Least-privilege | Requests `relayfile:fs:read:/discovery/linear/**`, `relayfile:fs:read:/linear/**`, `relayfile:fs:write:/linear/**` (no wildcard write) |
| Schema discipline | Reads `/discovery/linear/labels/.schema.json` and validates the payload before writing — the playbook's "schema-validated > LAYOUT-documented > skip" rule |
| Draft-create flow | Writes to a non-canonical filename (`/linear/labels/relayfile-writeback-test--<ts>.json`), polls for the adapter to rewrite it as `{ created, path, url }`, reads the canonical record |
| Optimistic concurrency | Demonstrates `RevisionConflictError` by writing with a stale `baseRevision` |
| Self-cleaning | Deletes the created label at the end of the smoke so the workspace stays tidy |

## Outward-facing safety

Every smoke run creates ONE real Linear label in the connected workspace and
deletes it before exit. All artifacts are clearly marked
`relayfile-writeback-test <ISO-utc>` so an operator can identify and remove
any orphan if the smoke is interrupted mid-flight. Look under
`/linear/labels/relayfile-writeback-test-*` if you need to clean up.

### What each delete actually does

The smoke performs two delete calls; their provider-side semantics differ:

| Delete target | Path shape | Linear-side effect |
|---|---|---|
| **Canonical** | `/linear/labels/<uuid>.json` (UUID-matching) | **Propagates** — real Linear label is deleted via GraphQL. |
| **Draft receipt** | `/linear/labels/relayfile-writeback-test--*.json` (non-UUID) | **Local-only** — Relayfile-side cleanup, does NOT enqueue a Linear writeback. Per cloud `path-eligibility.ts`, the Linear delete writeback only fires for paths matching `/linear/(issues\|labels\|projects)/<uuid>.json`. |

That means deleting the draft receipt is a free no-op writeback-wise; no
risk of accidentally double-firing a Linear delete.

### Why drafts exist at all

The Linear adapter writes a **receipt** to the draft path with
`{ created, path, externalId }` after the canonical is materialized in
parallel at `/linear/labels/<externalId>.json`. The discovery doc at
`/discovery/linear/.adapter.md` claims drafts are *renamed* to the canonical
path, but the runtime currently writes the receipt in place instead — tracked
as [`AgentWorkforce/relayfile-adapters#213`](https://github.com/AgentWorkforce/relayfile-adapters/issues/213).
The example follows the *real* contract: read `externalId` from
`providerResult`, operate on `/linear/labels/<externalId>.json`, then clean
up the draft receipt explicitly. You can also use
`client.sweepWritebackDrafts({ workspaceId, pathPrefix: "/linear/labels/", apply: true })`
to clear accumulated drafts in bulk.

## Run

Same credential resolution as the Notion read example — three options:

### Option A — pre-minted relayfile token (most CI-clean, no cloud hop)

```bash
export RELAYFILE_BASE_URL="https://api.relayfile.dev"
export RELAYFILE_WORKSPACE_ID="rw_…"
export RELAYFILE_TOKEN="ey…"   # JWT with read+write scopes for /linear and /discovery/linear
```

### Option B — cloud control plane (env)

```bash
export CLOUD_API_URL="https://agentrelay.com/cloud"
export CLOUD_API_ACCESS_TOKEN="cld_at_…"
export CLOUD_API_REFRESH_TOKEN="cld_rt_…"
export CLOUD_WORKSPACE_ID="<your-app-uuid>"
```

### Option C — local cred file (operator convenience)

Run `agent-relay cloud login` once. Set just `CLOUD_WORKSPACE_ID`.

### Smoke (no LLM)

```bash
npm install
npm run smoke
```

Runs the full create → read → update → conflict → delete → verify loop.

### Agent (needs Anthropic key)

```bash
export ANTHROPIC_API_KEY=sk-ant-…
npm run dev
```

The LLM picks the tools to call. It's instructed to always prefix label
names with the test marker and to delete after itself.

## Expected smoke output (excerpt)

```
── create label via draft + adapter rewrite ──
  result   : ✅ PASS
  evidence : {
               "draftPath": "/linear/labels/relayfile-writeback-test--1718711000000-ab12.json",
               "created": "abc12345-…",
               "canonicalPath": "/linear/labels/abc12345-….json",
               "url": "https://linear.app/…"
             }

…

── optimistic update with stale baseRevision → RevisionConflictError ──
  result   : ✅ PASS
  evidence : { "expectedRevision": "rev_X", "currentRevision": "rev_Y", "conflictDetected": true }

── verify deletion: canonical now 404s ──
  result   : ✅ PASS
  evidence : { "confirmedDeleted": true, "status": 404 }
```

## Why labels (and not issues / projects)

Labels are the highest-leverage Phase-1 writeback target:

- **Schema is non-empty** with `name` as the only required field — no `teamId` lookup needed for workspace-level labels.
- **Reversible** — deleting a label is a no-op for real users vs. closing an issue that someone might be working on.
- **Idempotent test-marker** in the name means orphans are trivially identifiable for cleanup.

Issues and projects have richer write paths and are demoed in dedicated
future examples once labels are proven.

## Surface map

Every SDK call here is pinned against `docs/integrations/SDK-SURFACE.md`.
Linear write-field contracts are in `/discovery/linear/.adapter.md` on the
data plane.
