# Emitted-Shape Canonical Conformance — Plan

## Status

- Date: 2026-04-16
- Boundary: [emitted-shape-canonical-conformance-boundary.md](emitted-shape-canonical-conformance-boundary.md)
- Checklist: [emitted-shape-canonical-conformance-checklist.md](emitted-shape-canonical-conformance-checklist.md)

## Goal

Replace hand-authored test payloads in `internal/schema/validate_test.go` with fixtures generated from real producer code where available, closing the core adapter-side evidence gap identified in the remediation review. After this work, the first canonical schema proof is grounded in real adapter-emitted shapes with documented provenance, while the CLI side remains documented derived evidence until a shipped producer path exists.

## Key Insight: The Adapter Already Proves the Shape

The critical discovery from reading the adapter codebase is that the evidence already exists — it just hasn't been brought into core relayfile:

1. `relayfile-adapters/packages/github/src/issues/issue-mapper.ts` exports `mapIssue()` which transforms GitHub API responses into the canonical shape.
2. `relayfile-adapters/packages/github/src/__tests__/fixtures/index.ts` contains `mockIssuePayload` — a realistic GitHub REST API issue response.
3. `relayfile-adapters/packages/github/src/issues/__tests__/issue-mapping.test.ts` line 132-157 already asserts that `mapIssue(mockIssuePayload)` produces a specific JSON object with exactly the 12 fields the canonical schema requires.

The adapter test's expected output at lines 139-156 is:

```json
{
  "assignees": ["monalisa"],
  "author": {
    "avatarUrl": "https://avatars.githubusercontent.com/u/3?v=4",
    "login": "hubot"
  },
  "body": "We need E2E coverage for issue ingestion and webhook routing.",
  "closed_at": null,
  "created_at": "2026-03-25T10:00:00Z",
  "html_url": "https://github.com/octocat/hello-world/issues/10",
  "labels": ["bug"],
  "milestone": null,
  "number": 10,
  "state": "open",
  "title": "Track adapter issue ingestion coverage",
  "updated_at": "2026-03-28T07:45:00Z"
}
```

This is the real emitted shape. The plan's job is to capture this as a fixture in core relayfile and validate it against the canonical schema.

## Implementation Steps

### Step 1: Create testdata directory and raw input fixtures

Create `internal/schema/testdata/` with the raw input fixtures that document what each producer receives as input.

**File: `internal/schema/testdata/github-issue-adapter-raw-input.json`**

