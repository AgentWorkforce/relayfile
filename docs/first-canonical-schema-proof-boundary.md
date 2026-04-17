# First Canonical Schema Proof — Boundary

## Status

- Date: 2026-04-15
- Scope: GitHub issue file schema — single proof of canonical schema ownership
- Prerequisites: [canonical-file-schema-ownership-boundary.md](canonical-file-schema-ownership-boundary.md), [canonical-file-schema-ownership-review-verdict.md](canonical-file-schema-ownership-review-verdict.md) (approved)
- State: **Implemented** — all artifacts live

## Scope Boundary

This proof covers exactly one service (GitHub), one file type (issue), and one VFS path pattern. Everything else is explicitly deferred.

### In Scope

| Artifact | Location | Purpose |
|----------|----------|---------|
| GitHub issue JSON Schema | `schemas/github/issue.schema.json` | Canonical shape for `/github/repos/{owner}/{repo}/issues/{number}/meta.json` |
| Schema embed package | `schemas/embed.go` | Exposes `schemas.FS` via `//go:embed` for Go consumers |
| Path pattern registry | `schemas/README.md` | Maps VFS path patterns to schema files; documents evolution rules |
| Validation utility | `internal/schema/validate.go` | `ValidateContent(path, content)` with regex path matching and schema caching |
| Conformance tests | `internal/schema/validate_test.go` | 10 tests: adapter conformance, CLI conformance, negative cases, edge cases |

### Out of Scope

- Schemas for pull requests, reviews, comments, Slack messages, Linear issues, Notion pages.
- TypeScript or Python type generation from the schema.
- Runtime validation in `Store.WriteFile()` or any hot path.
- Changes to `relayfile-adapters` or `relayfile-cli` codebases.
- Writeback schemas (`issue.write.schema.json`).
- Schema evolution or migration tooling.

## Architectural Fit

The proof inserts one new layer into the existing stack without modifying any existing layer:

```
OpenAPI spec (relayfile-v1.openapi.yaml)           <- API envelope contracts
  +-- VFS types (store.go: File, TreeEntry)         <- runtime envelope types
        +-- Canonical schemas (schemas/)             <- file content contracts  <- THIS PROOF
              |-- adapters conform (ApplyAction.Content)
              +-- CLI callers conform (FormatFn output)
```

### Relationship to Existing Types

The proof does NOT touch or redefine:

- **`File`** in `internal/relayfile/store.go` — envelope type. `File.Content` remains `string`. The canonical schema specifies what that string decodes to for GitHub issue paths.
- **`ApplyAction`** in `internal/relayfile/adapters.go` — adapter output type. `ApplyAction.Content` remains `string`. The canonical schema specifies the expected JSON shape when `ApplyAction.Path` matches the issue path pattern.
- **`FileSemantics`** — envelope metadata (properties, relations, permissions, comments). Orthogonal to content schemas.
- **`ProviderAdapter.ParseEnvelope()`** — adapter interface. Adapters still own parsing; the schema constrains their output shape.

### What the Schema Defines

For files at `/github/repos/{owner}/{repo}/issues/{number}/meta.json`:

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `number` | integer (>= 1) | Yes | GitHub issue number |
| `title` | string or null | Yes | Issue title |
| `state` | `"open"`, `"closed"`, or null | Yes | Lowercase enum |
| `body` | string or null | Yes | Issue body text |
| `labels` | string[] | Yes | Flattened from GitHub label objects |
| `assignees` | string[] | Yes | Login names, flattened from assignee objects |
| `author` | object (`{avatarUrl, login}`) | Yes | Issue author with avatar URL and login |
| `milestone` | string or null | Yes | Milestone name |
| `created_at` | date-time string or null | Yes | ISO 8601 |
| `updated_at` | date-time string or null | Yes | ISO 8601 |
| `closed_at` | date-time string or null | Yes | ISO 8601, null when open |
| `html_url` | string (URI) | Yes | Browser URL for the issue |

