# First Canonical Schema Proof — Remediation Checklist

## Status

- Date: 2026-04-15
- Tracks: [first-canonical-schema-proof-remediation-boundary.md](first-canonical-schema-proof-remediation-boundary.md)
- State: **Defined** — ready for implementation

## Pre-Flight

- [ ] Confirm `internal/schema/validate_test.go` has exactly 10 `Test*` functions
- [ ] Confirm `go test ./internal/schema/...` passes before edits

## Documentation Edits

### 1. First Proof Boundary Doc

- [ ] `docs/first-canonical-schema-proof-boundary.md` line 22: change `9 tests` to `10 tests`
- [ ] `docs/first-canonical-schema-proof-boundary.md` line 134: change `9 passing tests` to `10 passing tests`

### 2. First Proof Checklist Doc

- [ ] `docs/first-canonical-schema-proof-checklist.md` line 100: change `all 9 tests` to `all 10 tests`

### 3. Ownership Review Verdict Doc

- [ ] `docs/canonical-file-schema-ownership-review-verdict.md` line 53: change `9 tests` to `10 tests`
- [ ] `docs/canonical-file-schema-ownership-review-verdict.md` line 116: change `9 conformance tests` to `10 conformance tests`

## Verification

- [ ] All five edits applied
- [ ] No remaining occurrences of "9 tests" or "9 conformance tests" or "all 9" in proof or ownership docs
- [ ] `go test ./internal/schema/...` still passes (regression check)
- [ ] `go build ./...` still succeeds (regression check)
- [ ] Count in docs matches actual `Test*` function count in `internal/schema/validate_test.go`

## Gate

- [ ] All checklist items checked
- [ ] Remediation is documentation-only — no code files modified
