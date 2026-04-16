# Canonical Schemas

This directory is the canonical registry for relayfile file-content schemas.

| Path Pattern | Schema | Access |
|---|---|---|
| `/github/repos/{owner}/{repo}/issues/{number}/meta.json` | `github/issue.schema.json` | Read |

> **Migration note:** This path pattern replaces the previous `{number}.json` convention. Code targeting the old `/github/repos/{owner}/{repo}/issues/{number}.json` path must be updated.

## Evolution Rules

- Adding an optional field to an existing schema is non-breaking.
- Removing a field, renaming a field, or making an optional field required is breaking and requires a version bump.
- Loosening `additionalProperties` from `false` to `true` is non-breaking.
- Tightening `additionalProperties` from `true` to `false` is breaking.

## Strictness Escape Hatch

This proof starts with `additionalProperties: false` so drift is caught early. When an adapter needs a new field:

1. Add the field to the canonical schema as optional.
2. Update adapters or CLI conformance mappers to emit the field.
3. Keep older producers conformant because the new field stays optional.

This gives the schema room to evolve without silently accepting arbitrary provider-specific shape drift.

## Future Work

- Add writeback schemas such as `issue.write.schema.json` using the same ownership pattern.
- Add schemas for other providers and file types after this proof is stable.
- Evaluate opt-in runtime validation once the schema set and performance tradeoffs are understood.
