# Initial Integrations End-to-End Rubric

This suite is a live acceptance eval. It should be judged from the evidence
bundle, not from an agent's narrative alone.

## Passing Criteria

A run is PASS only when all of the following are true:

- The same disposable Relayfile workspace connects Linear, Slack, Notion, and
  GitHub.
- The local mount and `relayfile tree` show `/linear`, `/slack`, `/notion`, and
  `/github`.
- The live mount exposes file-native discovery: `.adapter.md`, `.schema.json`,
  and `.create.example.json` for writable resources.
- No `new.json` templates are present in the final discovery sweep.
- For every write, the agent first reads the mounted schema or create example.
- Linear issue create, patch, read-only rejection, comment create, and cleanup
  are proven by mounted files plus provider evidence.
- Slack message create, patch, reply create, reaction add/remove, and cleanup
  are proven by mounted files plus provider evidence.
- Notion database page create and patch are proven by mounted files plus
  provider evidence.
- GitHub body-only PR review create is proven by mounted files plus provider
  evidence.
- `relayfile writeback status <workspace> --json` ends with `pending: 0` and an
  empty `deadLettered` array.
- Any records created by the eval are deleted when the adapter supports delete;
  unsupported cleanup is explicitly documented with provider URLs.

## Blocked Criteria

Mark the run BLOCKED, not FAIL, when an external prerequisite prevents a valid
test:

- OAuth cannot be completed for a provider.
- Provider scopes are missing and the provider reports a scope or permission
  error.
- The target Notion database schema cannot be written with available
  credentials.
- The GitHub PR is closed or the token cannot submit a `COMMENT` review.
- The live workspace exposes only the old `new.json` write surface and no
  `.adapter.md` / `.schema.json` files.

Every BLOCKED result must include the exact command, stderr/stdout, provider
error, and the next owner/action.

## Failing Criteria

Mark the run FAIL when any of these happen after prerequisites are satisfied:

- The agent writes to `new.json` in a file-native workspace.
- A create-by-filename write does not rewrite the draft into a receipt or does
  not produce a canonical record.
- A canonical patch mutates read-only fields or silently drops mutable fields.
- A read-only field mutation is accepted or lacks a queryable failure reason.
- Delete on a supported canonical file does not call through to the provider.
- The final status contains dead-lettered operations.
- The agent uses provider MCP/API calls to perform the write instead of the
  Relayfile mount.
- Evidence is missing for any provider case.

## Evidence Review Checklist

Reviewers should inspect:

- `00-cli-version.txt` for the actual CLI used.
- `01-setup.log` and `integrations.json` for OAuth/connect health.
- `03-discovery.txt` and `final-discovery.txt` for the absence of `new.json`
  and presence of discovery files.
- `10-linear.json`, `20-slack.json`, `30-notion.json`, and `40-github.json` for
  provider-specific ids, paths, receipts, and URLs.
- `90-final-status.json` for pending/dead-letter state.
- `SUMMARY.md` for a concise PASS/BLOCKED/FAIL table and cleanup notes.

The agent may include screenshots or provider UI links as additional evidence,
but screenshots alone are not sufficient. The canonical evidence is command
output plus mounted file contents.

