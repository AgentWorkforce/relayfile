# Integration Verification Playbook

The canonical methodology for proving — with empirical evidence against a real Relayfile workspace — that a provider integration actually works end-to-end. Read this in full at the start of every verification task. Update it whenever a new failure mode appears.

## Why this playbook exists

A Nango integration shipping does not equal an integration that works. Across the May 2026 verification effort, the following gaps were only visible by actually exercising the end-to-end flow on a real workspace, never by reading PR descriptions:

- Discovery schemas materializing at the advertised paths but containing zero properties for some flat resources (`cloud#778`).
- Outbound writeback rejecting canonical edit paths the adapter itself emits (`cloud#780`).
- A control-plane endpoint silently 15–90s slow under realistic load, so the CLI's HTTP client times out before the cloud responds (`cloud#766`).
- Sync workers silently no-op'ing because of a plan/quota gate, while reporting `backfilled:true`.
- A Worker bundle transitively pulling `pg` from a sibling import, breaking deploys (`cloud#762`).

Each of these passed CI. Each was caught only by running the full flow. The playbook locks in that the full flow gets run.

## The seven phases

For every provider × workspace, run these in order. Don't skip phases. If a phase is honestly skippable (e.g. provider does not support outbound writes), note that explicitly with the reason — never silently.

### Phase 1 — Auth

- Verify the workspace token in `~/.relayfile/credentials.json` is valid:
  ```
  curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
    "https://api.relayfile.dev/v1/workspaces/<ws>/fs/tree?path=/&depth=1" \
    -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: auth-$RANDOM" --max-time 30
  ```
  Expected: `200`.
- If `401`: refresh. Try `relayfile login --no-open` first. If it emits `warning: could not refresh workspace token for <ws>: ... context deadline exceeded` (the `cloud#766` symptom), mint the token directly with curl:
  ```
  CAT=$(jq -r .accessToken ~/.relayfile/cloud-credentials.json)
  curl -X POST "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/join" \
    -H "Authorization: Bearer $CAT" -H "Content-Type: application/json" \
    -d '{"agentName":"relayfile-cli","scopes":["fs:read","fs:write","sync:read","sync:trigger","ops:read"]}' \
    --max-time 120 -o /tmp/join.out
  ```
  Persist the returned `.token` into `~/.relayfile/credentials.json`'s `.token` field (use `python3 -c 'import json...'` for safe in-place edit).
- If cloud login itself fails (no fresh `accessToken` after `relayfile login`): the operator must complete the browser OAuth. Ask, don't wait silently.

### Phase 2 — Connect

- Confirm the provider is connected for this workspace:
  ```
  relayfile integration list --workspace <ws> --json
  ```
  Expected row: `{"provider":"<p>","status":"ready","connectionId":"<uuid>"}` (or `connected`).
- If not connected: `relayfile integration connect <p> --workspace <ws> --no-open` and complete OAuth in the browser. The CLI poll resolves once the cloud reports `oauth.connected:true` (often `<5s` after the Nango webhook fires; see `cloud#736` for the connection-created ingress fix that made this reliable).
- Verify cloud-side state directly:
  ```
  curl -sS "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/integrations/<p>/status" \
    -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: c-$RANDOM" --max-time 60
  ```
  Look for: `oauth.connected:true`, `connectionMatched:true`, `currentConnectionId:<uuid>`, `initialSync.state` is `queued` | `running` | `complete`, `lastError:null`.
- **Red flag (file an issue if seen):** `oauth.connected:false` with no error after completing OAuth, OR `connectionMatched:false` with a non-null `currentConnectionId` — that's a Nango webhook ingestion gap (the `cloud#736` family).

### Phase 3 — Sync

- Wait for `initialSync.state === "complete"`. If it gets stuck in `queued` with `syncName:null` / `model:null`, that's typically a Nango plan/quota issue — the sync worker accepted the job but Nango never registered the actual sync. Confirm with the user (the operator can check Nango's billing/usage UI) before assuming it's a code bug.
- Verify the workspace tree has provider data:
  ```
  curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/tree" \
    --data-urlencode "path=/<p>" --data-urlencode "depth=2" \
    -H "Authorization: Bearer $TOKEN" -H "X-Correlation-Id: t-$RANDOM" --max-time 60
  ```
  Expected: real entries (not just `LAYOUT.md` + `_index.json`). Note total path count for evidence.

### Phase 4 — Tree shape

- Read `/<p>/LAYOUT.md` to confirm the documented record/alias structure matches what's actually emitted:
  ```
  curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/file" \
    --data-urlencode "path=/<p>/LAYOUT.md" -H "Authorization: Bearer $TOKEN"
  ```
- Spot-check the actual canonical record path (e.g. `/<p>/<resource>/<canonical-filename>.json`) and the `by-id`/`by-uuid`/`by-*` alias subtrees the LAYOUT advertises. They must actually exist.
- If LAYOUT advertises `discovery/<p>/.../.schema.json` but no `/discovery/<p>/` subtree exists, that's the pre-#761 condition and is itself a finding.

### Phase 5 — Discovery materialization

