---
description: Runtime ingest paths must update Relayfile digest artifacts
paths:
  - internal/relayfile/**
  - internal/httpapi/**
  - internal/mountsync/**
  - cmd/relayfile-cli/**
  - packages/sdk/**
---

# Relayfile integration digests

Digest files are first-class Relayfile artifacts. Provider mutations must keep
`/digests/today.md` and `/digests/yesterday.md` in sync with the event log.

## Runtime requirements

- New ingest or mutation paths must append filesystem events before digest
  regeneration runs.
- Digest writes must be visible to mounted workspaces as normal file events.
- Digest generation must ignore `/digests/*` source events so digest writes do
  not recurse forever or count themselves as provider activity.
- Terminal provider states must remain readable as records. Do not collapse
  closed, merged, archived, completed, canceled, or resolved provider objects
  into file deletes unless the upstream object was actually deleted.
- Tests must cover the mutation event, the generated digest content, and the
  non-recursion behavior for `/digests/*`.

## SDK/API contract

If an API or SDK change affects `DigestContext`, `DigestHandler`, change-event
shape, or digest file paths, update all SDKs and the OpenAPI spec in the same
change.
