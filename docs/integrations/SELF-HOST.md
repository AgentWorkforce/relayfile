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
| Scoped token issuing | relayauth-compatible | managed | run relayauth or equivalent |
| Agent framework tools | `@relayfile/agents` | same package | same package |
| Connect orchestration | `SelfHostConnect` (`@relayfile/sdk`) | managed connect flow | provide a `ConnectCapableProvider` + frontend widget |
| Provider OAuth + connection binding | provider-side (`connectionId`) | managed | run Nango/Pipedream; tag connection |
| Generic event ingest | `ingestWebhook` / `/webhooks/ingest` | managed workers call ingest | run webhook/sync handlers |
| Data-ready gate | `waitForData` / `/sync/status` | managed per provider | call before reads |
| Sync definitions | not shipped as generic defaults | managed by Agent Relay | scoped engagement |
| Writeback queue and op status | yes | managed | run provider workers |

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