- Trigger an on-demand backfill:
  ```
  curl -sS -X POST "https://agentrelay.com/cloud/api/v1/workspaces/<ws>/sync/refresh" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{}' \
    --max-time 240
  ```
  Expected: `200` with body `{ "workspaceId": "<ws>", "refreshed": [{ "provider": "<p>", "discoveryBackfilled": true, "errors": 0 }, ...] }`.
  Note: this endpoint is currently slow (commonly 90–180s for 6 providers). 240s timeout is a safe floor.
- Read each writable resource's discovery files:
  ```
  for path in \
    "/discovery/<p>/.adapter.md" \
    "/discovery/<p>/<resource>/.schema.json" \
    "/discovery/<p>/<resource>/.create.example.json"; do
    curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/file" \
      --data-urlencode "path=$path" -H "Authorization: Bearer $TOKEN"
  done
  ```
  For `.schema.json`: parse content, report `properties` key count + `lastEditedAt`.
- **The `cloud#778` pattern to watch for:** `refreshed[].discoveryBackfilled === true` but `properties: {}` AND `lastEditedAt` is older than the refresh timestamp. That means `writeManagedFile` dedup'd a byte-identical empty schema — the sampling silently produced zero records for that resource. File a separate issue per provider × resource with the empty schema, citing the stale `lastEditedAt` as evidence.
- **The `cloud#761` honest scope:** placeholder-path resources (`/<p>/<parent>/{placeholder}/<sub>`) are intentionally NOT backfilled by #761 — their schemas stay permissive-empty by design. Note these explicitly; do not confuse with #778.

### Phase 6 — Outbound writeback

Only run if Phase 5 produced a non-empty `.schema.json` for at least one resource AND a safe target exists.

#### Target selection

In order of preference:

1. **Schema-validated + pre-existing throwaway**: a record explicitly marked `relayfile-writeback-test-*` (e.g. `KAN-4` in `rw_fc7b534b`).
2. **Schema-validated + dedicated test record** the operator creates upfront and confirms is throwaway.
3. **LAYOUT-documented edit on a confirmed throwaway**: when no schema exists but the LAYOUT documents the contract.
4. **Skip**: anything else. Do not write to a real production record to "test the chain."

#### Edit flow (canonical record path)

- Read the existing canonical record:
  ```
  curl -sG "https://api.relayfile.dev/v1/workspaces/<ws>/fs/file" \
    --data-urlencode "path=/<p>/<resource>/<canonical-filename>.json" \
    -H "Authorization: Bearer $TOKEN"
  ```
  Save the `revision` for `If-Match`.
- Construct the mutated content: read the schema, identify a mutable (`readOnly:false`) field, modify it with the marker. Marker format: `[relayfile writeback test <ISO-utc>]`. For dict-shaped fields (e.g. Jira ADF descriptions), append a structured node. For string fields, append the marker text. Preserve all other fields verbatim.
- PUT the modified content:
  ```
  curl -sS -X PUT "https://api.relayfile.dev/v1/workspaces/<ws>/fs/file?path=$(urlencode <path>)" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -H "X-Correlation-Id: wb-$RANDOM" -H "If-Match: <revision>" \
    --data-binary @/tmp/wb-body.json --max-time 60
  ```
  Expected: `HTTP 202` with `{"opId":"op_<n>", "status":"queued", "targetRevision":"<new-rev>", "writeback":{"provider":"<p>","state":"pending"}}`.

#### Round-trip verification

- Wait 30–60s for the writeback executor to PATCH the provider.
- Read the op state: `GET /v1/workspaces/<ws>/ops/op_<n>`. Expected: `status: "succeeded"` or `"completed"`. Watch for the **`cloud#780` pattern**: `status:"failed", attemptCount:0, lastError:"No <p> writeback rule matched <path>"` — the routing layer rejected the path because the adapter has no edit rule. File an issue per the template.
- Re-read the canonical record from the workspace. Expected: new revision, marker present in the mutated field.
- (Optional but recommended) Verify in the provider's actual UI/API that the change landed (e.g. the Jira REST API for the issue). If the cloud-side record shows the marker but the provider doesn't, the writeback succeeded locally but the outbound failed — that's the same class as `cloud#780`.

#### Create flow (non-canonical filename)

- Use `<resource>/.create.example.json` as the starting point. Customize the marker in a clearly-labeled field (e.g. the issue/comment/page title or body).
- PUT to a non-canonical filename inside the resource directory (e.g. `/<p>/<resource>/wb-test-<utc>.json`).
- Expected: adapter creates the real provider record, the canonical `<id>.json` appears with the synced data, and the original draft file is rewritten as a **pointer/receipt** referencing the canonical path. Verify both the canonical record exists AND the draft was rewritten (not just left as-is, not deleted).
- The pointer-rewrite is the documented file-native create contract; if the draft stays as a raw payload or vanishes without a receipt, that's a finding.

### Phase 7 — Inbound (webhook-driven sync)

This phase requires the operator to make a change in the external provider UI/API. Coordinate with them.

