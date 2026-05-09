# VFS Contract Cases

These cases pin the basic filesystem promises an agent depends on when it uses
relayfile as a mount.

## vfs-contracts.read-then-write
Executor: relayfile
Kind: regression
Tags: vfs, read, write
Human Review: false

### Message
Read a note, update it, and read it back from the relayfile mount.

### Mock
```json
{
  "files": {
    "/notes/todo.md": "status: draft\nowner: agent\n"
  },
  "metadata": {
    "/notes/todo.md": { "etag": "seed-1", "adapter": "fixture" }
  }
}
```

### Operations
```json
[
  { "op": "read", "path": "/notes/todo.md" },
  { "op": "write", "path": "/notes/todo.md", "content": "status: reviewed\nowner: agent\n" },
  { "op": "read", "path": "/notes/todo.md" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- reviewed
fileExists:
- /notes/todo.md
fileContentIncludes:
- {"path":"/notes/todo.md","value":"status: reviewed"}
fileMetadata:
- {"path":"/notes/todo.md","key":"adapter","value":"fixture"}
minToolCalls: 3

### Must
- Preserve ordinary read-write-read semantics through the mount.
- Keep relayfile metadata visible alongside file content.

### Must Not
- Return stale content after the write.

## vfs-contracts.list-after-create
Executor: relayfile
Kind: regression
Tags: vfs, list, create
Human Review: false

### Message
Create a file and list its parent directory.

### Mock
```json
{
  "files": {
    "/projects/.keep": ""
  }
}
```

### Operations
```json
[
  { "op": "write", "path": "/projects/launch-plan.md", "content": "Launch checklist\n" },
  { "op": "list", "path": "/projects" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- launch-plan.md
fileExists:
- /projects/launch-plan.md
fileContentEquals:
- {"path":"/projects/launch-plan.md","value":"Launch checklist\n"}
toolCallsInclude:
- list

### Must
- Show newly created files in the parent listing.

### Must Not
- Require a refresh operation before the listing reflects the create.

## vfs-contracts.delete-removes
Executor: relayfile
Kind: regression
Tags: vfs, delete
Human Review: false

### Message
Delete a file and verify the parent directory no longer lists it.

### Mock
```json
{
  "files": {
    "/tmp/remove-me.txt": "obsolete\n",
    "/tmp/keep-me.txt": "still here\n"
  }
}
```

### Operations
```json
[
  { "op": "delete", "path": "/tmp/remove-me.txt" },
  { "op": "list", "path": "/tmp" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- keep-me.txt
fileNotExists:
- /tmp/remove-me.txt
fileExists:
- /tmp/keep-me.txt
toolCallsInclude:
- delete

### Must
- Remove the deleted path from the final mount snapshot.

### Must Not
- Hide unrelated sibling files.
