# verify-features

Use this skill to verify Relayfile behavior from a user-visible boundary after a source change, release preparation, incident, or catalog update.

## Select the checks

1. Run `npm run features:validate` before behavior tests.
2. Read `.agentworkforce/features/manifest.yaml` structurally. Select by requested ID/category, or match changed files against each feature's `locations`.
3. Resolve every selected category through `verification.categories`; run the exact heading in `.agentworkforce/features/verify/procedures.md`.
4. Add the applicable sequence from `.agentworkforce/features/critical-paths.md`.
5. Never infer provider support from a canonical-path helper or an exported planned type.

## Run and report

Use `npm run features:verify -- --tier 1` for deterministic checks. Add `--tier 4` for packed consumers. Tiers 3, 5, and 6 require explicit opt-in described by their procedures; use `--require-live` only in a secrets-enabled disposable environment.

Every check ends as exactly one of `PASS`, `FAIL`, `SKIP`, or `MANUAL`. Only PASS counts as passing. Missing credentials, FUSE support, receiver URLs, PostgreSQL DSNs, or provider fixtures are SKIP. Publish/tag/release and unbounded interactive mutations are MANUAL. Preserve redacted evidence in `.workflow-artifacts/verify-features/`.

## Diagnose

Start with the first failing boundary, not the final symptom: catalog/route → build/export → server/auth → data plane → events/mount → provider/writeback → hosted observer. Compare hosted SDK routes with the local OpenAPI before treating a local 404 as a defect. For ingest, always inspect both the event and `/digests/today.md` plus `/digests/yesterday.md`. For terminal provider state, require a file update unless upstream actually deleted the object.

## Clean up

Follow the procedure's order and marker-checked fixture cleanup. Confirm external provider objects/subscriptions/workspaces first, then clients, mount daemons, and local servers. Never clean a broad workspace, use an unverified PID, or report PASS when cleanup is unconfirmed.
