# Writeback Cases

Writeback cases model the file-native adapter contract from relayfile-adapters
issue 45.

## writeback.patch-canonical
Executor: relayfile
Kind: regression
Tags: writeback, patch
Human Review: false

### Message
Write to a canonical issue JSON file and queue a PATCH writeback.

### Mock
```json
{
  "files": {
    "/linear/issues/LIN-42.json": { "id": "LIN-42", "title": "Original", "description": "before" }
  }
}
```

### Operations
```json
[
  {
    "op": "write",
    "path": "/linear/issues/LIN-42.json",
    "content": { "description": "Updated during rollout validation" },
    "writeback": { "adapter": "linear", "mode": "patch" }
  },
  { "op": "list", "path": "/linear/issues" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- LIN-42.json
fileExists:
- /linear/issues/LIN-42.json
fileContentIncludes:
- {"path":"/linear/issues/LIN-42.json","value":"Updated during rollout validation"}
writebackQueued:
- {"adapter":"linear","path":"/linear/issues/LIN-42.json"}
forbidPhrases:
- new.json

### Must
- Treat canonical id files as adapter PATCH operations.

### Must Not
- Create a duplicate issue file for a canonical write.

## writeback.create-via-draft
Executor: relayfile
Kind: regression
Tags: writeback, create
Human Review: false

### Message
Write a non-canonical draft issue file and rewrite it as a pointer receipt.

### Mock
```json
{
  "files": {
    "/linear/issues/.schema.json": { "required": ["teamId", "title"] }
  }
}
```

### Operations
```json
[
  {
    "op": "write",
    "path": "/linear/issues/file-native-validation.json",
    "content": { "teamId": "team-1", "title": "File-native writeback validation" },
    "writeback": { "adapter": "linear", "mode": "create" },
    "createReceipt": {
      "path": "/linear/issues/LIN-203.json",
      "url": "https://linear.app/agentworkforce/issue/LIN-203"
    }
  }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- created receipt
fileExists:
- /linear/issues/file-native-validation.json
fileContentIncludes:
- {"path":"/linear/issues/file-native-validation.json","value":"\"created\": true"}
- {"path":"/linear/issues/file-native-validation.json","value":"LIN-203.json"}
writebackQueued:
- {"adapter":"linear","path":"/linear/issues/file-native-validation.json"}

### Must
- Queue a create writeback for non-canonical filenames.
- Leave a durable receipt at the draft path.

### Must Not
- Pretend the draft filename is the canonical adapter id.

## writeback.readonly-rejected
Executor: relayfile
Kind: regression
Tags: writeback, validation
Human Review: false

### Message
Write a read-only field to a canonical file and surface the validation failure.

### Mock
```json
{
  "files": {
    "/linear/issues/LIN-99.json": { "id": "LIN-99", "title": "Immutable id" }
  },
  "readOnlyFields": {
    "/linear/issues": ["id"]
  }
}
```

### Operations
```json
[
  {
    "op": "write",
    "path": "/linear/issues/LIN-99.json",
    "content": { "id": "different", "title": "Attempted mutation" },
    "writeback": { "adapter": "linear", "mode": "patch" }
  },
  { "op": "read", "path": "/.relay/writeback-status.json" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- ReadOnlyFieldError
fileContentIncludes:
- {"path":"/.relay/writeback-status.json","value":"ReadOnlyFieldError"}
- {"path":"/linear/issues/LIN-99.json","value":"Immutable id"}
fileNotExists:
- /linear/issues/different.json

### Must
- Reject read-only field mutation before queueing writeback.
- Make the validation reason visible through writeback status.

### Must Not
- Mutate the canonical file after validation fails.

## writeback.nested-readonly-rejected
Executor: relayfile
Kind: regression
Tags: writeback, validation, nested
Human Review: false

### Message
Write a nested read-only metadata field and surface the validation failure.

### Mock
```json
{
  "files": {
    "/github/repos/AgentWorkforce/relay/issues/114.json": {
      "number": 114,
      "metadata": { "id": "original-node-id" },
      "title": "Eval harness"
    }
  },
  "readOnlyFields": {
    "/github/repos/AgentWorkforce/relay/issues": ["metadata.id"]
  }
}
```

### Operations
```json
[
  {
    "op": "write",
    "path": "/github/repos/AgentWorkforce/relay/issues/114.json",
    "content": { "metadata": { "id": "changed-node-id" }, "title": "Attempted mutation" },
    "writeback": { "adapter": "github", "mode": "patch" }
  },
  { "op": "read", "path": "/.relay/writeback-status.json" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- ReadOnlyFieldError
fileContentIncludes:
- {"path":"/.relay/writeback-status.json","value":"metadata.id"}
- {"path":"/github/repos/AgentWorkforce/relay/issues/114.json","value":"original-node-id"}

### Must
- Reject nested read-only field mutation before queueing writeback.

### Must Not
- Only enforce top-level read-only fields.
