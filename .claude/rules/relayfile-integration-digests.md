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

## yesterday.md immutability and the workspace-local boundary

- `/digests/yesterday.md` is the closing-window artifact for the prior local
  calendar day. The rotation boundary is **workspace-local midnight in the
  configured TZ**, not UTC.
- After the close write for a given local day produces `yesterday.md`,
  subsequent ingest events for *the same local day* must NOT mutate the file.
  The implementation pins the close via the per-workspace marker
  `.relay/digests/yesterday.lock`; the marker contains the closed
  `YYYY-MM-DD`, and a re-close call for that same date short-circuits with
  `Skipped=true`. The marker lives under `.relay/` (not `/digests/.state/`)
  because a marker write inside `/digests/` would surface as a `/digests/*`
  file event and re-enter digest regeneration. The mount watcher already
  excludes `.relay/`, so the marker is invisible to regen and cannot recurse.
- Rotation forward is the only permitted overwrite. When the local day
  advances, the close pipeline writes the new day's content atomically
  (temp + rename) and overwrites the marker.
- Self-host closing-window rendering is exhaustive: `RunClosing` emits every
  source event in the closed local day and must not silently truncate. The
  historical coverage-cap identifiers (`digest.DigestCoverageCap` and
  `digest.WarningCoverageTruncated`) stay stable for downstream parsers and
  future cloud-side policy code; if a caller outside the shared self-host
  renderer engages a cap, it must surface
  `digest.coverage.truncated events_seen=N events_emitted=M` in
  `Meta.Warnings`.
- Scheduler responsibility: a tick-driven scheduler closes any local day
  that has elapsed since the marker. On restart, every missing day is
  closed in chronological order before normal operation resumes.

## SDK/API contract

If an API or SDK change affects `DigestContext`, `DigestHandler`, change-event
shape, or digest file paths, update all SDKs and the OpenAPI spec in the same
change.