All 12 fields are required. Nullable fields use `type: ["string", "null"]` — the field is always present but may be `null`. `additionalProperties: false` — strict by design.

### Design Decisions

1. **All fields required, nullable where appropriate** — agents always see the full shape. Missing data is `null`, not absent. This eliminates defensive nil-checking in consumer code.
2. **`labels` as `string[]`** not `{name, color}[]` — the canonical schema is agent-friendly. Agents care about label names. Color and ID are provider metadata.
3. **`assignees` as `string[]`** — login names only, flattened from GitHub's assignee objects.
4. **`author` as `{avatarUrl, login}`** — minimal author object. The nested object convention uses camelCase for sub-fields.
5. **`snake_case` top-level field names** — consistent with relayfile conventions. The canonical schema normalizes from GitHub's mixed casing.
6. **`state` lowercase enum with null** — GitHub's GraphQL API returns `OPEN`/`CLOSED`. The canonical schema normalizes to lowercase. Null represents unknown or draft state.
7. **`html_url` as URI** — browser-navigable link. Validated with `format: "uri"`.
8. **`additionalProperties: false`** — strict. Catches drift immediately. Loosening is a non-breaking change if needed later.

## Boundary Rules for This Proof

1. The schema file is JSON Schema draft 2020-12. No other format.
2. The validation utility is in `internal/schema/`, not in `internal/relayfile/`. It does not import `internal/relayfile/` types.
3. Schema files are embedded via `schemas/embed.go` (`//go:embed README.md github/*.json`). No filesystem reads at runtime.
4. Path pattern matching uses regex (`^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$`). One pattern registered for this proof.
5. `ValidateContent()` returns `nil` for paths with no registered schema. It does not reject unknown paths.
6. All tests are in core relayfile. No cross-repo test dependencies.
7. `santhosh-tekuri/jsonschema/v6` is the only new dependency, isolated to `internal/schema/`.
8. Format assertion is enabled — `date-time` and `uri` formats are validated, not just accepted.

## Conformance Model

```
Adapter (relayfile-adapters)                CLI caller
  |                                           |
  | produces ApplyAction{                     | applies FormatFn:
  |   Path: "/.../issues/42/meta.json"        |   gh CLI output (Layer 1)
  |   Content: "{...}"                        |     -> canonical JSON (Layer 2)
  | }                                         |
  +------------------+------------------------+
                     |
                     v
      schema.ValidateContent(path, content)
                     |
                     v
      schemas/github/issue.schema.json
```

Both data paths produce JSON targeting the same VFS path pattern. The canonical schema is the shared contract. The validation utility verifies conformance. Neither data path defines the schema.

## CLI Conformance Pattern

The `mapCLIToCanonical()` helper in `validate_test.go` demonstrates the exact transform a `FormatFn` should apply:

1. Flatten `labels` from `[{name, id, color}]` to `["name1", "name2"]`.
2. Extract `assignees` logins from `[{login, id}]` to `["login1", "login2"]`.
3. Map `user` to `author` with `{avatarUrl, login}` (rename `avatar_url` to `avatarUrl`).
4. Lowercase `state` (`"OPEN"` -> `"open"`).
5. Rename `createdAt` -> `created_at`, `updatedAt` -> `updated_at`, `closedAt` -> `closed_at`.
6. Rename `url` -> `html_url`.

## Exit Criteria — Met

1. `schemas/github/issue.schema.json` exists and is valid JSON Schema draft 2020-12 with 12 required fields.
2. `schemas/embed.go` exports `schemas.FS` via `//go:embed`.
3. `schemas/README.md` documents the path pattern -> schema mapping with one entry and evolution rules.
4. `internal/schema/validate.go` exports `ValidateContent(path string, content []byte) error` with caching.
5. `internal/schema/validate_test.go` has 10 passing tests covering adapter conformance, CLI conformance, and negative/edge cases.
6. No files in `internal/relayfile/`, `relayfile-adapters/`, or `relayfile-cli/` were modified.
7. `go build ./...` succeeds. `go test ./internal/schema/...` passes.
