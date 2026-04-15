# Canonical File Schema Ownership — Review Verdict

## Status

- Date: 2026-04-15
- Verdict: **Approved with conditions**

## Summary

Core relayfile should own canonical file schemas. The boundary is clean, the proof is narrow, and the existing code already separates envelope types (`File`, `TreeEntry`) from content schemas — this work fills the content-schema gap without restructuring anything.

## What Was Reviewed

1. [canonical-file-schema-ownership-boundary.md](canonical-file-schema-ownership-boundary.md) — ownership rules, schema location, relationship to existing types.
2. [canonical-file-schema-ownership-proof-direction.md](canonical-file-schema-ownership-proof-direction.md) — first proof: GitHub issue schema, validation utility, conformance tests.
3. Existing relayfile-cli canonical boundary document (provided as input).
4. Existing relayfile-cli bridge boundary document (provided as input).
5. Core relayfile source: `internal/relayfile/store.go`, `internal/relayfile/adapters.go`, OpenAPI spec.

## Verdict: Approved

The design is sound for these reasons:

### 1. Natural ownership boundary

Core relayfile already defines the VFS envelope (`File`, `FileSemantics`, `WriteRequest`). Canonical content schemas are the missing layer inside `File.Content`. Placing them in core relayfile extends an existing responsibility rather than creating a new one.

### 2. No coupling introduced

The schema files are JSON Schema documents. Adapters and CLI callers can reference them (import types, validate at test time) without taking a runtime dependency on core relayfile. The filesystem remains the sole integration point between relayfile-cli and core relayfile — schemas add documentation, not coupling.

### 3. Adapter autonomy preserved

Adapters still own webhook parsing, path mapping, and writeback. They conform to canonical schemas the same way they conform to the existing `ApplyAction` interface — by producing the right shape. The schema makes the "right shape" explicit rather than implicit.

### 4. CLI boundary unchanged

relayfile-cli's `materialize()` stays schema-unaware. The caller's `FormatFn` is where Layer 1 → Layer 2 mapping happens. Canonical schemas give callers a target to aim at; they do not change the `materialize()` API.

### 5. The proof is scoped correctly

One service, one file type, optional validation, no changes to other repos. This is the right size for a first proof.

## Conditions

### Condition 1: Schema must reflect actual adapter output

The canonical schema must be validated against real adapter output before publication. If the schema says `labels: string[]` but the GitHub adapter currently emits `labels: {name: string, color: string}[]`, the schema must either match reality or the adapter must be updated first. Do not publish an aspirational schema that nothing conforms to.

**How to verify:** The conformance test in the proof direction (step 4) satisfies this. Run it against actual adapter fixtures before merging the schema.

### Condition 2: `additionalProperties: false` needs an escape hatch plan

Starting strict is correct, but document the process for loosening. When a new adapter version needs to add a field, the steps should be:

1. Add the field to the canonical schema (non-breaking: new optional field).
2. Adapters that produce the field start emitting it.
3. Old adapters continue to conform (field is optional).

This is standard JSON Schema evolution but should be written down in `schemas/README.md`.

### Condition 3: No runtime validation in the write path yet

The validation utility should be test-time and optional. Adding schema validation to `Store.WriteFile()` would add latency and a hard dependency on a JSON Schema library in the hot path. Defer runtime enforcement until the schema set is stable and the performance impact is measured.

### Condition 4: Writeback schemas are next

The proof covers read-path schemas (what an agent reads). Writeback schemas (what an agent writes to trigger an API action) are equally important and should follow the same ownership pattern. They can use the same `schemas/` directory structure:

```
schemas/github/
  issue.schema.json              # read: what the file contains
  issue.write.schema.json        # write: what an agent can PUT to modify
  review.create.schema.json      # write: what an agent PUTs to create a review
```

This is deferred from the first proof but should be the immediate follow-on.

## Risks Accepted

| Risk | Severity | Mitigation |
|------|----------|------------|
| Schema/adapter divergence at publication time | Medium | Conformance test catches it before merge |
| JSON Schema library dependency in Go | Low | Only used in validation utility, not in core write path |
| Schema proliferation without governance | Low | `schemas/README.md` registry prevents orphaned schemas |
| Canonical schemas lag behind adapter evolution | Medium | CI test in core relayfile that validates adapter fixtures against schemas |

## Risks Rejected

| Proposed risk | Why it's not a real risk |
|---------------|------------------------|
| "Schemas will constrain adapter innovation" | Adapters can request schema changes. The schema is a contract, not a cage. |
| "relayfile-cli will need to import schemas" | It won't. Callers import schemas; `materialize()` is schema-unaware. |
| "This adds too much process" | One JSON file, one test. The process is proportional to the value. |

## Architecture Alignment

The canonical schema layer fits cleanly into the existing architecture:

```
OpenAPI spec (relayfile-v1.openapi.yaml)     ← API envelope contracts
  └── VFS types (store.go: File, TreeEntry)  ← runtime envelope types
        └── Canonical schemas (schemas/)     ← file content contracts  ← NEW
              ├── adapters conform           ← webhook → canonical
              └── CLI callers conform        ← CLI output → canonical
```

No existing layer is modified. The new layer is additive and fills a documented gap.

## Conclusion

The three-layer schema model (raw CLI output → canonical file schema → downstream consumer schema) is the right decomposition. Core relayfile is the right owner for Layer 2. The proof is correctly scoped. Ship it with the four conditions above.

RELAYFILE_CANONICAL_SCHEMA_OWNERSHIP_BOUNDARY_READY