- Ask the operator to make a small, identifiable change in the provider (e.g. add a label to a Linear issue, edit a Notion page title, append text to a Confluence page, send a test message to a designated Slack channel). Note the timestamp.
- Within ~60s, the Nango webhook should fire → cloud → file event → workspace tree update.
- Re-read the affected canonical record from the workspace. Expected: the change is reflected in the file content, the file's `revision` advanced.
- Also check `/digests/today.md` — the change should appear in the activity summary for the provider (assuming the digest internal-path filter from `cloud#771` is correctly excluding internals).
- If the change does not propagate within ~5 minutes: file a webhook-ingress issue with the operator's exact action, timestamp, expected vs observed.

## Recurring failure patterns

When you see these, file the issue immediately — they are known classes with known evidence:

| Pattern | Looks like | Filed as | Issue template focus |
|---|---|---|---|
| Empty discovery schema masked by dedup | `backfilled:true` but `properties:{}` AND `lastEditedAt` older than refresh | `cloud#778` | Resource × root cause hypothesis (row-id vs alias-key mismatch); add behavioral regression test asking |
| Missing writeback rule for canonical edit | `op_<n>.status:"failed", attemptCount:0, lastError:"No <p> writeback rule matched <path>"` | `cloud#780` | Cite both the LAYOUT contract AND the live emit path; ask for the rule + a regression test |
| Pathologically slow control-plane endpoint | Same endpoint, multiple HTTP timings, variance ≥10× | `cloud#766` | Always include a comparison to fast endpoints (`/api/health`, `/fs/tree`) at the same time |
| Worker bundle drift | B1 worker import safety fails with Node builtin resolution errors | `cloud#762` | The fix is import-graph split, NEVER an `external` allowlist |
| Connection-created ingestion gap | `oauth.connected` stays `false` after a successful Nango OAuth | `cloud#736` | Check connect-session pre-creates a pending row; webhook ingress; mock vs prod path divergence |
| Worker dedup at parity-enablement | (latent) Worker queue consumer doing real work without persistent dedup | `cloud#775` | At-least-once → duplicate side effects; needs D1 or postgres-js-over-Hyperdrive |

## Issue-filing template

Every gap discovered during verification becomes a GitHub issue. Use this template:

```markdown
## Summary
[1–3 sentences: what happens, where, and why it matters operationally]

## Empirical evidence
[Curl response excerpts, file-content excerpts, file paths + revisions. Concrete, not summarized.]

## Repro
[Numbered steps an engineer can copy-paste. Include exact paths, exact headers if relevant, the workspace id if it's a published one.]

## Suspected root cause
[Hypothesis with file:line refs if available. Be explicit that it's a hypothesis vs verified.]

## Why it matters
[Operational impact — does it block a documented feature? Mislead an agent? Silently corrupt data?]

## Asks / next steps
[Concrete, numbered. E.g. "Trace X", "Add regression test for Y at file:line", "Update the operator-facing doc Z".]

## Context
[Cross-link related issues + PRs. Mention which verification phase surfaced it.]
```

## Test-marker conventions

Every outward-facing write (writeback test, create test) MUST include a clearly-marked machine-greppable marker so the operator can find + clean up afterwards:

- Format: `[relayfile writeback test <ISO-utc>]` — e.g. `[relayfile writeback test 2026-05-20T08:09:56Z]`.
- For dict-shaped fields (ADF, JSON-LD bodies): embed the marker as a structured paragraph/text node so it doesn't break the document.
- For string fields: append on a new line with a leading blank line.
- For create operations: include the marker in the title/name AND the body, so it's discoverable from both the listing view and the detail view.

After every writeback test, output: the provider, the exact record id, the path in the workspace, the timestamp marker. That output is what the operator uses to manually verify + clean up.

## Verification matrix output

Final report MUST include a per-provider × phase matrix:

| Provider | Auth | Connect | Sync | Tree | Discovery | Writeback-out | Webhook-in |
|---|---|---|---|---|---|---|---|
| gmail | ✅ HTTP 200 | ✅ oauth.connected:true | ✅ initialSync.state:complete (<n> records) | ✅ matches LAYOUT | ⚠️ schema empty (cloud#NNN) | ⏭️ skip (no schema) | ❌ change not propagated (cloud#MMM) |
| gcal | ... | ... | ... | ... | ... | ... | ... |

Cells: ✅ + one-line evidence • ⚠️ + caveat • ❌ + linked issue • ⏭️ + reason for skip. Never a bare ✅ without evidence.

## What this playbook does NOT cover

- Performance / load testing.
- Multi-workspace concurrency.
- Token expiration and refresh policies.
- Webhook signing verification (assumes Nango handles it; the cloud webhook router validates).
- Provider-specific feature parity audits (e.g. "does the Linear adapter cover all 53 webhook event types"). Those are separate work.

Update this list as scope grows.

## Updating this playbook

Append a new failure-pattern row to the table whenever a new class of gap is discovered. Append a new phase section if a phase emerges that isn't covered (e.g. a `Phase 8 — Bulk operations` if bulk writeback semantics become a real concern). An unrecorded failure mode WILL recur — keep this file current as the live ledger.
