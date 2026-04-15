# Canonical File Schema Ownership — Review Verdict

## Status

- Date: 2026-04-15
- Verdict: **Approved — implemented and verified**

## Summary

Core relayfile owns canonical file schemas. The boundary is clean, the proof is implemented, and the existing code already separates envelope types (`File`, `TreeEntry`, `ApplyAction`) from content schemas. The `schemas/` directory, `internal/schema/` validation utility, and conformance tests fill the content-schema gap without restructuring anything.

## What Was Reviewed

1. [canonical-file-schema-ownership-boundary.md](canonical-file-schema-ownership-boundary.md) — ownership rules, schema location, relationship to existing types.
2. [canonical-file-schema-ownership-proof-direction.md](canonical-file-schema-ownership-proof-direction.md) — first proof: GitHub issue schema, validation utility, conformance tests.
3. Existing relayfile-cli canonical boundary document (defines the three schema layers: raw CLI, canonical file, downstream consumer).
4. Existing relayfile-cli bridge boundary document (defines filesystem as the integration point between relayfile-cli and core relayfile).
5. Core relayfile source: `internal/relayfile/store.go` (envelope types), `internal/relayfile/adapters.go` (adapter types).
6. Implemented artifacts: `schemas/github/issue.schema.json`, `schemas/embed.go`, `schemas/README.md`, `internal/schema/validate.go`, `internal/schema/validate_test.go`.

## Verdict: Approved

The design is sound and the implementation is complete for these reasons:

### 1. Natural ownership boundary

Core relayfile already defines the VFS envelope (`File` with `Path`, `Revision`, `ContentType`, `Content`, `Semantics`). Canonical content schemas are the missing layer inside `File.Content`. Placing them in core relayfile extends an existing responsibility rather than creating a new one.

The existing `ApplyAction` type in `adapters.go` already has a `Content` field that adapters populate — the canonical schema makes the expected shape of that content explicit.

### 2. No coupling introduced

The schema files are JSON Schema documents. Adapters and CLI callers can reference them (import types, validate at test time) without taking a runtime dependency on core relayfile. The filesystem remains the sole integration point between relayfile-cli and core relayfile — schemas add documentation and validation, not coupling.

The `schemas/embed.go` file provides a clean Go embedding surface (`schemas.FS`) without requiring external consumers to import it.

### 3. Adapter autonomy preserved

Adapters still own webhook parsing (via `ProviderAdapter.ParseEnvelope()`), path mapping, and writeback (via `ProviderWritebackAdapter.ApplyWriteback()`). They conform to canonical schemas the same way they conform to the existing `ApplyAction` interface — by producing the right shape. The schema makes the "right shape" explicit rather than implicit.

### 4. CLI boundary unchanged

relayfile-cli's `materialize()` stays schema-unaware. The caller's `FormatFn` is where Layer 1 to Layer 2 mapping happens. The `mapCLIToCanonical()` helper in `validate_test.go` demonstrates the exact mapping pattern without importing it into relayfile-cli's `src/`.

### 5. The proof is correctly scoped and complete

One service (GitHub), one file type (issue), optional validation, no changes to other repos. The implementation includes:

- `schemas/github/issue.schema.json` — canonical schema with 12 fields, all required, strict `additionalProperties: false`.
- `schemas/embed.go` — Go embedding for runtime access.
- `schemas/README.md` — path pattern registry and evolution rules.
- `internal/schema/validate.go` — `ValidateContent()` with regex path matching, schema caching, and format assertion.
- `internal/schema/validate_test.go` — 10 tests covering adapter conformance, CLI conformance, negative cases (missing fields, extra fields, invalid enums, unmapped raw CLI), nullable fields, and unknown paths.

## Conditions Met

### Condition 1: Schema reflects actual adapter output

The canonical schema was written against the expected adapter output shape. `TestGitHubIssueAdapterConformance` validates this with a realistic payload. The negative tests (`MissingRequired`, `ExtraField`, `InvalidState`) confirm the schema catches real divergence.

### Condition 2: `additionalProperties: false` has an escape hatch

Documented in `schemas/README.md`: add optional fields (non-breaking), update producers, keep older producers conformant. The process is clear and the starting strictness is correct — loosening is always non-breaking.

### Condition 3: No runtime validation in the write path

`ValidateContent()` is test-time and opt-in. It is not called from `Store.WriteFile()` or any hot path. The `santhosh-tekuri/jsonschema/v6` dependency is isolated to `internal/schema/` and only linked when that package is imported.

### Condition 4: Writeback schemas are the next slice

The proof covers read-path schemas (what an agent reads from `File.Content`). Writeback schemas (what an agent writes to trigger API actions via `ProviderWritebackAdapter.ApplyWriteback()`) should follow the same ownership pattern:

```
schemas/github/
  issue.schema.json              # read: what the file contains
  issue.write.schema.json        # write: what an agent PUTs to modify
  review.create.schema.json      # write: what an agent PUTs to create a review
```

This is deferred from the first proof but should be the immediate follow-on.

## Risks Accepted

| Risk | Severity | Mitigation |
|------|----------|------------|
| Schema/adapter divergence over time | Medium | Conformance test catches it; CI enforcement when adapters are in-repo |
| JSON Schema library dependency in Go | Low | Isolated to `internal/schema/`, not in core write path, already in `go.mod` |
| Schema proliferation without governance | Low | `schemas/README.md` registry prevents orphaned schemas |
| Canonical schemas lag behind adapter evolution | Medium | CI test validates adapter fixtures against schemas |

## Risks Rejected

| Proposed risk | Why it is not a real risk |
|---------------|------------------------|
| "Schemas will constrain adapter innovation" | Adapters can request schema changes. The schema is a contract, not a cage. Adding optional fields is non-breaking. |
| "relayfile-cli will need to import schemas" | It will not. Callers import schemas; `materialize()` is schema-unaware. The boundary is at the call site, as demonstrated by `TestGitHubIssueCLIConformance`. |
| "This adds too much process" | One JSON file, one test file, one README entry. The process is proportional to the value. |
| "Envelope types in store.go already cover this" | They do not. `File.Content` is `string` — the envelope says nothing about what that string decodes to. Canonical schemas fill that gap. |

## Architecture Alignment

The canonical schema layer fits cleanly into the existing architecture:

```
OpenAPI spec (relayfile-v1.openapi.yaml)         <- API envelope contracts
  +-- VFS types (store.go: File, TreeEntry)       <- runtime envelope types
        +-- Canonical schemas (schemas/)           <- file content contracts
              |-- adapters conform (ApplyAction.Content)
              +-- CLI callers conform (FormatFn output)
```

No existing layer was modified. The new layer is additive and fills a documented gap between the envelope (which types like `File` and `ApplyAction` define) and the content (which until now was untyped `string`).

## Slice Honesty

What this slice **delivers**: a JSON Schema file for GitHub issues, an embedded schema filesystem, a path pattern registry with evolution rules, a Go validation utility with caching, and 10 conformance tests covering adapter output, CLI output, and edge cases.

What this slice **does not deliver**: runtime enforcement in the write path, multi-service schema coverage, SDK type generation, writeback schemas, or schema migration tooling.

What this slice **proves**: core relayfile can be the schema authority without breaking the adapter boundary, without changing relayfile-cli, and without adding coupling. The pattern established here scales to cover all services and both read/write paths.

The slice is bounded. The boundary is clean. The implementation is complete.

RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_BOUNDARY_READY
