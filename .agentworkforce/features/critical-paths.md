# Relayfile critical paths

Resolve each category through `manifest.yaml` and execute the named procedure in `verify/procedures.md`. These paths name categories, not a second feature-ID inventory.

## Fast health triage

1. Run `node scripts/validate-feature-catalog.mjs`.
2. Run the `cli` procedure.
3. Run the focused contract and guardian tests from `proactive-automation`.
4. If source changed, continue with every category whose `locations` contains the changed path.

## Local authenticated CRUD, revision, and ACL

Run `server-storage-auth` → `filesystem` → `acl-revisions-forks`. Prove health, create/read/query/export, stale `If-Match` conflict, path-scope denial, and cleanup deletion. Any skipped local assertion makes the path incomplete.

## Two-mount bidirectional convergence

Run `mount` with two isolated local roots and the local server. Prove API seed reaches both mounts, each mount writes back to the other, delete tombstones converge, events advance, and both daemon PIDs exit. The outbound webhook receiver remains separately `SKIP` if absent.

## Provider ingest to event and digest

Run `ingest-digests` → `events`. Ingest create/update/terminal/delete in that order. Prove the provider file event and both current digest artifacts change; terminal state remains data; only the explicit upstream delete removes the provider file.

## File write to provider receipt and recovery

Run `operations-writeback`. Prove queued operation → provider result/receipt → acknowledgment. Then inject a retryable failure, prove bounded retry and dead letter, replay it, and prove final receipt. Mock proof is local; a provider receipt is a distinct Tier 5 result.

## Bootstrap resume and destructive fences

Run `mount` with a checkpointed traversal, cancel it, resume, and prove progress does not restart from zero. Trigger (without applying) delete-ratio, clobber, rehome, root-integrity, path-collision, and PID-reuse guards. Every fence must be externally visible in status or an incident artifact.

## Hosted setup, connect, mount, and agent event

Run `workspace-auth` → `integrations` → `sdk-typescript` or `sdk-python` → `agents` with disposable hosted credentials. Prove scoped invitation, backend-specific connection, mount-session renewal, WebSocket recovery, and agent read/write fence. Missing credentials produce `SKIP`, never `PASS`.

## Durable resource subscription and delivery claim

Run `sdk-typescript` and prove create-or-renew, owner-scoped list, delivery claim, claim-token acceptance, and cancellation use the exact OpenAPI routes. Assert the client drops a forged `ownerId`, rejects empty identifiers and out-of-range TTLs before I/O, and surfaces a capability `404`. This deterministic client-contract proof does not confirm a hosted implementation; exercise the same lifecycle against a disposable hosted workspace before reporting live service support as `PASS`.

## Package consumer and release boundary

Run `release-e2e` after `sdk-typescript`, `sdk-python`, `core-client-local-mount`, and `agents`. Install packed artifacts into clean temp consumers, import declared entrypoints, resolve the platform binary, and exercise help/version plus a local flow. Publishing, tagging, and release creation remain `MANUAL`.

## Diagnosis and cleanup order

On failure, retain redacted logs, identify the first broken boundary, and rerun its named procedure after repair. Cleanup order is external provider artifact → hosted subscription/workspace → WebSocket/client → mount daemon → local server → marker-checked temp root. If cleanup cannot be confirmed, report `FAIL` and the exact retained resource.
