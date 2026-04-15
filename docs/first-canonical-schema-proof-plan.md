# First Canonical Schema Proof — Implementation Plan

## Status

- Date: 2026-04-15
- Scope: GitHub issue schema, validation utility, conformance tests
- Prerequisites: [first-canonical-schema-proof-boundary.md](first-canonical-schema-proof-boundary.md)
- Checklist: [first-canonical-schema-proof-checklist.md](first-canonical-schema-proof-checklist.md)
- State: **Implemented** — all five steps complete

## Overview

Five steps, ordered by dependency. Each step produces a testable artifact. No step modifies existing code in `internal/relayfile/` or any external repo.

## Step 1: Create the Schema File — DONE

**Produced:** `schemas/github/issue.schema.json`

Directory structure:

```
schemas/
  github/
    issue.schema.json
```

The schema is JSON Schema draft 2020-12 defining the canonical shape for files at `/github/repos/{owner}/{repo}/issues/{number}/meta.json`. It specifies 12 required fields:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://relayfile.dev/schemas/github/issue.schema.json",
  "title": "GitHubIssueMetaFile",
  "description": "Canonical schema for files at /github/repos/{owner}/{repo}/issues/{number}/meta.json",
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

Key design choices:
- **All 12 fields required** — agents always see the full shape. Missing data is `null`, not absent.
- **Nullable types** via `["string", "null"]` — `title`, `state`, `body`, `milestone`, timestamps can all be null.
- **`author` as nested object** — `{avatarUrl, login}` with its own `additionalProperties: false`.
- **`labels` and `assignees` as `string[]`** — flattened from GitHub's nested objects. Agent-friendly.
- **`html_url` with `format: "uri"`** — validated as a proper URI.
- **`number` with `minimum: 1`** — GitHub issue numbers are positive integers.

## Step 2: Create the Path Pattern Registry and Embed Package — DONE

**Produced:** `schemas/README.md` and `schemas/embed.go`

`schemas/README.md` contains:

1. **Registry table** — one entry mapping the VFS path pattern to the schema file:

   | Path Pattern | Schema | Access |
   |---|---|---|
   | `/github/repos/{owner}/{repo}/issues/{number}/meta.json` | `github/issue.schema.json` | Read |

2. **Schema evolution rules** — adding optional fields is non-breaking; removing/renaming is breaking.

3. **Strictness escape hatch** — process for adding fields when adapters need them.

4. **Future work** — writeback schemas, multi-provider expansion, opt-in runtime validation.

`schemas/embed.go` exposes the schema assets:

```go
package schemas

import "embed"

// FS exposes the canonical schema assets for validation.
//
//go:embed README.md github/*.json
var FS embed.FS
```

## Step 3: Add the JSON Schema Dependency — DONE

**Produced:** Updated `go.mod` and `go.sum`

Added `github.com/santhosh-tekuri/jsonschema/v6` — supports draft 2020-12, format assertion, nullable types via `type: ["string", "null"]`. The dependency is consumed only by `internal/schema/`.

## Step 4: Implement the Validation Utility — DONE

**Produced:** `internal/schema/validate.go`

Implementation details:

```go
package schema

// ValidateContent checks whether content conforms to the canonical schema for a
// registered VFS path. Unknown paths are ignored (returns nil).
func ValidateContent(path string, content []byte) error
```

Architecture:
- **Path registration**: `[]registration` slice mapping compiled regex patterns to schema file paths.
- **Single pattern registered**: `^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$` -> `github/issue.schema.json`.
- **Schema loading**: `loadSchema()` reads from `schemas.FS`, compiles with `santhosh-tekuri/jsonschema/v6`.
- **Caching**: `sync.Once` for initial compilation of all registered schemas; `sync.Map` for compiled schema cache.
- **Compiler config**: `jsonschema.Draft2020` default draft, `AssertFormat()` enabled (validates `date-time`, `uri`).
- **Error format**: `"validate <path> against <schema>: <detail>"` — includes VFS path, schema file, and JSON Schema validation detail.
- **Unknown paths**: `registeredSchema()` returns `""` -> `ValidateContent()` returns `nil`.

## Step 5: Write Conformance Tests — DONE

**Produced:** `internal/schema/validate_test.go`

### Adapter conformance tests (4 tests)

| Test | Purpose | Key assertion |
|------|---------|---------------|
| `TestGitHubIssueAdapterConformance` | Valid adapter output passes | All 12 fields present, correct types, `err == nil` |
| `TestGitHubIssueAdapterConformanceMissingRequired` | Missing required field caught | Omits `title`, error mentions `"title"` |
| `TestGitHubIssueAdapterConformanceExtraField` | Extra property caught | Adds `"provider"` field, error mentions `"additional properties"` |
| `TestGitHubIssueAdapterConformanceInvalidState` | Invalid enum caught | Uses `"OPEN"` instead of `"open"`, error mentions `"/state"` |

### CLI caller conformance tests (2 tests)

