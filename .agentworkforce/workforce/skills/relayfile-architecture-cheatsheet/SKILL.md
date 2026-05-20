# Relayfile Architecture Cheatsheet

Compressed working-memory model of how Relayfile is laid out, so verification agents can trace failures without re-deriving the architecture each session. Read at the start of any task that touches the cloud or mount daemon.

## Two services, two tokens, two URLs

There are two distinct deployed services. Confusing them is the most common time-sink:

| | URL | Role | What it stores |
|---|---|---|---|
| **api.relayfile.dev** | `https://api.relayfile.dev/v1/...` | The "file API" (Cloudflare Worker + WorkspaceDO + R2). Serves all `fs/*`, `sync/*`, `ops/*`, etc. | The workspace itself: file tree, record JSON, discovery files, digest. |
| **agentrelay.com/cloud** | `https://agentrelay.com/cloud/api/v1/...` | The "control plane" (Next.js + CF Worker, `packages/web`). Identity, workspace registry, integration connect-sessions, the new `POST /sync/refresh` for discovery backfill, the integration status endpoint. | Workspaces table, integrations table, Nango connect-session state. |

And two tokens:

| | File | Issued by | Used against | Refresh path |
|---|---|---|---|---|
| **Cloud identity** | `~/.relayfile/cloud-credentials.json` (`cld_at_*` / `cld_rt_*`) | Cloud OAuth provider | agentrelay.com/cloud endpoints | Browser OAuth via `relayfile login` |
| **Workspace token** | `~/.relayfile/credentials.json` (`.token`, a JWT with `wks:<workspaceId>`, scopes `fs:read/write/sync:read/sync:trigger/ops:read`) | Issued by `POST /cloud/api/v1/workspaces/{id}/join` | api.relayfile.dev endpoints | The CLI auto-tries to mint this after `relayfile login`; the `/join` endpoint is currently slow (`cloud#766`) so direct curl with `--max-time 120` is the reliable path |

Most production debugging touches BOTH services. The workspace token works for `fs/*`/`ops/*`/`sync/*` on api.relayfile.dev AND for the cloud's `/integrations/*/status` + `/sync/refresh` routes (which use the strict `hasWorkspaceAccess` check requiring the token's `wks` claim to match the path).

## The mount daemon

The local `relayfile mount` daemon is the Go binary `relayfile-mount` (also linked as `relayfile-cli` for the embedded mount loop). It polls `api.relayfile.dev/sync/status` every ~30s, applies remote events to a local mirror under the local mount dir, and queues local writes back as writeback ops.

Key state files in the mount dir:

- `.relay/state.json` — daemon's live state (workspaceId, lag, counters, telemetry, circuit-breaker state).
- `.relay/integrations/<provider>.json` — per-integration metadata.
- `.relay/dead-letter/<opId>.json` — failed writebacks that exhausted retries.
- `.relay/pending-deletes/<sha256>.json` — two-phase tombstone markers (from `#165`).
- `.relayfile-mount-state.json` — per-file revision tracking.
- `.relayfile.acl` — per-directory ACL marker file (handlers/fs.ts:57 emit; filtered from digest by `cloud#771`).

Important invariants from PRs #164/#165:

- The mount root must always remain a directory (`writeFileAtomic` refuses to rename a file over the root or any directory).
- Snapshot deletes go through two-phase tombstones (require 2 consecutive clean confirmations before `os.Remove`).
- A circuit breaker (`RELAYFILE_CB_*`) opens on cloud 5xx storms; while open, destructive reconcile is refused.
- Typed `RelativeRemotePath` rejects `..`/empty/basename-collision-with-root.

PR #166 added the bootstrap fix: large-workspace convergence with `BootstrapComplete` flag, progress-extending watchdog (`RELAYFILE_BOOTSTRAP_TIMEOUT`), resumable bootstrap cursor, independent `RELAYFILE_CURSOR_TIMEOUT`, the `--full-reconcile` / `RELAYFILE_FORCE_FULL_RECONCILE` escape hatch.

## The cloud architecture: WorkspaceDO

api.relayfile.dev is a Cloudflare Worker that fronts one Durable Object per workspace (`WORKSPACE_DO.idFromName(workspaceId)` in `packages/relayfile/src/durable-objects/workspace.ts`). The DO holds:

- DO SQLite (`state.storage.sql`) — the `files`, `events`, `operations`, `webhook_envelopes` tables.
- R2 — file bodies keyed by `${workspaceId}${normalizedPath}@${revision}`.

