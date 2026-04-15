# Canonical File Schema Ownership — Proof Direction

## Status

- Date: 2026-04-15
- Scope: First proof of canonical schema ownership in core relayfile
- Prerequisite: [canonical-file-schema-ownership-boundary.md](canonical-file-schema-ownership-boundary.md)

## Goal

Demonstrate that core relayfile can own, publish, and enforce canonical file schemas without disrupting existing adapter behavior or requiring relayfile-cli changes. The proof is deliberately narrow: one service (GitHub), one file type (issue), end-to-end.

## Proof Scope

| In scope | Out of scope |
|----------|-------------|
| GitHub issue canonical schema (`schemas/github/issue.schema.json`) | Schemas for every service/file type |
| Go type generation from JSON Schema | TypeScript SDK type generation |
| Optional validation utility callable from adapters | Mandatory validation in the write path |
| One example CLI caller conformance test | Integrating validation into relayfile-cli core |
| Path pattern documentation for GitHub issues | Path pattern registry for all services |

## Steps

### 1. Create the schema directory and first schema

Add `schemas/github/issue.schema.json` defining the canonical shape for files at `/github/repos/{owner}/{repo}/issues/{number}.json`.

The schema should capture the minimal stable contract:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://relayfile.dev/schemas/github/issue.schema.json",
  "title": "GitHubIssueFile",
  "type": "object",
  "required": ["number", "title", "state", "created_at", "updated_at"],
  "properties": {
    "number": { "type": "integer" },
    "title": { "type": "string" },
    "state": { "type": "string", "enum": ["open", "closed"] },
    "body": { "type": ["string", "null"] },
    "labels": {
      "type": "array",
      "items": { "type": "string" },
      "default": []
    },
    "assignees": {
      "type": "array",
      "items": { "type": "string" },
      "default": []
    },
    "created_at": { "type": "string", "format": "date-time" },
    "updated_at": { "type": "string", "format": "date-time" }
  },
  "additionalProperties": false
}
```

Design choices:
- **`additionalProperties: false`** — strict. Forces adapters and CLI callers to produce exactly the canonical shape, not a superset. This can be relaxed later if needed.
- **`labels` as `string[]`** — flattened from GitHub's label objects. The canonical schema is agent-friendly, not a mirror of the API response.
- **`snake_case`** — consistent with relayfile conventions, even though GitHub's API uses mixed casing.

### 2. Add a path pattern registry

Create `schemas/README.md` documenting the path pattern → schema mapping:

```
| Path Pattern | Schema | Notes |
|---|---|---|
| `/github/repos/{owner}/{repo}/issues/{number}.json` | `github/issue.schema.json` | Read + write |
```

This registry is the authoritative list of which schemas apply where. It starts with one entry and grows as schemas are added.

### 3. Add a validation utility

Create a lightweight Go function in `internal/relayfile/` (or a new `internal/schema/` package) that validates a `File.Content` against its canonical schema given the file path:

```go
// ValidateContent checks whether content conforms to the canonical
// schema for the given VFS path. Returns nil if no schema is registered
// for the path pattern, or if validation passes.
func ValidateContent(path string, content []byte) error
```

Implementation options:
- Embed JSON Schema files via `//go:embed` and use a Go JSON Schema library (e.g., `santhosh-tekuri/jsonschema`).
- Keep it optional — callers invoke it explicitly; it is not in the write-path hot loop.

### 4. Verify adapter conformance

Write a test that takes a sample GitHub adapter webhook output (from existing test fixtures in `relayfile-adapters` or constructed inline) and validates the resulting `ApplyAction.Content` against the canonical schema.

This test lives in core relayfile, not in relayfile-adapters. It asserts that the expected adapter output shape matches the schema core relayfile defines. If the adapter diverges, this test catches it.

### 5. Verify CLI caller conformance

Write a test that takes a sample `gh issue view --json` output, applies a `FormatFn` mapping (Layer 1 → Layer 2), and validates the result against the canonical schema.

This test demonstrates the conformance pattern from the CLI boundary document. It lives in core relayfile (or as an example in `schemas/examples/`), not in relayfile-cli.

## What Success Looks Like

After the proof:

1. `schemas/github/issue.schema.json` exists and is the single source of truth for issue file shape.
2. A Go validation utility can check any `File.Content` against its canonical schema.
3. Tests prove that both adapter output and CLI-derived output conform to the schema.
4. No code in relayfile-cli or relayfile-adapters was modified — conformance is demonstrated, not enforced by import.

## What This Proof Intentionally Defers

- **Writeback schemas** (what an agent writes to create a review, post a comment, etc.).
- **Schema generation into SDK types** (TypeScript `GitHubIssueFile` interface, Python dataclass).
- **Runtime validation in the write path** (rejecting non-conforming writes at the API level).
- **Schemas for other services** (Slack, Linear, Notion).
- **Schema evolution/migration tooling**.

These are real needs but they are follow-on work. The proof establishes the ownership pattern; the pattern scales to cover these concerns.

## Risk Assessment

**Risk: the canonical schema disagrees with what adapters actually produce.**
Mitigation: the conformance test (step 4) catches this immediately. If the schema and adapter diverge, the schema is adjusted — not the other way around, unless the adapter output is clearly wrong. The schema is authoritative, but it must reflect reality at the time it is published.

**Risk: `additionalProperties: false` is too strict for evolving adapters.**
Mitigation: start strict. Loosening is a non-breaking change. Tightening is breaking. Better to discover missing fields now than to ship a permissive schema that hides shape mismatches.

**Risk: JSON Schema validation adds a dependency.**
Mitigation: the validation utility is optional and not in the write path. The schema files are useful even without runtime validation — they document the contract and enable code generation.
