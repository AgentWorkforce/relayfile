# Canonical File Schema Ownership Boundary

## Status

- Date: 2026-04-15
- Scope: Core relayfile as canonical schema authority
- Audience: relayfile, relayfile-adapters, relayfile-cli, relayfile-providers maintainers

## Problem

Canonical file schemas — the shape of data at each VFS path — are currently implicit. Adapters define them by accident (whatever shape they emit becomes the de facto standard). CLI callers guess by reading adapter output. There is no single authority, no shared type, and no way to validate conformance. Two data paths (webhook and CLI) targeting the same VFS path can silently produce incompatible shapes.

## Decision

Core relayfile owns canonical file schemas. They are defined here, in this repo, as the authoritative specification of what a file at a given VFS path must contain. Adapters conform to them. CLI callers conform to them. Neither defines them.

## What a Canonical File Schema Is

A canonical file schema specifies:

1. **VFS path pattern** — the path template where files of this type live (e.g., `/github/repos/{owner}/{repo}/issues/{number}.json`).
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
  github/
    issue.schema.json          # /github/repos/{owner}/{repo}/issues/{number}.json
    pull-request.schema.json   # /github/repos/{owner}/{repo}/pulls/{number}/metadata.json
    review.schema.json         # /github/repos/{owner}/{repo}/pulls/{number}/reviews/{id}.json
  slack/
    message.schema.json        # /slack/channels/{channel}/messages/{ts}.json
  linear/
    issue.schema.json          # /linear/teams/{team}/issues/{id}.json
  notion/
    page.schema.json           # /notion/pages/{id}.json
```

Each schema file is a JSON Schema (draft 2020-12) document. TypeScript interfaces may be generated from these schemas but are derived artifacts, not the source of truth.

## Schema Format

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://relayfile.dev/schemas/github/issue.schema.json",
  "title": "GitHubIssueFile",
  "description": "Canonical schema for files at /github/repos/{owner}/{repo}/issues/{number}.json",
  "type": "object",
  "required": ["number", "title", "state", "created_at", "updated_at"],
  "properties": {
    "number": { "type": "integer" },
    "title": { "type": "string" },
    "state": { "type": "string", "enum": ["open", "closed"] },
    "body": { "type": ["string", "null"] },
    "labels": { "type": "array", "items": { "type": "string" } },
    "assignees": { "type": "array", "items": { "type": "string" } },
    "created_at": { "type": "string", "format": "date-time" },
    "updated_at": { "type": "string", "format": "date-time" }
  },
  "additionalProperties": false
}
```

## Ownership Table

| Concern | Owner | Relation to Canonical Schema |
|---------|-------|------------------------------|
| Define canonical file schemas | **core relayfile** (`schemas/`) | Source of truth |
| Publish schemas as importable types | **core relayfile** | Generated from JSON Schema |
| Conform webhook payloads to schemas | **relayfile-adapters** | Consumer of schemas |
| Conform CLI output to schemas | **caller of relayfile-cli** | Consumer of schemas |
| Validate files against schemas | **core relayfile** (optional utility) | Enforcement layer |
| Define raw CLI output shapes | **external CLI vendors** | Unrelated — Layer 1 |
| Define downstream consumer shapes | **consuming agents** | Unrelated — Layer 3 |

## Relationship to Existing Code

Core relayfile already defines the VFS envelope types in `internal/relayfile/store.go`:

- `File` — the VFS file wrapper (path, revision, contentType, content, provider metadata, semantics).
- `FileSemantics` — properties, relations, permissions, comments.
- `TreeEntry`, `WriteRequest`, `WriteResult` — the API-level types.

These are **envelope types** (the container), not **content schemas** (what's inside `File.Content`). Canonical file schemas specify the shape of the decoded `Content` field for files at specific path patterns. The two layers are complementary:

```
┌──────────────────────────────┐
│  VFS Envelope (store.go)     │  ← path, revision, contentType, semantics
│  ┌────────────────────────┐  │
│  │  Canonical File Schema │  │  ← the JSON inside File.Content
│  │  (schemas/*.json)      │  │     for a given path pattern
│  └────────────────────────┘  │
└──────────────────────────────┘
```

## How Adapters Conform

Adapters in `relayfile-adapters` produce `ApplyAction` values (defined in `internal/relayfile/adapters.go`). The `ApplyAction.Content` field must contain JSON that conforms to the canonical schema for the target path. Adapters import or reference the canonical schema to ensure conformance.

Adapters do NOT define schemas. If an adapter needs a field that the canonical schema doesn't include, the adapter requests a schema change in core relayfile — it does not unilaterally extend the shape.

## How relayfile-cli Conforms

relayfile-cli's `materialize()` is schema-unaware. The **caller** of `materialize()` provides a `FormatFn` that maps raw CLI output (Layer 1) to the canonical schema (Layer 2). The caller can import canonical schema types or validate against JSON Schema files published by core relayfile.

relayfile-cli does not import canonical schemas into its `src/`. The conformance boundary is at the call site.

## Schema Versioning

Canonical schemas are versioned alongside the relayfile API version. A breaking change to a canonical schema is a breaking change to the VFS contract. Schema changes follow the same process as API changes:

1. Propose the change with rationale.
2. Assess impact on adapters and known CLI callers.
3. Version bump if breaking.
4. Adapters and CLI callers update their conformance logic.

## What This Boundary Does NOT Cover

- **Writeback schemas** — the shape of data an agent writes to trigger an API action (e.g., review creation). These are separate schemas, also owned by core relayfile, but not addressed in this document.
- **Event schemas** — the shape of VFS events (`Event` in `store.go`). These are envelope types, not file content schemas.
- **Provider-specific metadata** — fields like `providerObjectId` are envelope metadata, not file content.

## Boundary Rules

1. Canonical file schemas live in `schemas/` in the core relayfile repo.
2. JSON Schema is the source format. TypeScript/Go types are generated.
3. Adapters and CLI callers conform to schemas; they do not define them.
4. `File.Content` at a documented path pattern must parse to the canonical schema for that pattern.
5. Schema changes are breaking changes and follow the API versioning process.
6. Validation against schemas is optional today, mandatory when the tooling matures.
