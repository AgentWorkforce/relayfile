# First Canonical Schema Proof — Checklist

## Status

- Date: 2026-04-15
- Tracks: [first-canonical-schema-proof-boundary.md](first-canonical-schema-proof-boundary.md)
- State: **Complete** — all items verified

## Pre-Flight

- [x] Confirm `schemas/` directory structure created
- [x] Confirm `internal/schema/` directory structure created
- [x] Confirm `santhosh-tekuri/jsonschema/v6` is compatible with Go module and JSON Schema draft 2020-12
- [x] Cross-reference canonical schema fields against expected GitHub adapter output shape

## Deliverables

### 1. Schema File

- [x] Create `schemas/github/issue.schema.json`
  - [x] `$schema` set to `https://json-schema.org/draft/2020-12/schema`
  - [x] `$id` set to `https://relayfile.dev/schemas/github/issue.schema.json`
  - [x] `title` set to `GitHubIssueMetaFile`
  - [x] 12 required fields: `number`, `title`, `state`, `created_at`, `updated_at`, `body`, `labels`, `assignees`, `author`, `milestone`, `closed_at`, `html_url`
  - [x] `number` type: `integer` with `minimum: 1`
  - [x] `title` type: `["string", "null"]`
  - [x] `state` type: `["string", "null"]` with enum `["open", "closed", null]`
  - [x] `body` type: `["string", "null"]`
  - [x] `labels` type: `array` of `string`, default `[]`
  - [x] `assignees` type: `array` of `string`, default `[]`
  - [x] `author` type: `object` with required `avatarUrl` (nullable string) and `login` (nullable string), `additionalProperties: false`
  - [x] `milestone` type: `["string", "null"]`
  - [x] `created_at`, `updated_at`, `closed_at` type: `["string", "null"]` with `format: "date-time"`
  - [x] `html_url` type: `string` with `format: "uri"`
  - [x] `additionalProperties: false`
  - [x] File is valid JSON

### 2. Schema Embed Package

- [x] Create `schemas/embed.go`
  - [x] Package name: `schemas`
  - [x] `//go:embed README.md github/*.json`
  - [x] Exports `var FS embed.FS`

### 3. Path Pattern Registry

- [x] Create `schemas/README.md`
  - [x] Path pattern -> schema mapping table with one entry:
    - `/github/repos/{owner}/{repo}/issues/{number}/meta.json` -> `github/issue.schema.json`
  - [x] Schema evolution rules documented:
    - Adding optional fields: non-breaking
    - Removing or renaming fields: breaking (version bump)
    - Changing required fields: breaking (version bump)
    - Loosening `additionalProperties`: non-breaking
    - Tightening `additionalProperties`: breaking
  - [x] Escape hatch for `additionalProperties: false` documented (review verdict condition 2)
  - [x] Future work section acknowledging writeback schemas and multi-provider expansion

### 4. Validation Utility

- [x] Create `internal/schema/validate.go`
  - [x] Package name: `schema`
  - [x] Imports `schemas.FS` from `schemas/embed.go`
  - [x] Uses `santhosh-tekuri/jsonschema/v6` with `Draft2020` and `AssertFormat()`
  - [x] Exports `ValidateContent(path string, content []byte) error`
  - [x] Path pattern matching via regex: `^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$`
  - [x] Returns `nil` for unregistered path patterns
  - [x] Returns descriptive error on validation failure (path, schema file, constraint detail)
  - [x] Schema compilation cached via `sync.Once` + `sync.Map`
  - [x] Does NOT import `internal/relayfile/` — no coupling to envelope types
- [x] `santhosh-tekuri/jsonschema/v6` added to `go.mod`
  - [x] Dependency isolated to `internal/schema/` only

### 5. Adapter Conformance Tests

- [x] `TestGitHubIssueAdapterConformance` — valid adapter output with all 12 fields passes validation
- [x] `TestGitHubIssueAdapterConformanceMissingRequired` — missing `title` fails
- [x] `TestGitHubIssueAdapterConformanceExtraField` — extra `provider` field fails (`additionalProperties: false`)
- [x] `TestGitHubIssueAdapterConformanceInvalidState` — `state: "OPEN"` fails (not in enum)

### 6. CLI Caller Conformance Tests

- [x] `TestGitHubIssueCLIConformance` — Layer 1 -> Layer 2 mapped output passes validation
  - [x] Simulates `gh` CLI output (camelCase, uppercase state, nested label/assignee objects, `user` instead of `author`)
  - [x] Applies `mapCLIToCanonical()` transform: flatten labels, extract logins, lowercase state, rename fields, map user->author
  - [x] Validates mapped output against canonical schema
- [x] `TestGitHubIssueCLIConformanceUnmappedFails` — raw CLI output without mapping fails validation
  - [x] Proves that Layer 1 output does not accidentally conform (missing `author`, `created_at`, etc.)

### 7. Negative / Edge Case Tests

- [x] `TestValidateContentUnknownPath` — path with no registered schema returns `nil`
- [x] `TestValidateContentInvalidJSON` — malformed JSON returns decode error
- [x] `TestValidateContentNullableFields` — nullable fields (`body: null`, `author.avatarUrl: null`, `author.login: null`, `milestone: null`, `closed_at: null`) all pass
- [x] `TestValidateContentMissingOptionalArraysStillFails` — omitting `labels` and `assignees` (required arrays) fails validation

## Verification

- [x] `go build ./internal/schema/...` succeeds
- [x] `go test ./internal/schema/...` passes all 10 tests
- [x] `go build ./...` succeeds (no breakage to existing packages)
- [x] No files in `internal/relayfile/` were modified
- [x] No files outside this repo were modified

## Review Verdict Conditions Met

- [x] **Condition 1**: Schema reflects actual adapter output — verified by `TestGitHubIssueAdapterConformance` with realistic 12-field payload
- [x] **Condition 2**: `additionalProperties: false` escape hatch documented in `schemas/README.md` strictness section
- [x] **Condition 3**: No runtime validation in write path — `Store.WriteFile()` unchanged, `ValidateContent()` is test-time and opt-in only
- [x] **Condition 4**: Writeback schemas acknowledged as next step in `schemas/README.md` future work section

## Test Inventory

| # | Test Name | Type | Validates |
|---|-----------|------|-----------|
| 1 | `TestGitHubIssueAdapterConformance` | Positive | Full adapter payload conforms |
| 2 | `TestGitHubIssueAdapterConformanceMissingRequired` | Negative | Missing required field caught |
| 3 | `TestGitHubIssueAdapterConformanceExtraField` | Negative | Extra property caught |
| 4 | `TestGitHubIssueAdapterConformanceInvalidState` | Negative | Invalid enum value caught |
| 5 | `TestGitHubIssueCLIConformance` | Positive | CLI-to-canonical mapping conforms |
| 6 | `TestGitHubIssueCLIConformanceUnmappedFails` | Negative | Raw CLI output rejected |
| 7 | `TestValidateContentUnknownPath` | Edge | Unknown paths pass silently |
| 8 | `TestValidateContentInvalidJSON` | Edge | Malformed JSON caught |
| 9 | `TestValidateContentNullableFields` | Edge | Null values in nullable fields accepted |
| 10 | `TestValidateContentMissingOptionalArraysStillFails` | Negative | Required arrays cannot be omitted |
