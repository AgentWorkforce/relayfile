# Permissions Cases

Permissions cases protect mount-scoped access and read-only behavior.

## permissions.readonly-honored
Executor: relayfile
Kind: regression
Tags: permissions, acl
Human Review: false

### Message
A persona with read-only access to Linear attempts to write a Linear issue.

### Mock
```json
{
  "acl": [
    { "pathPrefix": "/linear", "access": "read" }
  ],
  "files": {
    "/linear/issues/LIN-42.json": { "id": "LIN-42", "title": "Read-only issue" }
  }
}
```

### Operations
```json
[
  { "op": "read", "path": "/linear/issues/LIN-42.json" },
  { "op": "write", "path": "/linear/issues/LIN-42.json", "content": { "title": "Mutated" } }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- denied by ACL
aclDenied:
- /linear/issues/LIN-42.json
fileContentIncludes:
- {"path":"/linear/issues/LIN-42.json","value":"Read-only issue"}
noFilesModified:
- /linear

### Must
- Permit the read and reject the write.
- Leave the original file untouched after denial.

### Must Not
- Queue writeback for an ACL-denied write.

## permissions.scoped-mount
Executor: relayfile
Kind: regression
Tags: permissions, scope
Human Review: false

### Message
A scoped persona lists the mount root and should only see allowed subtrees.

### Mock
```json
{
  "acl": [
    { "pathPrefix": "/secret", "access": "none" }
  ],
  "files": {
    "/github/repos/AgentWorkforce/relay/issues/1.json": { "number": 1, "state": "open" },
    "/secret/payroll.json": { "classified": true }
  }
}
```

### Operations
```json
[
  { "op": "list", "path": "/" },
  { "op": "list", "path": "/secret" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- github
forbidPhrases:
- payroll
aclDenied:
- /secret
fileExists:
- /github/repos/AgentWorkforce/relay/issues/1.json

### Must
- Hide restricted root entries from ordinary listings.
- Record an ACL denial when the restricted subtree is accessed directly.

### Must Not
- Leak restricted filenames in the visible output.