This is `mockIssuePayload` from the adapter fixtures with all template literals resolved. It represents a real GitHub REST API issue response — the input to `mapIssue()`. Key characteristics:
- `user` object (not `author`)
- Nested `labels` with `id`, `name`, `color`, `description`
- Nested `assignees` with `login`, `id`, `node_id`, `avatar_url`
- `state: "open"` (GitHub REST API uses lowercase, unlike GraphQL's `OPEN`)
- `reactions` object, `author_association`, `comments` count — fields the canonical schema strips

**File: `internal/schema/testdata/github-issue-cli-raw-input.json`**

A GitHub CLI (`gh issue view --json`) response shape. Key differences from the REST API:
- `state: "OPEN"` (uppercase, from GraphQL)
- `createdAt`, `updatedAt`, `closedAt` (camelCase, from GraphQL)
- `url` (not `html_url`)
- `user` with `avatar_url` and `login`
- Nested `labels` and `assignees` objects with extra fields

### Step 2: Generate adapter-emitted fixture

**File: `internal/schema/testdata/generate-fixtures.ts`**

```typescript
import { mapIssue } from '../../../../relayfile-adapters/packages/github/src/issues/issue-mapper.js';
import { readFileSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';

const rawInput = JSON.parse(
  readFileSync(new URL('./github-issue-adapter-raw-input.json', import.meta.url), 'utf-8')
);

const result = mapIssue(rawInput, 'octocat', 'hello-world');
const emitted = JSON.parse(result.content);

writeFileSync(
  new URL('./github-issue-adapter-emitted.json', import.meta.url),
  JSON.stringify(emitted, null, 2) + '\n'
);

// Record provenance
const adapterCommit = execSync('git -C ../../../../relayfile-adapters rev-parse HEAD', { encoding: 'utf-8' }).trim();
console.log(`Generated adapter-emitted fixture from relayfile-adapters commit ${adapterCommit}`);
```

Run with `npx tsx internal/schema/testdata/generate-fixtures.ts`.

The script produces `github-issue-adapter-emitted.json` — the exact output of `mapIssue()` run against the raw input fixture. This is **Emitted** provenance: real producer code, representative input, captured output.

**Fallback if cross-repo import fails**: If the adapter repo is not available or the import path doesn't resolve, extract the expected output from the adapter test assertion (lines 139-156 of `issue-mapping.test.ts`) and document it as **Emitted (snapshot)** provenance — the shape was asserted by the adapter's own test suite, just not generated live. This is weaker than a live generation but stronger than hand-authoring, because the adapter test is the adapter team's own assertion of what `mapIssue()` produces.

### Step 3: Generate CLI mapped fixture

Apply `mapCLIToCanonical()` logic (the Go helper in `validate_test.go`) to `github-issue-cli-raw-input.json` and write the result to `github-issue-cli-mapped.json`.

This can be done by:
1. Writing a small Go script in `testdata/` that loads the CLI raw input, applies the mapping, and writes the output.
2. Or manually applying the transform and checking in the result, with the generation method documented in PROVENANCE.md.

Option 2 is simpler for this slice. The transform is well-documented and deterministic:
- Flatten `labels` from `[{name, id, color}]` to `["name1"]`
- Extract `assignees` logins from `[{login, id}]` to `["login1"]`
- Map `user` to `author` with `{avatarUrl, login}` (rename `avatar_url` to `avatarUrl`)
- Lowercase `state` (`"OPEN"` -> `"open"`)
- Rename `createdAt` -> `created_at`, `updatedAt` -> `updated_at`, `closedAt` -> `closed_at`
- Rename `url` -> `html_url`

### Step 4: Write provenance file

**File: `internal/schema/testdata/PROVENANCE.md`**

```markdown
# Fixture Provenance

## github-issue-adapter-raw-input.json

- **Provenance**: Raw Input
- **Source**: `relayfile-adapters/packages/github/src/__tests__/fixtures/index.ts` (`mockIssuePayload`)
- **Description**: GitHub REST API issue response as received by the adapter. This is the input to `mapIssue()`.
- **Generation**: Template literals resolved manually from the fixture source file.

## github-issue-adapter-emitted.json

- **Provenance**: Emitted
- **Source**: Output of `mapIssue(rawInput, "octocat", "hello-world")` from `relayfile-adapters/packages/github/src/issues/issue-mapper.ts`
- **Generation**: `npx tsx internal/schema/testdata/generate-fixtures.ts`
- **Adapter commit**: {commit hash recorded at generation time}
- **Generated**: {date}
- **Cross-check**: Must match the expected output asserted in `relayfile-adapters/packages/github/src/issues/__tests__/issue-mapping.test.ts` lines 139-156.

## github-issue-cli-raw-input.json

- **Provenance**: Raw Input
- **Source**: Constructed to match the shape of `gh issue view --json number,title,state,body,labels,assignees,user,milestone,createdAt,updatedAt,closedAt,url` output.
- **Description**: GitHub CLI / GraphQL-style issue response. Key differences from REST API: uppercase `state`, camelCase timestamps, `url` instead of `html_url`.
- **Generation**: Hand-constructed from GitHub CLI documentation and the REST API fixture data.

## github-issue-cli-mapped.json

- **Provenance**: Derived
- **Source**: Output of applying `mapCLIToCanonical()` from `internal/schema/validate_test.go` to `github-issue-cli-raw-input.json`.
- **Description**: The canonical schema shape after CLI mapping. `mapCLIToCanonical()` is test-only code in `validate_test.go`, not a shipped CLI tool. This fixture proves the intended mapping works, not that a shipped CLI tool produces it.
- **Generation**: Transform applied manually following the documented mapping rules.
```

### Step 5: Update Go tests

Modify `internal/schema/validate_test.go`:

**Add fixture loader:**

```go
func loadFixture(t *testing.T, name string) []byte {
    t.Helper()
    data, err := os.ReadFile(filepath.Join("testdata", name))
    if err != nil {
        t.Fatalf("load fixture %s: %v", name, err)
    }
    return data
}
```

**Update `TestGitHubIssueAdapterConformance`:**

```go
func TestGitHubIssueAdapterConformance(t *testing.T) {
    // Provenance: Emitted — generated by mapIssue() in relayfile-adapters
    fixture := loadFixture(t, "github-issue-adapter-emitted.json")
    err := ValidateContent(issueMetaPath, fixture)
    if err != nil {
        t.Fatalf("ValidateContent returned error: %v", err)
    }
}
```

**Update `TestGitHubIssueCLIConformance`:**

```go
func TestGitHubIssueCLIConformance(t *testing.T) {
    // Provenance: Derived — mapCLIToCanonical() is test-only, not a shipped producer
    rawFixture := loadFixture(t, "github-issue-cli-raw-input.json")
    var raw map[string]any
    if err := json.Unmarshal(rawFixture, &raw); err != nil {
        t.Fatalf("unmarshal CLI raw fixture: %v", err)
    }

    mapped := mapCLIToCanonical(raw)
    content := mustJSON(t, mapped)

    // Validate against canonical schema
    err := ValidateContent(issueMetaPath, content)
    if err != nil {
        t.Fatalf("ValidateContent returned error: %v", err)
    }

    // Cross-check: mapped output must match the checked-in fixture
    expectedFixture := loadFixture(t, "github-issue-cli-mapped.json")
    var expected map[string]any
    if err := json.Unmarshal(expectedFixture, &expected); err != nil {
        t.Fatalf("unmarshal CLI mapped fixture: %v", err)
    }
    if !reflect.DeepEqual(mapped, expected) {
        t.Fatalf("mapCLIToCanonical output does not match github-issue-cli-mapped.json fixture")
    }
}
```

**Leave unchanged:** `TestGitHubIssueAdapterConformanceMissingRequired`, `TestGitHubIssueAdapterConformanceExtraField`, `TestGitHubIssueAdapterConformanceInvalidState`, `TestGitHubIssueCLIConformanceUnmappedFails`, `TestValidateContentUnknownPath`, `TestValidateContentInvalidJSON`, `TestValidateContentNullableFields`, `TestValidateContentMissingOptionalArraysStillFails`.

### Step 6: Verification

1. `go test ./internal/schema/...` — all 10 tests pass.
2. `go build ./...` — succeeds.
3. Manual audit: `github-issue-adapter-emitted.json` matches the adapter test's expected output.
4. Manual audit: raw input fixtures fail `ValidateContent()` (they are not canonical shape).

## File Inventory

| File | New/Modified | Purpose |
|------|-------------|---------|
| `internal/schema/testdata/github-issue-adapter-raw-input.json` | New | Raw GitHub API input to adapter |
| `internal/schema/testdata/github-issue-adapter-emitted.json` | New | Adapter-emitted canonical fixture (Emitted provenance) |
| `internal/schema/testdata/github-issue-cli-raw-input.json` | New | Raw CLI-style input |
| `internal/schema/testdata/github-issue-cli-mapped.json` | New | CLI-mapped canonical fixture (Derived provenance) |
| `internal/schema/testdata/PROVENANCE.md` | New | Fixture provenance documentation |
| `internal/schema/testdata/generate-fixtures.ts` | New | Adapter fixture generation script |
| `internal/schema/validate_test.go` | Modified | Load fixtures instead of hand-authored payloads |
| `docs/emitted-shape-canonical-conformance-boundary.md` | New | Boundary document |
| `docs/emitted-shape-canonical-conformance-checklist.md` | New | Checklist document |
| `docs/emitted-shape-canonical-conformance-plan.md` | New | This plan |

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Adapter repo not locally available | Low | Blocks fixture generation | Use adapter test's expected output as fallback (document honestly) |
| `mapIssue()` import path breaks | Medium | Blocks fixture generation | Update import path in generation script |
| Adapter output doesn't match schema | Very low | Signals real conformance issue | Investigate — this is the proof working as intended |
| CLI raw input shape is inaccurate | Low | Weakens CLI fixture | CLI fixture is already Derived provenance; document the limitation |

## What Comes After

Once this proof is accepted:

1. **CI fixture regeneration** — add a GitHub Action that regenerates adapter fixtures on adapter releases and opens a PR if the output changes.
2. **Additional providers** — extend the pattern to Slack, Linear, Notion using the same fixture/provenance model.
3. **Shipped CLI mapper** — promote `mapCLIToCanonical()` from test-only to a real package, upgrading CLI fixture provenance from Derived to Emitted.
4. **Writeback schemas** — define `issue.write.schema.json` and validate writeback payloads with the same fixture-based approach.
