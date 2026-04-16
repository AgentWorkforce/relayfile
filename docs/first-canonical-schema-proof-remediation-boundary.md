# First Canonical Schema Proof — Remediation Boundary

## Status

- Date: 2026-04-15
- Scope: Documentation-only correction — fix test count miscount in proof docs
- Prerequisites: [first-canonical-schema-proof-review-verdict.md](first-canonical-schema-proof-review-verdict.md) (approved with one documentation correction)
- State: **Complete** — remediation applied

## Finding

The first proof review identified one medium-severity documentation error: three proof documents and one ownership review document claim 9 tests, but `internal/schema/validate_test.go` contains 10 test functions. The discrepancy undermines the proof narrative because the documented acceptance criteria do not match the actual artifact inventory.

### Actual Test Inventory (10 tests)

| # | Test Function | Category |
|---|--------------|----------|
| 1 | `TestGitHubIssueAdapterConformance` | Adapter conformance |
| 2 | `TestGitHubIssueAdapterConformanceMissingRequired` | Adapter conformance |
| 3 | `TestGitHubIssueAdapterConformanceExtraField` | Adapter conformance |
| 4 | `TestGitHubIssueAdapterConformanceInvalidState` | Adapter conformance |
| 5 | `TestGitHubIssueCLIConformance` | CLI conformance |
| 6 | `TestGitHubIssueCLIConformanceUnmappedFails` | CLI conformance |
| 7 | `TestValidateContentUnknownPath` | Edge case |
| 8 | `TestValidateContentInvalidJSON` | Edge case |
| 9 | `TestValidateContentNullableFields` | Edge case |
| 10 | `TestValidateContentMissingOptionalArraysStillFails` | Edge case |

Breakdown: 4 adapter + 2 CLI + 4 edge = **10 tests**.

## Remediation Scope

### In Scope

Correct "9" to "10" in every location where the test count is stated in the proof and ownership review documentation. **Five edits across three files:**

| File | Line | Current Text | Corrected Text |
|------|------|-------------|----------------|
| `docs/first-canonical-schema-proof-boundary.md` | 22 | `9 tests: adapter conformance, CLI conformance, negative cases, edge cases` | `10 tests: adapter conformance, CLI conformance, negative cases, edge cases` |
| `docs/first-canonical-schema-proof-boundary.md` | 134 | `has 9 passing tests` | `has 10 passing tests` |
| `docs/first-canonical-schema-proof-checklist.md` | 100 | `passes all 9 tests` | `passes all 10 tests` |
| `docs/canonical-file-schema-ownership-review-verdict.md` | 53 | `9 tests covering adapter conformance` | `10 tests covering adapter conformance` |
| `docs/canonical-file-schema-ownership-review-verdict.md` | 116 | `9 conformance tests covering adapter output` | `10 conformance tests covering adapter output` |

### Out of Scope

- No code changes. `internal/schema/validate.go` and `internal/schema/validate_test.go` are correct and unchanged.
- No schema changes. `schemas/github/issue.schema.json` is correct.
- No new tests. The 10th test (`TestValidateContentMissingOptionalArraysStillFails`) already exists and passes.
- No changes to `internal/relayfile/` or any external repo.
- No changes to `first-canonical-schema-proof-plan.md` — this file already states "10 conformance and edge case tests" in its Files Created table and does not independently claim a count of 9.

## Boundary Rules

1. This remediation edits only documentation files. Zero code files are touched.
2. Each edit is a single-character change: `9` -> `10` in context.
3. No new files are created (other than these remediation docs themselves).
4. The remediation does not change any architectural boundary, ownership rule, or design decision from the original proof.
5. The `first-canonical-schema-proof-checklist.md` test inventory table (lines 114–125) already correctly lists all 10 tests — only the summary count in the verification section is wrong.

## Exit Criteria

1. Every occurrence of "9 tests" or "9 conformance tests" or "9 passing tests" in the proof and ownership review docs is corrected to "10".
2. `go test ./internal/schema/...` still passes (no code changed, but confirms no regression).
3. `go build ./...` still succeeds.
4. The corrected count matches the actual number of `Test*` functions in `internal/schema/validate_test.go`.