PR #730 eliminated a memory OOM in `WorkspaceDO`: streaming export (parent Worker streams from R2; DO returns only paginated manifests), keyset-paginated queries, `Content-Length` rejection before buffering, chunked base64. PR #766 / #768 separately addresses the `/join` slowness in the control plane (which is on the agentrelay.com/cloud Worker, not the WorkspaceDO).

## The sync pipeline

Provider events flow:

```
Nango webhook → cloud webhook router (packages/web/lib/integrations/nango-webhook-router.ts)
  → handleAuthEvent / handleSyncEvent
  → packages/core/src/sync/record-writer.ts writeBatchToRelayfile()
  → materializeProviderContract() (LAYOUT + discovery emit; BEFORE applied-records gate so even no-op batches refresh contracts)
  → bucketByModel() (in-batch records)
  → writeProviderRecord() per record (canonical + indices + aliases)
  → writeProviderAuxiliaryFiles() (provider-specific aux trees)
```

Critical: every provider's `LAYOUT.md`, discovery files, and record/index/alias trees are emitted by the SAME adapter — wired through the SINGLE `ADAPTERS` registry in `record-writer.ts`. There is exactly one add-point for a new integration (the `ADAPTERS` array), and the LAYOUT-advertises-discovery / discovery-emits-files / writeback-router-validates-payloads invariant is structurally guaranteed by sharing one registry entry. See `.claude/rules/integration-adapter-registry.md`.

## The discovery chain (#745 → #756 → #761)

Three PRs together produce the writeback discovery surface:

- **#745** — discovery is materialized inside `writeBatchToRelayfile` (active-sync path). The first version inferred the schema from the batch's records, which means schema fidelity refines over time as more records are synced. Idempotent via monotonic-merge + `canonicalizeSchema` + `writeManagedFile` byte-stable dedup.
- **#756** — new on-demand path `POST /cloud/api/v1/workspaces/{id}/sync/refresh` with `ensureProviderDiscoveryContract()` for already-synced workspaces (where no new Nango batch ever fires). Originally fed inference a synthetic zero-record job (→ permissive empty schemas).
- **#761** — fixed #756's empty-schema problem by sampling existing synced records: for each resource, read `<resource.path>/_index.json`, dereference each row id through `["by-id", "by-uuid"]` (first hit wins), unwrap the `payload` envelope. Generic across nango adapters (no per-provider switchboard).

#761's coverage is honest: works for FLAT resources (`/<p>/<resource>` with an `_index.json` + id-keyed `by-id`/`by-uuid` aliases) — verified for Linear `/linear/issues` (66 props) and Jira `/jira/projects` (12 props). Skips `{placeholder}`-path sub-resources by design (comments, per-parent sub-resources). The `cloud#778` anomaly is the SILENT case: refresh reports `backfilled:true` but the schema is empty for some flat resources (Jira issues, Confluence pages) — root cause likely a row-id vs alias-key mismatch in those specific adapters; masked by `writeManagedFile` dedup'ing byte-identical empties.

## File-native writeback contract

Per-provider `discovery/<p>/.adapter.md` documents which resources are writable. For each writable resource:

