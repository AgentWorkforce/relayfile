# Self-Host Connect and Ingestion

**Status:** Guide for running Relayfile integrations without Agent Relay Cloud.

Relayfile OSS is the filesystem, auth boundary, ingest endpoint, writeback queue, and mount layer. Provider OAuth and provider **sync definitions** live in your provider stack — Nango, Composio, Pipedream, or a custom `ConnectionProvider`. This guide covers the two halves a self-host developer wires up: **Connect** (auth) and **Ingestion** (data).

## Connect

Hosted Agent Relay ships a polished `connectIntegration()` / `connectNotion()` flow through Cloud. For self-host, `@relayfile/sdk` now ships the same orchestration with **no Cloud dependency** via `SelfHostConnect`, backed by any provider that implements the `ConnectCapableProvider` contract:

```ts
import { SelfHostConnect, type ConnectCapableProvider } from "@relayfile/sdk";

// Implement this around your provider stack. The concrete Nango/Pipedream
// provider-package adapters are tracked as relayfile-providers follow-up work.
const provider: ConnectCapableProvider = {
  name: "nango",
  proxy: async (_request) => {
    throw new Error("Wire this to your provider proxy");
  },
  healthCheck: async (_connectionId) => {
    throw new Error("Wire this to your provider health check");
  },
  createConnectSession: async (_input) => {
    throw new Error("Wire this to your provider connect-session API");
  },
  getConnectionStatus: async (_input) => {
    throw new Error("Wire this to your provider connection-status API");
  },
};

const connect = new SelfHostConnect({
  provider,
  // relayfile slug -> your provider's config key (they are not always equal)
  providerConfigKeys: { github: "github" },
});

// 1. mint a connect session for your end user
const session = await connect.startConnect("github", { endUserId: user.id });
//    → render session.sessionToken in a frontend widget, or redirect to session.connectLink

// 2. wait until upstream OAuth is live (auth-ready)
await connect.waitForConnection("github", { connectionId: session.connectionId });
```

