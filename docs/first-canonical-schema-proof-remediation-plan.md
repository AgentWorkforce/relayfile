# First Canonical Schema Proof — Remediation Plan

## Status

- Date: 2026-04-15
- Scope: Fix test count miscount (9 -> 10) in proof documentation
- Prerequisites: [first-canonical-schema-proof-remediation-boundary.md](first-canonical-schema-proof-remediation-boundary.md)
- Checklist: [first-canonical-schema-proof-remediation-checklist.md](first-canonical-schema-proof-remediation-checklist.md)
- State: **Defined** — ready for implementation

## Overview

The first proof review (approved) identified one documentation correction: every proof and ownership review doc that states a test count says "9" when the actual count is 10. This plan defines the exact edits needed to close the finding.

## Root Cause

The 10th test, `TestValidateContentMissingOptionalArraysStillFails`, was added during implementation but the surrounding documentation was not updated from the original 9-test plan. The checklist's own test inventory table (lines 114–125) correctly lists all 10 tests — only the summary counts were missed.

## Step 1: Verify Baseline

**Gate:** Confirm the code is correct before editing docs.

```bash
go test ./internal/schema/... -v 2>&1 | grep -c "^--- PASS"
# Expected: 10

go build ./...
# Expected: success
```

No code changes are needed. The test file is correct.

## Step 2: Apply Five Documentation Edits

All edits are single-token replacements of "9" with "10" in context.

### Edit 1: `docs/first-canonical-schema-proof-boundary.md` line 22

In the "In Scope" table, Conformance tests row:

**Before:**
```
| Conformance tests | `internal/schema/validate_test.go` | 9 tests: adapter conformance, CLI conformance, negative cases, edge cases |
```

**After:**
```
| Conformance tests | `internal/schema/validate_test.go` | 10 tests: adapter conformance, CLI conformance, negative cases, edge cases |
```

### Edit 2: `docs/first-canonical-schema-proof-boundary.md` line 134

In the "Exit Criteria" section, item 5:

**Before:**
```
5. `internal/schema/validate_test.go` has 9 passing tests covering adapter conformance, CLI conformance, and negative/edge cases.
```

**After:**
```
5. `internal/schema/validate_test.go` has 10 passing tests covering adapter conformance, CLI conformance, and negative/edge cases.
```

### Edit 3: `docs/first-canonical-schema-proof-checklist.md` line 100

In the "Verification" section:

**Before:**
```
- [x] `go test ./internal/schema/...` passes all 9 tests
```

**After:**
```
- [x] `go test ./internal/schema/...` passes all 10 tests
```

### Edit 4: `docs/canonical-file-schema-ownership-review-verdict.md` line 53

In the "The proof is correctly scoped and complete" section:

**Before:**
```
- `internal/schema/validate_test.go` — 9 tests covering adapter conformance, CLI conformance, negative cases (missing fields, extra fields, invalid enums, unmapped raw CLI), nullable fields, and unknown paths.
```

**After:**
```
- `internal/schema/validate_test.go` — 10 tests covering adapter conformance, CLI conformance, negative cases (missing fields, extra fields, invalid enums, unmapped raw CLI), nullable fields, and unknown paths.
```

### Edit 5: `docs/canonical-file-schema-ownership-review-verdict.md` line 116

In the "Slice Honesty" section:

**Before:**
```
What this slice **delivers**: a JSON Schema file for GitHub issues, an embedded schema filesystem, a path pattern registry with evolution rules, a Go validation utility with caching, and 9 conformance tests covering adapter output, CLI output, and edge cases.
```

**After:**
```
What this slice **delivers**: a JSON Schema file for GitHub issues, an embedded schema filesystem, a path pattern registry with evolution rules, a Go validation utility with caching, and 10 conformance tests covering adapter output, CLI output, and edge cases.
```

## Step 3: Verify No Remaining Miscounts

```bash
grep -rn "9 tests\|9 conformance\|9 passing\|all 9" docs/first-canonical-schema-proof-*.md docs/canonical-file-schema-ownership-review-verdict.md
# Expected: no output
```

## Step 4: Confirm No Regression

```bash
go test ./internal/schema/...
go build ./...
```

Both must pass. Since no code was changed, this is a sanity gate only.

## Files Modified (Complete List)

| File | Change |
|------|--------|
| `docs/first-canonical-schema-proof-boundary.md` | "9 tests" -> "10 tests" (2 locations) |
| `docs/first-canonical-schema-proof-checklist.md` | "all 9 tests" -> "all 10 tests" (1 location) |
| `docs/canonical-file-schema-ownership-review-verdict.md` | "9 tests" / "9 conformance tests" -> "10" (2 locations) |

## Files NOT Modified

- `internal/schema/validate.go` — no code changes
- `internal/schema/validate_test.go` — already has 10 tests, correct as-is
- `schemas/github/issue.schema.json` — schema unchanged
- `schemas/embed.go` — embed unchanged
- `schemas/README.md` — registry unchanged
- `docs/first-canonical-schema-proof-plan.md` — already says "10 conformance and edge case tests" in its Files Created table; no independent miscount
- `docs/first-canonical-schema-proof-review-verdict.md` — this is the review that found the issue; it correctly states "10 test functions"
- `internal/relayfile/` — no changes
- Any external repo — no changes

## Done Criteria

1. All five edits from Step 2 are applied.
2. `grep` in Step 3 returns no matches.
3. `go test` and `go build` in Step 4 pass.
4. The proof narrative is internally consistent: every document that states a test count says 10, matching the 10 `Test*` functions in `internal/schema/validate_test.go`.

## Risk

| Risk | Severity | Mitigation |
|------|----------|------------|
| Typo in edit introduces new inconsistency | Low | Grep verification in Step 3 catches remaining miscounts |
| Accidental code change | None | Plan specifies documentation-only edits; checklist gates on "no code files modified" |

This is a minimal, bounded, documentation-only remediation. It closes the single finding from the first proof review without changing any code, schema, or architectural boundary.