- **Edit** an existing record: write modified content (preserving all fields) to the canonical record path with `If-Match: <revision>`. Server-managed fields (`readOnly:true` in the schema) must be omitted or unchanged. `cloud#780` is the current Jira gap: the canonical edit path is documented but the adapter's writeback router has no rule for it.
- **Create** a new record: write a valid JSON payload to a NON-canonical filename inside the resource directory (e.g. `/<p>/<resource>/draft-<utc>.json`). The adapter creates the real provider record, emits the canonical `<id>.json`, and **rewrites the original draft file as a pointer/receipt** referencing the canonical path.
- **Delete**: remove the canonical `<id>.json` (only when the adapter's `.adapter.md` confirms delete is supported).

`new.json` is NOT special in the file-native contract. If a legacy `new.json` template exists alongside `discovery/`, the workspace is mid-migration; prefer the discovery files.

## The digest pipeline

`/digests/today.md` and `/digests/yesterday.md` are regenerated whenever a non-`/digests/*` write to the workspace happens. The pipeline lives in `packages/relayfile/src/durable-objects/digest.ts`:

- `refreshWorkspaceDigests` → `writeDigestWindow` → `readDigestEvents`.
- The 2000-event budget is enforced at SQL fetch time (`LIMIT DIGEST_EVENT_LIMIT + 1`) inside the WorkspaceDO's DO SQLite.
- PR #771 added `isInternalDigestPath` filtering (in both the SQL `WHERE` AND a JS post-fetch filter) for `.relayfile.acl`, `LAYOUT.md`, `/.relayfile-mount-state.json`, `/discovery/**`, `/digests/**`, `/.skills/**` — so these internal paths don't consume the budget. Path-segment anchored (not substring), `additionalProperties:true` permissive when in doubt (under-filter > over-filter).

If a digest reports `events:2000 truncated:true warnings:[digest_event_limit_exceeded]`, real provider activity is being squeezed out. Read the body — is it dominated by some internal path the filter doesn't cover yet? Add it to the predicate.

## Worker import safety (B1 check)

`tests/b1-worker-import-safety.test.ts` runs in CI's `Unit Tests` job. It bundles every Worker sync entrypoint with esbuild and asserts the transitive import graph contains no Node-only deps (`fs`, `net`, `dns`, `crypto`, `util` resolution errors).

When B1 fails, it's almost always because someone added an import in code that ends up in the Worker bundle and that import transitively pulls a Node module (most commonly `pg` via `db/client`). The fix is an **import-graph split** — never an `external` allowlist (which masks the break). PR #762 split `nango-sync-dedup.ts` for exactly this reason.

A B1 failure may be inherited from main — always check `origin/main` baseline (`git diff origin/main -- <suspect-file>`) before blaming the current branch.

## Phase-0 CI semantics (the "known issue" trap)

The cloud repo's `Phase 0 (acceptance + handler + replay)` job runs:

1. The route-coverage gate (`scripts/check-route-coverage.mjs`) — requires every API route × method to have a `// @route <METHOD> <path>` header in some `packages/acceptance/src/**/*.test.ts` file.
2. The acceptance suite against LIVE PROD (not the PR's preview).

The "known Phase-0 issue" is specifically: a brand-new route returns `404`/`405` against prod until the PR is merged + deployed, so the acceptance test for that route can't pass pre-merge. This is the only legitimate Phase-0 skip case. ALL OTHER Phase-0 failures are real:

- Missing route-coverage header → add the acceptance test (mirror `workspaces-delete.test.ts`).
- Acceptance suite failing on an EXISTING route → real regression.
- Acceptance test asserting JSON body on a 404 HTML page → make the assertion conditional on `status === 401`.

When in doubt, do not `--admin`-merge past Phase 0 — diagnose first.

## Quick paths and commands

```bash
# Verify workspace token
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  "https://api.relayfile.dev/v1/workspaces/<ws>/fs/tree?path=/&depth=1" \
  -H "Authorization: Bearer $TOKEN"

# Refresh workspace token (the /join workaround for cloud#766)
CAT=$(jq -r .accessToken ~/.relayfile/cloud-credentials.json)
curl -X POST "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/join" \
  -H "Authorization: Bearer $CAT" -H "Content-Type: application/json" \
  -d '{"agentName":"relayfile-cli","scopes":["fs:read","fs:write","sync:read","sync:trigger","ops:read"]}' \
  --max-time 120

# Read a workspace file (handles `{}` paths via URL encoding)
curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/file" \
  --data-urlencode "path=/discovery/<p>/<resource>/.schema.json" \
  -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: r-$RANDOM"

# Read the workspace tree (paginated via depth)
curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/tree" \
  --data-urlencode "path=/<p>" --data-urlencode "depth=2" \
  -H "Authorization: Bearer $TOKEN"

# Trigger on-demand discovery backfill (cloud control plane)
curl -sS -X POST "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/sync/refresh" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{}' \
  --max-time 240

# Inspect a writeback op
curl -sS "https://api.relayfile.dev/v1/workspaces/<ws>/ops/op_<n>" \
  -H "Authorization: Bearer $TOKEN"

# Per-provider integration status (cloud control plane)
curl -sS "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/integrations/<provider>/status" \
  -H "Authorization: Bearer $TOKEN"
```

## Cross-links

- PRs that ship the discovery chain: cloud#745, cloud#756, cloud#761.
- PR that unbroke main CI alongside #761: cloud#762.
- Mount-protection PRs: relayfile#164, relayfile#165, relayfile#166.
- DO OOM PR: cloud#730.
- Workspace delete cascade: cloud#735.
- Connection-created ingress fix: cloud#736.
- Digest internal-path filter: cloud#771.
- Open known-issue tracker: cloud#766 (/join slow), cloud#775 (Worker dedup at parity-enablement), cloud#778 (silent-empty schemas for some flat resources), cloud#780 (no Jira writeback rule for canonical edit).
- Rules: `.claude/rules/integration-adapter-registry.md` (the single-ADAPTERS-registry invariant), `.claude/rules/relayfile-integration-digests.md` (digest contract), `.claude/rules/workers-fetch.md` (Worker-safe HTTP).
