# Canonical File Schema Ownership — Proof Direction

## Status

- Date: 2026-04-15
- Scope: First proof of canonical schema ownership in core relayfile
- Prerequisite: [canonical-file-schema-ownership-boundary.md](canonical-file-schema-ownership-boundary.md)
- State: **Implemented** — all five steps complete

## Goal

Demonstrate that core relayfile can own, publish, and enforce canonical file schemas without disrupting existing adapter behavior or requiring relayfile-cli changes. The proof is deliberately narrow: one service (GitHub), one file type (issue), end-to-end.

## Proof Scope

| In scope | Out of scope |
|----------|-------------|
| GitHub issue canonical schema (`schemas/github/issue.schema.json`) | Schemas for every service/file type |
| Go validation utility with embedded schemas | TypeScript SDK type generation |
| Optional validation callable from adapters and tests | Mandatory validation in the write path |
| Adapter and CLI caller conformance tests | Integrating validation into relayfile-cli core |
| Path pattern registry (`schemas/README.md`) | Path pattern registry for all services |

## Implemented Steps

### 1. Schema directory and first schema — DONE

`schemas/github/issue.schema.json` defines the canonical shape for files at `/github/repos/{owner}/{repo}/issues/{number}/meta.json`.

The schema captures the stable contract with all fields required and nullable where appropriate:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://relayfile.dev/schemas/github/issue.schema.json",
  "title": "GitHubIssueMetaFile",
  "type": "object",
  "required": ["number", "title", "state", "created_at", "updated_at",
               "body", "labels", "assignees", "author", "milestone",
               "closed_at", "html_url"],
  "properties": {
    "number":     { "type": "integer", "minimum": 1 },
    "title":      { "type": ["string", "null"] },
    "state":      { "type": ["string", "null"], "enum": ["open", "closed", null] },
    "body":       { "type": ["string", "null"] },
    "labels":     { "type": "array", "items": { "type": "string" }, "default": [] },
    "assignees":  { "type": "array", "items": { "type": "string" }, "default": [] },
    "author":     {
      "type": "object",
      "required": ["avatarUrl", "login"],
      "properties": {
        "avatarUrl": { "type": ["string", "null"] },
        "login":     { "type": ["string", "null"] }
      },
      "additionalProperties": false
    },
    "milestone":  { "type": ["string", "null"] },
    "created_at": { "type": ["string", "null"], "format": "date-time" },
    "updated_at": { "type": ["string", "null"], "format": "date-time" },
    "closed_at":  { "type": ["string", "null"], "format": "date-time" },
    "html_url":   { "type": "string", "format": "uri" }
  },
  "additionalProperties": false
}
```

Design choices:
- **`additionalProperties: false`** — strict. Forces adapters and CLI callers to produce exactly the canonical shape. Loosening is a non-breaking change if needed later.
- **`labels` as `string[]`** — flattened from GitHub's label objects. Agent-friendly, not an API mirror.
- **`snake_case`** for top-level fields — consistent with relayfile conventions.
- **All fields required, nullable where appropriate** — agents always see the full shape. Missing data is `null`, not absent.

### 2. Path pattern registry — DONE

`schemas/README.md` documents the path pattern to schema mapping:

| Path Pattern | Schema | Access |
|---|---|---|
| `/github/repos/{owner}/{repo}/issues/{number}/meta.json` | `github/issue.schema.json` | Read |

The README also documents schema evolution rules (adding optional fields is non-breaking; removing or renaming fields is breaking) and the strictness escape hatch process.

### 3. Validation utility — DONE

`internal/schema/validate.go` provides:

```go
func ValidateContent(path string, content []byte) error
```

Implementation:
- Embeds JSON Schema files via `schemas/embed.go` (`//go:embed README.md github/*.json`).
- Uses `santhosh-tekuri/jsonschema/v6` with draft 2020-12 and format assertion enabled.
- Path pattern matching via regex: `^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$`.
- Returns `nil` for unknown paths — unregistered paths pass silently.
- Compiles schemas once (`sync.Once`) and caches (`sync.Map`).
- Optional — not in the `Store.WriteFile()` hot path. Callers invoke it explicitly.

### 4. Adapter conformance test — DONE

`internal/schema/validate_test.go` includes `TestGitHubIssueAdapterConformance` — validates a sample adapter output payload against the canonical schema. Also includes negative tests:

- `TestGitHubIssueAdapterConformanceMissingRequired` — catches missing `title`.
- `TestGitHubIssueAdapterConformanceExtraField` — catches `additionalProperties` violations.
- `TestGitHubIssueAdapterConformanceInvalidState` — catches invalid enum value (`"OPEN"` instead of `"open"`).
- `TestValidateContentNullableFields` — confirms nullable fields accept `null`.
- `TestValidateContentMissingOptionalArraysStillFails` — confirms `labels` and `assignees` are required even when empty.

### 5. CLI caller conformance test — DONE

`internal/schema/validate_test.go` includes:

- `TestGitHubIssueCLIConformance` — simulates raw `gh` CLI output (camelCase, nested label/assignee objects, uppercase state), applies `mapCLIToCanonical()` transform, validates result against canonical schema.
- `TestGitHubIssueCLIConformanceUnmappedFails` — confirms raw CLI output fails validation without the mapping step.

The `mapCLIToCanonical()` helper demonstrates the exact pattern a relayfile-cli caller's `FormatFn` should follow: flatten labels to names, extract assignee logins, lowercase state, rename camelCase to snake_case, map `user` to `author`.

## What Success Looks Like — Achieved

1. `schemas/github/issue.schema.json` exists and is the single source of truth for issue file shape.
2. `schemas/embed.go` exposes an `embed.FS` for Go consumers.
3. `schemas/README.md` documents the path pattern registry and evolution rules.
4. `internal/schema/validate.go` validates any `File.Content` against its canonical schema.
5. `internal/schema/validate_test.go` proves both adapter and CLI-derived output conform to the schema, with positive and negative test cases.
6. No code in relayfile-cli or relayfile-adapters was modified — conformance is demonstrated, not enforced by import.

## What This Proof Intentionally Defers

- **Writeback schemas** (what an agent writes to create a review, post a comment, etc.).
- **Schema generation into SDK types** (TypeScript `GitHubIssueFile` interface, Python dataclass).
- **Runtime validation in the write path** (rejecting non-conforming writes at the API level).
- **Schemas for other services** (Slack, Linear, Notion).
- **Schema evolution/migration tooling**.

These are real needs but they are follow-on work. The proof establishes the ownership pattern; the pattern scales to cover these concerns.

## Risk Assessment

**Risk: the canonical schema disagrees with what adapters actually produce.**
Mitigation: the conformance test catches this immediately. If the schema and adapter diverge, the schema is adjusted — not the other way around, unless the adapter output is clearly wrong. The schema is authoritative, but it must reflect reality at the time it is published.

**Risk: `additionalProperties: false` is too strict for evolving adapters.**
Mitigation: start strict. Loosening is a non-breaking change. Tightening is breaking. The escape hatch is documented in `schemas/README.md`.

**Risk: JSON Schema library dependency in Go.**
Mitigation: `santhosh-tekuri/jsonschema/v6` is isolated to `internal/schema/` and does not affect the core server binary unless imported. The dependency is already in `go.mod`.