| Test | Purpose | Key assertion |
|------|---------|---------------|
| `TestGitHubIssueCLIConformance` | Mapped CLI output passes | Raw CLI -> `mapCLIToCanonical()` -> validates, `err == nil` |
| `TestGitHubIssueCLIConformanceUnmappedFails` | Unmapped CLI output fails | Raw CLI has wrong field names/types, error mentions missing canonical fields |

The `mapCLIToCanonical()` helper demonstrates the exact FormatFn pattern:
1. Flatten `labels` from `[{name, id, color}]` to `["name"]`.
2. Extract `assignees` logins from `[{login, id}]` to `["login"]`.
3. Map `user` to `author` with `{avatarUrl, login}`.
4. Lowercase `state`.
5. Rename `createdAt`/`updatedAt`/`closedAt` to `created_at`/`updated_at`/`closed_at`.
6. Rename `url` to `html_url`.

### Edge case tests (4 tests)

| Test | Purpose | Key assertion |
|------|---------|---------------|
| `TestValidateContentUnknownPath` | Unknown paths pass | Slack path returns `nil` |
| `TestValidateContentInvalidJSON` | Malformed JSON caught | Truncated JSON returns decode error |
| `TestValidateContentNullableFields` | Null values accepted | `body: null`, `author.avatarUrl: null`, `author.login: null`, etc. all pass |
| `TestValidateContentMissingOptionalArraysStillFails` | Required arrays enforced | Omitting `labels`/`assignees` fails even though they have defaults |

## Implementation Order (Completed)

```
Step 1 --- schemas/github/issue.schema.json
  |
Step 2 --- schemas/README.md + schemas/embed.go
  |
Step 3 --- go get santhosh-tekuri/jsonschema/v6
  |
Step 4 --- internal/schema/validate.go
  |
Step 5 --- internal/schema/validate_test.go
  |
  v
Verified: go test ./internal/schema/... && go build ./...
```

## Files Created (Complete List)

| File | New/Modified | Purpose |
|------|-------------|---------|
| `schemas/github/issue.schema.json` | New | Canonical schema (12 fields, all required, strict) |
| `schemas/embed.go` | New | `//go:embed` package exposing `schemas.FS` |
| `schemas/README.md` | New | Path pattern registry and evolution rules |
| `internal/schema/validate.go` | New | `ValidateContent()` with regex matching and schema caching |
| `internal/schema/validate_test.go` | New | 10 conformance and edge case tests |
| `go.mod` | Modified | Added `santhosh-tekuri/jsonschema/v6` |
| `go.sum` | Modified | Dependency checksums |

## Files NOT Modified

- `internal/relayfile/store.go` — no changes to envelope types or write path
- `internal/relayfile/adapters.go` — no changes to adapter interfaces
- Any file in `relayfile-adapters/` — conformance demonstrated, not enforced by import
- Any file in `relayfile-cli/` — CLI boundary unchanged

## Risk Mitigation (Resolved)

| Risk | Resolution |
|------|------------|
| Schema disagrees with actual adapter output | Schema written against expected adapter shape; `TestGitHubIssueAdapterConformance` validates with realistic payload |
| `//go:embed` can't reach `schemas/` from `internal/schema/` | Used `schemas/embed.go` pattern — schema package exports `FS`, `internal/schema/` imports it |
| jsonschema library doesn't support `type: ["string", "null"]` | `santhosh-tekuri/jsonschema/v6` handles nullable types correctly; confirmed by `TestValidateContentNullableFields` |
| `additionalProperties: false` rejects valid adapter output | `TestGitHubIssueAdapterConformanceExtraField` catches violations; escape hatch documented in `schemas/README.md` |
| Format assertion rejects valid timestamps | `santhosh-tekuri/jsonschema/v6` with `AssertFormat()` correctly validates ISO 8601 `date-time` and `uri` formats |

## Done Criteria — Met

All items in [first-canonical-schema-proof-checklist.md](first-canonical-schema-proof-checklist.md) are checked. Specifically:

1. `schemas/github/issue.schema.json` is valid JSON Schema draft 2020-12 with 12 required fields.
2. `schemas/embed.go` exports `schemas.FS` via `//go:embed`.
3. `schemas/README.md` has the registry table, evolution rules, and escape hatch.
4. `internal/schema/validate.go` exports `ValidateContent` with caching and format assertion.
5. All tests in `internal/schema/validate_test.go` pass.
6. `go build ./...` succeeds. `go test ./internal/schema/...` passes.
7. No existing files in `internal/relayfile/` were modified.
8. Review verdict conditions 1–4 are satisfied.

## What Comes Next

The proof establishes the ownership pattern. The immediate follow-on work:

1. **Writeback schemas** — `schemas/github/issue.write.schema.json`, `review.create.schema.json`.
2. **Additional providers** — Slack, Linear, Notion schemas following the same pattern.
3. **SDK type generation** — TypeScript interfaces and Go structs generated from JSON Schema.
4. **Opt-in runtime validation** — `ValidateContent()` callable from `Store.WriteFile()` behind a flag.
5. **CI enforcement** — adapter conformance tests run against canonical schemas in CI.
