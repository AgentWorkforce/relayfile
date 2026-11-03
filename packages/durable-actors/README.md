# @relayfile/durable-actors

Self-hosted relayfile on [TerseAI durable-actors](https://github.com/TerseAI/durable-actors)
(0.14.6): every workspace is one `Workspace` actor with its own SQLite, behind a
stateless gateway. This is the self-host counterpart of the hosted
relayfile-cloud Durable Object deployment, with the same shape as relaycast's
actor deployment (armadillo): a gateway routes by verified credential, one
actor per tenant holds the state, maintenance runs on the actor alarm, and file
bytes stay out of actors, in object storage.

```
mount / CLI / SDK / writeback workers
        │ HTTPS + WS (relayfile /v1 API, unchanged)
        ▼
relayfile-actors gateway (N replicas, stateless)
  ├─ verify bearer (relayauth JWKS RS256, or HS256 RELAYFILE_JWT_SECRET)
  ├─ actor id = the token's VERIFIED workspace claim (path must match)
  ├─ file bytes > RELAYFILE_BLOB_INLINE_MAX_BYTES ─▶ GCS / S3 (sha256-addressed)
  ├─ HTTP ─▶ Workspace[ws].handle(request, caller)        (project session)
  └─ /fs/ws ─▶ socket grant ─▶ proxied actor WebSocket
        │
        ▼                         session-broker (internal; holds the admin key,
durable-actors control plane ◀── issues ≤60 s project sessions + socket grants)
        │
        ▼
Workspace actor (gVisor sandbox on GKE)
  @relayfile/core on actor SQLite: files, events, ops/writeback, envelopes
```

## What is served

Wire-compatible with the Go server for everything the mount daemon, CLI, SDK and
writeback workers use:

| Route | Notes |
| --- | --- |
| `GET /health`, `/healthz`, `/readyz` | `/health` advertises `features: ["symlink-v1"]`; `/readyz` checks the control plane (or broker) and the blob store |
| `GET /fs/tree`, `GET/PUT/DELETE /fs/file`, `POST /fs/bulk`, `POST /fs/bulk-read` | If-Match semantics, 409 `revision_conflict` shape, symlinks, modes, semantics, `contentIdentity` dedupe, ACL markers and path-scoped tokens |
| `PUT /fs/file` raw bytes | the streaming write (`application/octet-stream`, `X-Relayfile-*` headers) |
| `GET /fs/events` | `evt_<n>` cursors, asc/desc; an evicted cursor answers 410 `cursor_expired` + `action: full_resync` |
| `GET /fs/ws` | actor WebSocket; catch-up from `cursor`, `from=now`, `path` filters, inline content ≤ 1 MiB |
| `GET /fs/export?format=json`, `GET /fs/query` | |
| `GET /ops`, `GET /ops/{id}` | |
| `GET /writeback/pending`, `POST /writeback/{id}/ack` | pull-based writeback for provider workers |
| `POST /webhooks/ingest`, `POST /v1/internal/webhook-envelopes` | provider events into files (bearer `sync:trigger`, or the HMAC internal path) |
| `GET /sync/status` | |

Not served (404, which every client already treats as "unavailable"): `fs/merge`
(mount falls back to a conflict artifact), `sync/checkpoint-seals*` (only the
explicit checkpoint/seal teardown commands use them), forks, GitHub tarball
import/export (`format=tar`), dead-letter admin routes.

## Provider integrations without hosted-only services

The hosted product runs OAuth and syncs through Nango and writes back from
Cloudflare queues. Self-host uses the provider-neutral paths that already exist
in the API:

- **Inbound**: any adapter or sync worker posts provider events to
  `POST /v1/workspaces/{ws}/webhooks/ingest` (bearer with `sync:trigger`) or the
  HMAC-signed `POST /v1/internal/webhook-envelopes`, or writes files with
  `/fs/bulk`. Envelopes are deduplicated by `(provider, delivery_id)` and
  applied inside the workspace actor in the same transaction.
- **Writeback**: list providers whose writes an external worker performs in
  `RELAYFILE_EXTERNAL_WRITEBACK_PROVIDERS` (e.g. `github,notion,linear,slack`).
  Their writes stay `pending` until the worker polls
  `GET /writeback/pending` and acks `POST /writeback/{id}/ack`
  (`success: false` dead-letters the op). Writes under any other provider
  complete at once, as on the Go server without provider adapters, so mount
  outboxes never wait on a writeback nobody performs.

## Multi-replica safety

- Gateways are stateless; run any number behind a Service/LB.
- durable-actors guarantees one live owner per actor id, so each workspace's
  writes are serialized in its actor; every call is one SQLite transaction.
  Proven: 40 concurrent `If-Match: 0` creates of one path through two gateway
  replicas produce exactly one 202 and 39 409s and one `file.created` event.
- Blobs are content-addressed and written create-only, so replicas uploading
  the same bytes converge on one object.

## Env contract

### `relayfile-actors` image (`docker/actors.Dockerfile`)

`gateway` (default command):

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8080` | listen port |
| `DURABLE_ACTORS_CONTROL_PLANE_URL` | required | control-plane origin, e.g. `http://<release>-durable-actors.<ns>.svc.cluster.local:7100` |
| `DURABLE_ACTORS_PROJECT_ID` | `relayfile` | durable-actors project holding the `Workspace` actors |
| `RELAYFILE_ACTORS_BROKER_URL`, `RELAYFILE_ACTORS_BROKER_TOKEN` | production | where to get sessions and socket grants |
| `DURABLE_ACTORS_SECRET` | refused | only with `RELAYFILE_ACTORS_ALLOW_ADMIN_KEY=true`, for a dev-runtime sidecar in the same pod |
| `RELAYAUTH_JWKS_URL` and/or `RELAYFILE_JWT_SECRET` | one required | RS256 via relayauth JWKS; HS256 shared secret |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | unset | enables `/v1/internal/webhook-envelopes` |
| `RELAYFILE_EXTERNAL_WRITEBACK_PROVIDERS` | empty | comma list (or `*`) of providers with an external writeback worker |
| `RELAYFILE_BLOB_STORE` | `inline` | `gcs`, `s3`, or `inline` (bytes in actor SQLite, max 10 MiB per file; dev only) |
| `RELAYFILE_BLOB_BUCKET`, `RELAYFILE_BLOB_PREFIX` | | object location; keys are `<prefix>blobs/sha256/<aa>/<sha256>` |
| `RELAYFILE_BLOB_INLINE_MAX_BYTES` | `65536` | larger decoded content goes to the blob store |
| `STORAGE_EMULATOR_HOST` | | gcs: fake-gcs-server; otherwise credentials come from the metadata server (Workload Identity) |
| `RELAYFILE_S3_ENDPOINT`, `RELAYFILE_S3_REGION`, `RELAYFILE_S3_FORCE_PATH_STYLE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | | s3: MinIO, AWS, R2, GCS-HMAC |
| `RELAYFILE_MAX_BODY_BYTES` | `67108864` | request body cap |

`session-broker`: `PORT` (8090), `DURABLE_ACTORS_CONTROL_PLANE_URL`,
`DURABLE_ACTORS_PROJECT_ID`, `DURABLE_ACTORS_SECRET` (the admin api-key),
`RELAYFILE_ACTORS_BROKER_TOKEN` (≥ 32 chars, must differ from the admin key).
Run it internal-only, reachable from gateway pods only.

### `relayfile-actors-workspace` image (`docker/actors-workspace.Dockerfile`)

The actor project plus the durable-actors SDK compiler on the published
durable-actors TypeScript runtime.

- `deploy` (production Job): `DURABLE_ACTORS_CONTROL_PLANE_URL`,
  `DURABLE_ACTORS_SECRET` (admin), `DURABLE_ACTORS_PROJECT_ID`,
  `DURABLE_ACTORS_ORGANIZATION_ID`, `DURABLE_ACTORS_ARTIFACT_BUCKET` (the
  durable-actors chart's `storage.bucket`; write access via Workload Identity),
  optional `DURABLE_ACTORS_SECRET_REFS`. Compiles `src/actors.ts`, uploads
  `actors.mjs` create-only to
  `durable-actors/v3/artifacts/<uuid-from-sha256>/actors.mjs`, and registers it
  with `PUT /v1/projects/{project}/deployment`. Idempotent.
- `dev` (default; kind/CI only): `durable-actors dev` with
  `DURABLE_ACTORS_SECRET`, `DURABLE_ACTORS_PORT` (7100),
  `DURABLE_ACTORS_PROJECT_ID` (`local`), `DURABLE_ACTORS_DATA_DIR` (`/data/actors`).
  It binds 127.0.0.1, so it must share the gateway's network namespace.

### Workspace actor

No environment and no secrets: deployments need no `secretRefs`.

## Develop and test

```sh
npm run test --workspace=packages/durable-actors      # gateway + runtime on node:sqlite
npm run typecheck --workspace=packages/durable-actors

# Real durable-actors dev runtime + gateway sidecar in docker, then the Go mount e2e:
docker build -f docker/actors-workspace.Dockerfile -t relayfile-actors-workspace:local .
docker build -f docker/actors.Dockerfile -t relayfile-actors:local .
docker run -d --name rf-da -p 127.0.0.1:28480:8080 -e DURABLE_ACTORS_SECRET=<dev key> relayfile-actors-workspace:local
docker run -d --name rf-gw --network container:rf-da -e DURABLE_ACTORS_CONTROL_PLANE_URL=http://127.0.0.1:7100 \
  -e DURABLE_ACTORS_PROJECT_ID=local -e DURABLE_ACTORS_SECRET=<dev key> -e RELAYFILE_ACTORS_ALLOW_ADMIN_KEY=true \
  -e RELAYFILE_JWT_SECRET=<hs256 secret> relayfile-actors:local
go build -o relayfile-mount ./cmd/relayfile-mount
BASE_URL=http://127.0.0.1:28480 TOKEN=<hs256 jwt with wks/sub/aud=relayfile/scopes> WORKSPACE=ws_e2e \
  MOUNT_BIN=./relayfile-mount WORK_DIR=/tmp/rf-e2e RESTART_CMD="docker restart rf-da rf-gw" \
  bash packages/durable-actors/test/e2e/mount-e2e.sh
```

Helm: `charts/relayfile` `runtime.backend: durable-actors` and the
`agent-relay` umbrella in AgentWorkforce/helm-charts
(`docs/self-hosted-durable-actors.md`).

## Known gaps

- Upstream durable-actors has no invoke-only API key yet; the session broker
  keeps the admin key off the internet-facing gateway in the meantime.
- Blob garbage collection: replaced or deleted file content stays in the
  bucket (content-addressed, never rewritten). A periodic sweep that diffs
  live `blob_sha256` refs against the bucket is follow-up work.
- No migration from the Go server's Postgres state: actor mode is a
  fresh-install backend (re-sync providers or re-import files).
