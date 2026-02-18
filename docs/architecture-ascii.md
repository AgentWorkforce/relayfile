# RelayFile Architecture (ASCII)

## 1) System Topology

```text
                           (optional)
                    +----------------------+
                    | Notion Bridge/Proxy |
                    +----------+-----------+
                               |
                               v
+----------------+      +------+---------------------+       +----------------------+
| Notion API     |<---->| relayfile (HTTP API+Store)|<----->| Postgres (5438 host) |
| (read import + |      | :8080 inside container     |       | docker volume        |
|  writeback)    |      +------+---------------------+       +----------+-----------+
+--------+-------+             ^                                         |
         |                     | REST/JWT                                |
         |                     v                                         |
+--------+-------------------+-------------------------------------------+
| mountsync container (relayfile-mount loop)                             |
| - pull remote tree/events/files                                         |
| - push local edits with If-Match                                        |
| - keeps .relayfile-mount-state.json                                     |
+------------------------+------------------------------------------------+
                         |
                         v bind mount
                  +------+------------------+
                  | ./.livefs on host      |
                  | (virtual FS mirror)    |
                  +------+------------------+
                         ^
                         |
               local agent / Claude CLI reads+writes files
```

## 2) Inbound Flow (Provider -> RelayFile)

```text
import-notion-pages.sh (or webhook source)
  -> send-internal-envelope.sh (HMAC signed)
  -> POST /v1/internal/webhook-envelopes
  -> auth + replay-window verification
  -> IngestEnvelope():
       - dedupe by delivery key
       - coalesce by object/path window
       - enqueue envelope id
  -> envelope worker:
       - adapter.ParseEnvelope() -> ApplyAction[]
       - apply file upsert/delete (+ semantics)
       - emit events + ingress counters
       - retry, then dead-letter on failure
```

## 3) Agent Read/Traverse Flow

```text
agent reads ./.livefs
  -> mountsync ListTree/ListEvents/ReadFile from relayfile
  -> relayfile enforces JWT scopes + file permission policy
  -> mountsync materializes local files + tracks revisions/hashes
  -> agent sees normal files/dirs
```

## 4) Agent Write Flow

```text
agent edits ./.livefs/file.md
  -> mountsync detects hash/revision change
  -> PUT /v1/workspaces/{ws}/fs/file?path=...
     If-Match: <known revision>
  -> Store.WriteFile():
       - optimistic concurrency check
       - new revision + op pending + event
       - enqueue writeback task
  -> writeback worker:
       - adapter.ApplyWriteback()
       - provider HTTP call (Notion client)
       - success -> op succeeded
       - failure -> retry -> dead_lettered
```

## 5) Persistence Layout

```text
State backend snapshot (single logical state object):
  - workspaces
  - files (content, revision, semantics)
  - events
  - operations
  - envelope indexes
  - ingress counters
  - dead letters
  - retry/suppression bookkeeping

Queue backend:
  - envelope queue items
  - writeback queue items
```

```text
Production profile (Postgres):
  relayfile_state
  relayfile_envelope_queue
  relayfile_writeback_queue

Durable-local profile (files):
  .relayfile/state.json
  .relayfile/envelope-queue.json
  .relayfile/writeback-queue.json

Mount local state:
  ./.livefs/.relayfile-mount-state.json
```

## 6) Permission Evaluation (Current)

```text
Effective rules = inherited marker rules + file rules

Inherited marker:
  - any ancestor dir may contain .relayfile.acl
  - marker semantics.permissions apply to descendants

Rule forms:
  allow: scope:<name> | agent:<name> | workspace:<id> | public|any|*
  deny:  scope:<name> | agent:<name> | workspace:<id>

Decision:
  1) if any matching deny rule -> DENY
  2) else if any matching allow rule -> ALLOW
  3) else if no enforceable rules present -> ALLOW
  4) else -> DENY
```

## 7) Semantic Primitives

```text
Each file can carry:
  semantics.properties   (key/value attributes)
  semantics.relations    (opaque relation ids)
  semantics.permissions  (policy + metadata tokens)
  semantics.comments     (comment/reference ids)
```
