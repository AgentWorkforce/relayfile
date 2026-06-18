# Sync definition boundary

Self-host Relayfile ships the ingestion plumbing. Customer-specific sync
definitions are a scoped services engagement.

## What is OSS

- Workspace-token ingest: `RelayFileClient.ingestWebhook(...)` sends normalized
  provider events to `POST /v1/workspaces/{workspaceId}/webhooks/ingest`.
- Data readiness: `RelayFileClient.waitForData(workspaceId, provider)` polls
  `/v1/workspaces/{workspaceId}/sync/status` until cloud reports first-sync
  `ready`, or until OSS reports `healthy` with processed-ingest progress
  (`cursor`/`watermarkTs`).
- Adapter contract: `@relayfile/adapter-*` packages define path mapping,
  webhook normalization, writeback payloads, and digest handling.
- Self-host wiring: the developer runs Relayfile, their provider backend
  (Nango, Pipedream, or equivalent), and the webhook/sync worker that calls
  `ingestWebhook`.

## What is scoped work

The Nango/Composio sync definition decides which upstream objects, fields,
permissions, backfill depth, polling cadence, and webhook edge cases are worth
materializing for that customer. Relayfile does not ship a generic, drop-in
sync for every provider because that scope is product-specific.

The default delivery model is:

1. OSS scaffold and SDK APIs provide the plumbing.
2. A paid engagement authors the customer's sync definitions in their provider
   account, usually from a small template.
3. The sync worker emits normalized events through `ingestWebhook`.
4. Applications call `waitForData` before reading `/<provider>/**`.

This keeps the open source runtime reusable while making the customer's data
scope explicit and validated.