- `SelfHostConnect` orchestrates `startConnect → (user OAuths) → waitForConnection`. The connection ↔ workspace binding is **provider-side** (the `connectionId` is tagged in your Nango/Pipedream account and resolved at proxy time) — there is no Cloud bind route and no internal HMAC dependency.
- `waitForConnection` polls `provider.getConnectionStatus(...)` until the connection reports auth-ready (`oauth_connected`).
- Use the `supportsConnect(provider)` type guard to check a provider implements the capability before calling.
- The `ConnectCapableProvider` contract (`createConnectSession` / `getConnectionStatus`) lives in `@relayfile/sdk`. The concrete `NangoProvider` / `PipedreamProvider` implementations land in [`relayfile-providers`](https://github.com/AgentWorkforce/relayfile-providers) (tracked in [#310](https://github.com/AgentWorkforce/relayfile/issues/310)); any provider satisfying the contract works today.

## Ingestion

Auth-ready is not data-ready. After connect, a provider sync still has to backfill and materialize records into the VFS. Two SDK primitives cover the self-host ingest path:

```ts
// Push normalized provider events into the VFS with a workspace token (no internal HMAC).
await client.ingestWebhook(/* workspaceId, normalized event */);

// Wait until provider data is safe to read.
await client.waitForData(workspaceId, "github");
```

- **`ingestWebhook`** posts to the token-authed `POST /v1/workspaces/{workspaceId}/webhooks/ingest`. The receiver can be `@relayfile/webhook-server`, a Nango sync-webhook handler, a Composio trigger handler, or your own service.
- **`waitForData`** polls `/v1/workspaces/{workspaceId}/sync/status`. Cloud-managed syncs report first-sync completion with `ready`; OSS self-host reports provider health as `healthy`, so the SDK only treats `healthy` as data-ready after the status row carries processed-ingest progress (`cursor` or `watermarkTs`). If your OSS deployment ingests through custom webhooks or sync workers, gate on your own ingest completion when you need stronger proof than observed provider progress.

The end-to-end self-host shape: `connect → backfill + live webhooks → waitForData → agent reads the VFS`. Full reference tracked in [#311](https://github.com/AgentWorkforce/relayfile/issues/311).

## Provider sync coverage

A provider only **backfills** into the VFS if a sync definition ships for it. An adapter alone gives path-mapping + writeback, not backfill — picking a path-mapping-only provider and getting an empty tree is the common DX trap.

**Sync-ready** (adapter + shipped sync definition → initial backfill + live events): `confluence`, `daytona`*, `docker-hub`†, `dropbox`, `fathom`, `gcp`, `github`, `gitlab`, `gmail`, `google-calendar`, `granola`, `hubspot`, `jira`, `linear`, `neon`, `notion`, `recall`, `reddit`†, `slack`, `x`.

**Path-mapping / writeback-only** (adapter exists, **no** shipped sync def → no backfill; ingest only what you push via `ingestWebhook`): `airtable`, `asana`, `azure-blob`, `box`, `calendly`, `clickup`, `gcs`, `google-drive`, `intercom`, `mailgun`, `mixpanel`, `onedrive`, `pipedrive`, `postgres`, `redis`, `s3`, `salesforce`, `segment`, `sendgrid`, `sharepoint`, `shopify`, `stripe`, `teams`, `zendesk`.

†Composio-backed; the rest are Nango. Adapters live in [`relayfile-adapters`](https://github.com/AgentWorkforce/relayfile-adapters); sync definitions ship behind the [paid boundary](#paid-sync-definition-boundary).

## Responsibility matrix

| Layer | Current OSS surface | Hosted Agent Relay | Self-host responsibility |
|---|---|---|---|
| VFS API, revisions, ACL checks | yes | managed | run Relayfile |
| Scoped token issuing | relayauth-compatible, or relaycontrol introspection | managed | run relayauth or equivalent, or [relaycontrol introspection](#auth-without-relayauth-relaycontrol-introspection) |
| Agent framework tools | `@relayfile/agents` | same package | same package |
| Connect orchestration | `SelfHostConnect` (`@relayfile/sdk`) | managed connect flow | provide a `ConnectCapableProvider` + frontend widget |
| Provider OAuth + connection binding | provider-side (`connectionId`) | managed | run Nango/Pipedream; tag connection |
| Generic event ingest | `ingestWebhook` / `/webhooks/ingest` | managed workers call ingest | run webhook/sync handlers |
| Data-ready gate | `waitForData` / `/sync/status` | managed per provider | call before reads |
| Sync definitions | not shipped as generic defaults | managed by Agent Relay | scoped engagement |
| Writeback queue and op status | yes | managed | run provider workers |

## Auth without RelayAuth (relaycontrol introspection)

By default relayfile verifies every bearer token as an RS256 JWT against
RelayAuth's JWKS (`RELAYAUTH_JWKS_URL`, falling back to
`https://api.relayauth.dev/.well-known/jwks.json`). A self-host cluster that
runs relaycontrol can instead have relayfile ask relaycontrol about each token
through its in-cluster introspection endpoint, so no RelayAuth deployment (and
no egress to `api.relayauth.dev`) is needed.

### Modes

| `RELAYFILE_AUTH_MODE` | Behaviour |
|---|---|
| `jwks` (default) | Unchanged hosted behaviour: RS256 JWTs verified against JWKS. |
| `introspection` | Every bearer is sent, exactly as presented, to relaycontrol. JWKS is never fetched and `RELAYAUTH_JWKS_URL` is ignored. |
| `dual` | Tokens starting with a relaycontrol prefix (`sh_at_` user access tokens, `sh_wk_` workspace API tokens) go to introspection only; JWT-shaped tokens (three dot-separated parts after an optional `relay_*_` prefix) go to JWKS only; anything else is 401. There is no cross-fallback. Requires an explicit `RELAYAUTH_JWKS_URL` (no default), so an air-gapped cluster never dials `api.relayauth.dev`. |

When `RELAYFILE_AUTH_MODE` is unset the mode is derived: only
`RELAYCONTROL_INTERNAL_URL` set means `introspection`; both it and
`RELAYAUTH_JWKS_URL` set means `dual`; otherwise `jwks`. With none of the new
variables set, behaviour is identical to earlier releases. The chosen mode and
the introspection endpoint host are logged at startup; the secret never is.

### Environment

| Variable | Purpose |
|---|---|
| `RELAYFILE_AUTH_MODE` | `jwks`, `introspection` or `dual` (see above). An unknown value refuses to start. |
| `RELAYCONTROL_INTERNAL_URL` | relaycontrol's in-cluster base URL, e.g. `http://relaycontrol.relay.svc.cluster.local:8080`. `/internal/v1/auth/introspect` is appended unless already present. Must be `http`/`https`, without userinfo, query or fragment. |
| `RELAYCONTROL_INTROSPECT_SECRET` | Service credential, sent as `Authorization: Bearer <secret>`. Must equal relaycontrol's `RELAYCONTROL_INTROSPECT_SECRET`. |
| `RELAYCONTROL_INTROSPECT_SECRET_FILE` | Alternative to the above: path to a file holding the secret (contents trimmed). Setting both refuses to start. |
| `RELAYFILE_PUBLIC_ORIGIN` | relayfile's public origin (for example `https://files.example.com`), sent as `aud`. relaycontrol must list it in its introspection audiences (`SELFHOST_INTROSPECT_AUDIENCES`); relaycontrol's `GET /readyz` reports the accepted origins under `introspection.audiences` so a mismatch can be checked before traffic (see relay-control `docs/identity.md`). Normalized to `scheme://host[:port]`, lowercase, default port dropped. |
| `RELAYFILE_INTROSPECTION_TIMEOUT` | Per-call timeout, Go duration (default `3s`). |
| `RELAYFILE_INTROSPECTION_CACHE_TTL` | Positive-verdict cache TTL, Go duration (default `60s`, capped at `60s`; `0` disables the cache). |

`introspection` and `dual` refuse to start unless the URL, secret and public
origin are all set and valid.

### Contract

Matches AgentWorkforce/relay-control `main` (`src/auth/introspect.ts` and
`docs/identity.md`, verified against v0.5.6 at `901c9cd`), including the
`relayfile-agent` token class:

- Request: `POST {RELAYCONTROL_INTERNAL_URL}/internal/v1/auth/introspect` with
  `Content-Type: application/json` and body `{"token": "<bearer>", "aud":
  "<RELAYFILE_PUBLIC_ORIGIN>"}`. relayfile does not send `workspace` or
  `machine`; the response `workspace` is authoritative and is what the token is
  bound to. Tokens over 8 KiB or containing whitespace/control characters are
  rejected with 401 without calling relaycontrol.
- `200 {"active": true, ...}`: `workspace` becomes the token's workspace,
  `agent_name` (when non-null) or else `sub` becomes the principal, `scopes`
  are used as-is, `aud` must match `RELAYFILE_PUBLIC_ORIGIN`, and `exp` must be
  in the future. Scopes are never widened or mapped.
- `200 {"active": false, "reason": ...}`: 401 `token inactive`. Only the
  reason is logged.

### Fail-closed behaviour

Only a well-formed `active: true` answer authenticates a request. A 401 from
relaycontrol (wrong or rotated service secret) becomes 503 with a WARNING log;
a 404 (endpoint disabled), 400, any 5xx, a timeout, a transport error, a
redirect, a response over 64 KiB, or a body that is not a JSON object with a
boolean `active` all become 503. Tokens, the secret and request/response
bodies are never logged.

### Caching and revocation lag

Active verdicts are cached in memory, keyed by a SHA-256 of the token and
audience, for at most `RELAYFILE_INTROSPECTION_CACHE_TTL` (60s cap) and never
past the token's `exp`. Inactive verdicts and errors are never cached. A token
revoked in relaycontrol can therefore keep working against relayfile for up to
60 seconds. The cache holds at most 10,000 entries.

### Desktop-user tokens are not filesystem authority (yet)

relaycontrol v1 user access tokens (`sh_at_`, `principal_type:
"desktop-user"`) carry only relaycontrol scopes such as `cli:auth`,
`rth:read` and `rth:sync`, never `fs:*` or `sync:*`. relayfile introspects them
successfully but refuses them with 403 (`missing required scope: fs:read`, or
`no scopes granted` when the list is empty) on every filesystem and sync
route: `cli:auth` alone is not filesystem authority. Workspace binding and
path-scoped grant checks are identical for both verifiers.

### Agent tokens (`relayfile-agent` workspace API tokens)

Filesystem authority for self-host agents comes from a relaycontrol
workspace API token (`sh_wk_`) minted with purpose `relayfile-agent`
(relay-control `docs/identity.md`): `POST
/api/v1/workspaces/{workspace}/deployment-api-tokens` with `purpose:
"relayfile-agent"`, a `label`, an `agent_name` (unique per workspace while a
live token exists; relaycontrol answers 409 `agent_name_conflict`) and `aud`
set to `RELAYFILE_PUBLIC_ORIGIN`. relaycontrol introspects it as
`principal_type: "agent"` with `sub` = `token_id`, `agent_name` as the
relayfile principal, and the fixed scope list `fs:read fs:write sync:read
sync:trigger`. Agent tokens never carry `admin:*`; admin routes stay 403.

`exp` is the token's `expires_at` (default 90 days). A token minted with
`neverExpires: true` answers `exp = now + 3600` recomputed on every call, so
relayfile's positive cache (capped at `exp` and at most 60s) re-introspects
well inside that window. Revocation, expiry, or removing relayfile's origin
from `SELFHOST_INTROSPECT_AUDIENCES` turn the token inactive (`revoked`,
`expired`, `audience`) within the cache TTL.

### Out of scope

`packages/durable-actors` has its own JWT verification and does not use this
mode; it still needs a JWKS or its own configuration.

## Paid sync-definition boundary

The plumbing is OSS; the sync definitions are scoped work. A Nango/Composio sync definition decides *which objects, fields, depth, cadence, and deletion semantics* become files — choices that are customer-specific, so this repo ships the ingest scaffolding and examples, not a universal drop-in sync.

- **Included as OSS:** Relayfile server, SDK client, `SelfHostConnect`, generic ingest, `waitForData`, writeback queue, mount, agent tools, scaffolding.
- **Scoped engagement:** authoring and validating the provider sync definitions for the customer's required data.

Full detail: [`SYNC-DEFINITION-BOUNDARY.md`](./SYNC-DEFINITION-BOUNDARY.md).

## Related docs

- [`SDK-SURFACE.md`](./SDK-SURFACE.md) — authoritative shipped SDK + Cloud contract.
- [`SYNC-DEFINITION-BOUNDARY.md`](./SYNC-DEFINITION-BOUNDARY.md) — the OSS-vs-paid boundary in detail.
- [`docs/api-reference.md`](../api-reference.md) — ingest + sync-status endpoint shapes.
- [`docs/guides/cloud-integration.md`](../guides/cloud-integration.md) — existing connection IDs and provider ownership.
