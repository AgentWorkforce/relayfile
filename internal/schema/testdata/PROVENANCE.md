# Fixture Provenance

Generated and verified for the emitted-shape canonical conformance proof on 2026-04-16.

## github-issue-adapter-raw-input.json

- Provenance: Raw Input
- Source: `relayfile-adapters/packages/github/src/__tests__/fixtures/index.ts` (`mockIssuePayload`)
- Description: GitHub REST API issue payload as received by the GitHub adapter before canonical mapping.
- Generation: Produced by `internal/schema/testdata/generate-fixtures.ts` from the adapter repo fixture export.
- Adapter commit: `6c0becb476989f8f1bf034b14d64383e7001e3be`

## github-issue-adapter-emitted.json

- Provenance: Emitted
- Source: `mapIssue()` in `relayfile-adapters/packages/github/src/issues/issue-mapper.ts`
- Description: Exact canonical `meta.json` payload emitted by the real adapter for the captured raw input fixture.
- Generation: `npx --yes tsx internal/schema/testdata/generate-fixtures.ts`
- Adapter commit: `6c0becb476989f8f1bf034b14d64383e7001e3be`
- Cross-check: Matches the expected `JSON.parse(mapped.content)` assertion in `relayfile-adapters/packages/github/src/issues/__tests__/issue-mapping.test.ts`

## github-issue-cli-raw-input.json

- Provenance: Raw Input
- Source: Representative GitHub CLI issue payload shape using the same issue data as the adapter fixture.
- Description: Mirrors the documented `gh issue view --json` style field conventions used by the test-only CLI mapping path: uppercase `state`, camelCase timestamps, nested `labels` and `assignees`, `user`, and `url`.
- Generation: Checked in as a stable fixture to exercise the mapping boundary without claiming a shipped CLI producer exists.

## github-issue-cli-mapped.json

- Provenance: Derived
- Source: Output of applying `mapCLIToCanonical()` from `internal/schema/validate_test.go` to `github-issue-cli-raw-input.json`
- Description: Canonical shape expected after the documented CLI mapping transform. This is Derived, not Emitted: `mapCLIToCanonical()` is test-only code, not a shipped CLI producer.
- Generation: Checked in as a deterministic expected fixture and compared in `TestGitHubIssueCLIConformance`

## Regeneration

Run this from the repo root with the sibling `relayfile-adapters` repository available:

```bash
npx --yes tsx internal/schema/testdata/generate-fixtures.ts
```
