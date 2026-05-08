# Concurrency Cases

These cases model the README differentiator: multiple agents share one live
filesystem view.

## concurrency.realtime-sync
Executor: relayfile
Kind: regression
Tags: concurrency, realtime
Human Review: false

### Message
Agent A writes to the mount and Agent B reads the same path within one second.

### Mock
```json
{
  "files": {
    "/shared/status.json": { "state": "initial", "writer": "seed" }
  }
}
```

### Operations
```json
[
  { "id": "agent-a", "op": "write", "path": "/shared/status.json", "content": { "state": "ready", "writer": "agent-a" } },
  { "op": "sleep", "ms": 50 },
  { "id": "agent-b", "op": "read", "path": "/shared/status.json" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- agent-b
- ready
fileContentIncludes:
- {"path":"/shared/status.json","value":"agent-a"}
maxToolCalls: 3

### Must
- Make Agent A's write visible to Agent B without an explicit refresh.

### Must Not
- Return the seed value to Agent B after Agent A writes.

## concurrency.no-stale-after-write
Executor: relayfile
Kind: regression
Tags: concurrency, cache
Human Review: false

### Message
Write twice in quick succession and read the final version.

### Mock
```json
{
  "files": {
    "/shared/counter.txt": "version=0\n"
  }
}
```

### Operations
```json
[
  { "op": "write", "path": "/shared/counter.txt", "content": "version=1\n" },
  { "op": "write", "path": "/shared/counter.txt", "content": "version=2\n" },
  { "op": "read", "path": "/shared/counter.txt" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- version=2
forbidPhrases:
- version=0
fileContentEquals:
- {"path":"/shared/counter.txt","value":"version=2\n"}
minToolCalls: 3

### Must
- Invalidate stale reads after rapid write-through updates.

### Must Not
- Surface an intermediate version as the final read.
