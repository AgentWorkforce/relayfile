# Canonical File Schema Ownership Boundary

## Status

- Date: 2026-04-15
- Scope: Core relayfile as canonical schema authority
- Audience: relayfile, relayfile-adapters, relayfile-cli, relayfile-providers maintainers
- State: **Implemented** — first schema, validation utility, and conformance tests are live

## Problem

Canonical file schemas — the shape of data at each VFS path — were previously implicit. Adapters defined them by accident (whatever shape they emitted became the de facto standard). CLI callers guessed by reading adapter output. There was no single authority, no shared type, and no way to validate conformance. Two data paths (webhook and CLI) targeting the same VFS path could silently produce incompatible shapes.

## Decision

Core relayfile owns canonical file schemas. They are defined here, in this repo, as the authoritative specification of what a file at a given VFS path must contain. Adapters conform to them. CLI callers conform to them. Neither defines them.

## What a Canonical File Schema Is

A canonical file schema specifies:

1. **VFS path pattern** — the path template where files of this type live (e.g., `/github/repos/{owner}/{repo}/issues/{number}/meta.json`).
2. **Required fields** — fields that must be present in every file at that path.
3. **Field types and constraints** — string formats, enumerations, nullable fields.
4. **Envelope structure** — the top-level shape (flat object, nested, array).

A canonical schema does NOT specify:

- How to obtain the data (webhook parsing, CLI invocation, API call).
- How to authenticate with the source service.
- How to write back changes to the source API.
- Downstream transformations consumers may apply.

## Where Schemas Live

```
schemas/
  embed.go                         # //go:embed for validation utility
  README.md                        # Path pattern registry + evolution rules
  github/
    issue.schema.json              # /github/repos/{owner}/{repo}/issues/{number}/meta.json
```

Each schema file is a JSON Schema (draft 2020-12) document. The `schemas/embed.go` file exposes an `embed.FS` so the Go validation utility can load schemas at runtime. TypeScript interfaces and Go structs may be generated from these schemas in the future but are derived artifacts, not the source of truth.

## Current Schema: GitHub Issue

The first canonical schema (`schemas/github/issue.schema.json`) defines the shape of files at `/github/repos/{owner}/{repo}/issues/{number}/meta.json`:

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `number` | integer (>= 1) | Yes | GitHub issue number |
| `title` | string or null | Yes | Issue title |
| `state` | "open", "closed", or null | Yes | Lowercase enum |
| `body` | string or null | Yes | Issue body text |
| `labels` | string[] | Yes | Flattened from GitHub label objects |
| `assignees` | string[] | Yes | Login names, flattened from assignee objects |
| `author` | object (`{avatarUrl, login}`) | Yes | Issue author |
| `milestone` | string or null | Yes | Milestone name |
| `created_at` | date-time string or null | Yes | ISO 8601 |
| `updated_at` | date-time string or null | Yes | ISO 8601 |
| `closed_at` | date-time string or null | Yes | ISO 8601, null when open |
| `html_url` | string (URI) | Yes | Browser URL for the issue |

Design choices:
- **`additionalProperties: false`** — strict. Forces adapters and CLI callers to produce exactly the canonical shape, not a superset. Loosening is a non-breaking change if needed later.
- **`labels` as `string[]`** — flattened from GitHub's label objects (`{name, id, color}`). The canonical schema is agent-friendly, not a mirror of the API response.
- **`snake_case`** — consistent with relayfile conventions, except `author.avatarUrl` and `author.login` which follow the nested object convention.
- **All fields required** — agents can rely on every field being present. Nullable fields use `type: ["string", "null"]` rather than being optional.

## Ownership Table

| Concern | Owner | Relation to Canonical Schema |
|---------|-------|------------------------------|
| Define canonical file schemas | **core relayfile** (`schemas/`) | Source of truth |
| Publish schemas as importable types | **core relayfile** | Generated from JSON Schema (future) |
| Embed schemas for Go validation | **core relayfile** (`schemas/embed.go`) | `//go:embed` FS |
| Validate content against schemas | **core relayfile** (`internal/schema/`) | Optional utility, not in write path |
| Conform webhook payloads to schemas | **relayfile-adapters** | Consumer of schemas |
| Conform CLI output to schemas | **caller of relayfile-cli** | Consumer of schemas |
| Define raw CLI output shapes | **external CLI vendors** | Unrelated — Layer 1 |
| Define downstream consumer shapes | **consuming agents** | Unrelated — Layer 3 |

## Relationship to Existing Code

Core relayfile defines VFS envelope types in `internal/relayfile/store.go`:

- `File` — the VFS file wrapper (`Path`, `Revision`, `ContentType`, `Content`, `Encoding`, `Provider`, `ProviderObjectID`, `LastEditedAt`, `Semantics`).
- `FileSemantics` — `Properties` (map), `Relations`, `Permissions`, `Comments` (string slices).
- `TreeEntry` — directory listing entry with `Path`, `Type`, `Revision`, `Size`, counts.
- `WriteRequest`, `WriteResult` — the API-level write types.
- `Event` — VFS event with `EventID`, `Type`, `Path`, `Revision`, `Origin`, `Timestamp`.

And in `internal/relayfile/adapters.go`:

- `ApplyAction` — adapter output with `Type` (ActionType: `file_upsert`, `file_delete`, `ignored`), `Path`, `Content`, `ContentType`, `ProviderObjectID`, `Semantics`.
- `ProviderAdapter` — interface requiring `Provider()` and `ParseEnvelope()`.
- `ProviderWritebackAdapter` — interface for `ApplyWriteback()`.

These are **envelope types** (the container), not **content schemas** (what's inside `File.Content`). Canonical file schemas specify the shape of the decoded `Content` field for files at specific path patterns. The two layers are complementary:

```
┌──────────────────────────────┐
│  VFS Envelope (store.go)     │  <- Path, Revision, ContentType, Semantics
│  ┌────────────────────────┐  │
│  │  Canonical File Schema │  │  <- the JSON inside File.Content
│  │  (schemas/*.json)      │  │     for a given path pattern
│  └────────────────────────┘  │
└──────────────────────────────┘
```

## Validation Utility

The `internal/schema/` package provides `ValidateContent(path string, content []byte) error`:

- Embeds JSON Schema files via `schemas/embed.go` (`//go:embed`).
- Uses `santhosh-tekuri/jsonschema/v6` for draft 2020-12 validation with format assertion.
- Path pattern matching uses regex to map VFS paths to schema files (e.g., `^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$`).
- Returns `ErrUnknownPath` for unknown paths (no schema registered) — callers can distinguish this case with `errors.Is(err, ErrUnknownPath)`. 
- Compiles schemas once on first use (`sync.Once` + `sync.Map` cache).
- **Optional and test-time only** — not in the `Store.WriteFile()` hot path.

## How Adapters Conform

Adapters in `relayfile-adapters` produce `ApplyAction` values (defined in `internal/relayfile/adapters.go`). The `ApplyAction.Content` field must contain JSON that conforms to the canonical schema for the target `ApplyAction.Path`. Adapters import or reference the canonical schema to ensure conformance.

Adapters do NOT define schemas. If an adapter needs a field that the canonical schema doesn't include, the adapter requests a schema change in core relayfile — it does not unilaterally extend the shape.

## How relayfile-cli Conforms

relayfile-cli's `materialize()` is schema-unaware. The **caller** of `materialize()` provides a `FormatFn` that maps raw CLI output (Layer 1) to the canonical schema (Layer 2). The caller can import canonical schema types or validate against JSON Schema files published by core relayfile.

relayfile-cli does not import canonical schemas into its `src/`. The conformance boundary is at the call site. The CLI conformance test in `internal/schema/validate_test.go` (`TestGitHubIssueCLIConformance`) demonstrates the mapping pattern:

1. Simulate raw CLI output (camelCase fields, nested label/assignee objects, `OPEN` state).
2. Apply a `mapCLIToCanonical()` transform (flatten labels, extract logins, lowercase state, rename fields).
3. Validate the result against the canonical schema.

This pattern is what every relayfile-cli caller should follow.

## Schema Versioning

Canonical schemas are versioned alongside the relayfile API version. A breaking change to a canonical schema is a breaking change to the VFS contract. Schema changes follow the same process as API changes:

1. Propose the change with rationale.
2. Assess impact on adapters and known CLI callers.
3. Version bump if breaking.
4. Adapters and CLI callers update their conformance logic.

## What This Boundary Does NOT Cover

- **Writeback schemas** — the shape of data an agent writes to trigger an API action (e.g., review creation). These are separate schemas, also owned by core relayfile, but not addressed in this document.
- **Event schemas** — the shape of VFS events (`Event` in `store.go`). These are envelope types, not file content schemas.
- **Provider-specific metadata** — fields like `ProviderObjectID` are envelope metadata in `File` and `ApplyAction`, not file content.
- **Semantics** — `FileSemantics` (properties, relations, permissions, comments) are envelope metadata attached to the `File` struct, orthogonal to content schemas.

## Boundary Rules

1. Canonical file schemas live in `schemas/` in the core relayfile repo.
2. JSON Schema (draft 2020-12) is the source format. TypeScript/Go types are generated artifacts.
3. Adapters and CLI callers conform to schemas; they do not define them.
4. `File.Content` at a documented path pattern must parse to the canonical schema for that pattern.
5. Schema changes are breaking changes and follow the API versioning process.
6. Validation via `internal/schema/ValidateContent()` is optional today, mandatory when the tooling matures.
7. The `schemas/README.md` registry is the authoritative mapping from path patterns to schema files.
