# Integration Comparison Cases

These fixture-backed cases encode the GitHub, Linear, and Notion prompts used in
the MCP-vs-relayfile demos. They stay offline by default and can be swapped to a
configured mount by adding a case-level `Use Configured Mount` section.

## integrations.github-open-issues-via-mount
Executor: relayfile
Kind: capability
Tags: integrations, github, comparison
Human Review: true

### Message
List the open issues in the AgentWorkforce/relay GitHub repo through the relayfile mount.

### Mock
```json
{
  "files": {
    "/github/repos/AgentWorkforce/relay/issues/805.json": {
      "number": 805,
      "state": "open",
      "title": "Stabilize relayfile comparison evals",
      "labels": ["evals", "relayfile"]
    },
    "/github/repos/AgentWorkforce/relay/issues/815.json": {
      "number": 815,
      "state": "open",
      "title": "Measure token cost against MCP",
      "labels": ["cost"]
    }
  }
}
```

### Operations
```json
[
  { "op": "list", "path": "/github/repos/AgentWorkforce/relay/issues" },
  { "op": "read", "path": "/github/repos/AgentWorkforce/relay/issues/805.json" },
  { "op": "read", "path": "/github/repos/AgentWorkforce/relay/issues/815.json" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- 805.json
- 815.json
- state
- Stabilize relayfile comparison evals
- evals
- Measure token cost against MCP
fileExists:
- /github/repos/AgentWorkforce/relay/issues/805.json
maxToolCalls: 3

### Must
- Answer from the filesystem-shaped GitHub data, not by invoking a GitHub MCP.
- Preserve issue number, state, title, and labels in the evidence.

### Must Not
- Dump an entire oversized API response when a directory listing and grep suffice.

## integrations.linear-list-todo
Executor: relayfile
Kind: capability
Tags: integrations, linear, comparison
Human Review: true

### Message
List open Linear issues by state and verify the issue file shape.

### Mock
```json
{
  "files": {
    "/linear/issues/by-state/Todo/LIN-42.json": {
      "id": "issue-42",
      "identifier": "LIN-42",
      "title": "Compare Relayfile with MCP",
      "state": { "name": "Todo" },
      "assignee": { "name": "Khaliq" }
    },
    "/linear/issues/by-state/In Progress/LIN-77.json": {
      "id": "issue-77",
      "identifier": "LIN-77",
      "title": "Wire headless eval runner",
      "state": { "name": "In Progress" }
    }
  }
}
```

### Operations
```json
[
  { "op": "list", "path": "/linear/issues/by-state/Todo" },
  { "op": "read", "path": "/linear/issues/by-state/Todo/LIN-42.json" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- LIN-42
- Todo
- Compare Relayfile with MCP
fileContentIncludes:
- {"path":"/linear/issues/by-state/Todo/LIN-42.json","value":"\"identifier\": \"LIN-42\""}
- {"path":"/linear/issues/by-state/Todo/LIN-42.json","value":"\"state\""}
maxToolCalls: 2

### Must
- Demonstrate that the state directory is enough to answer a scoped Linear query.

### Must Not
- Require serial calls over every Linear issue.

## integrations.notion-yesterday-finds-edits-mcp-missed
Executor: relayfile
Kind: regression
Tags: integrations, notion, comparison, headline
Human Review: true

### Message
Use the configured mount (mountRoot below) — start by reading LAYOUT.md, then .skills/activity-summary.md — to answer: What did I work on yesterday (2026-05-12) across GH/Linear/Notion?

### Use Configured Mount
true

### Mock
```json
{
  "yesterday": "2026-05-12",
  "comparison": {
    "mcpMatches": 0,
    "mcpToolCalls": 12,
    "mcpTokens": 34268,
    "relayfileTokens": 28070,
    "mcpCostUsd": 0.36,
    "relayfileCostUsd": 0.24,
    "mcpMissed": "Khaliq's To Dos was visible as a Notion page file and did not require a title-exact MCP fetch."
  },
  "files": {
    "/notion/pages/_index.json": "pre-existing; filtered with jq date startswith(\"2026-05-12\")",
    "/linear/issues/_index.json": "pre-existing; filtered with jq date startswith(\"2026-05-12\")",
    "/linear/issues/by-title/relayfile-specific-tests.json": "pre-existing title lookup",
    "/notion/pages/by-title/khaliqs-to-dos__<short_id>.json": "pre-existing title lookup"
  }
}
```

### Expected jq commands
The agent should run these (or equivalent) as the primary discovery path:

```bash
# Read skill first
cat .skills/activity-summary.md

# Linear: one jq command over the index
jq '.[] | select(.updated | startswith("2026-05-12"))' linear/issues/_index.json

# Notion: one jq command over the index
jq '.[] | select(.updated | startswith("2026-05-12"))' notion/pages/_index.json

# GitHub: one jq command over the repo index
jq '.[] | select(.updated | startswith("2026-05-12"))' \
  github/repos/AgentWorkforce/relayfile/issues/_index.json
```

### Operations
```json
[
  { "op": "read", "path": "/LAYOUT.md" },
  { "op": "read", "path": "/.skills/activity-summary.md" },
  { "op": "jqFilter", "path": "/github/repos/AgentWorkforce/relayfile/issues/_index.json",
    "filter": ".[] | select(.updated | startswith(\"2026-05-12\"))" },
  { "op": "jqFilter", "path": "/linear/issues/_index.json",
    "filter": ".[] | select(.updated | startswith(\"2026-05-12\"))" },
  { "op": "jqFilter", "path": "/notion/pages/_index.json",
    "filter": ".[] | select(.updated | startswith(\"2026-05-12\"))" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- activity-summary
- _index.json
- 2026-05-12
- Khaliq's To Dos
- Integration Tracking
- Relayfile
fileExists:
- /.skills/activity-summary.md
- /notion/pages/_index.json
- /linear/issues/_index.json
metricLessThan:
- {"left":"relayfileTokens","right":"mcpTokens"}
- {"left":"relayfileCostUsd","right":"mcpCostUsd"}
metricGreaterThan:
- {"left":"relayfileMatches","right":"mcpMatches"}
maxToolCalls: 6

### Must
- Read LAYOUT.md first, then .skills/activity-summary.md before any exploration.
- Use `_index.json` + jq date filtering as the primary path — not `find -newermt`,
  not iterating individual files.
- Report results from all three providers: GitHub, Linear, and Notion.
- Use `linear/issues/by-title/` for named-issue lookups, not a full directory scan.

### Must Not
- Drop the Notion page because the title is fuzzy or apostrophe-normalized.
- Iterate over individual issue/page files to find the date — that is O(n) and
  the skill explicitly forbids it.
- Skip reading the skill file before doing discovery.
