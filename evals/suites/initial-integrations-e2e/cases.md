# Initial Integrations End-to-End Evals

These cases validate the hosted Relayfile path for the initial provider set:
Linear, Slack, Notion, and GitHub. They are live-provider evals, not offline
fixtures. Run them only against disposable provider resources and record the
evidence bundle described below.

The eval's purpose is to catch gaps between the published adapter package, the
cloud writeback bridge, the relayfile CLI/mount, and the agent-facing skill. A
passing run proves that an agent can discover the write contract from mounted
files, create records with non-canonical filenames, patch canonical files,
delete supported canonical files, and diagnose failures through relayfile
status surfaces.

## Global Preconditions

Required local tools:

```bash
relayfile --help
jq --version
node --version
```

Required provider setup:

- A disposable Agent Relay Cloud workspace name, for example
  `rf-e2e-$(date -u +%Y%m%d-%H%M%S)`.
- A local mount directory outside any source repo, for example
  `/tmp/relayfile-initial-integrations-e2e`.
- OAuth access for Linear, Slack, Notion, and GitHub through
  `relayfile integration connect`.
- A Linear team that can receive test issues. Export `EVAL_LINEAR_TEAM_ID`.
- A Slack channel where the bot may post, edit, react, and delete messages.
  Export `EVAL_SLACK_CHANNEL_ID`.
- A Notion database shared with the integration. Export
  `EVAL_NOTION_DATABASE_ID`. The database must accept the properties in its
  mounted `.create.example.json`; if not, update the create document according
  to the mounted schema before writing.
- A GitHub repository and open pull request where the integration may submit a
  test review. Export `EVAL_GITHUB_OWNER`, `EVAL_GITHUB_REPO`, and
  `EVAL_GITHUB_PULL_NUMBER`.

Run metadata:

```bash
export EVAL_RUN_ID="rf-e2e-$(date -u +%Y%m%d-%H%M%S)"
export EVAL_WORKSPACE="$EVAL_RUN_ID"
export EVAL_LOCAL_DIR="/tmp/$EVAL_RUN_ID"
export EVAL_EVIDENCE_DIR="$PWD/docs/evidence/$EVAL_RUN_ID"
mkdir -p "$EVAL_EVIDENCE_DIR"
```

Safety rules:

- Only delete records created by this eval run.
- Prefix every provider object title/body/message with `$EVAL_RUN_ID`.
- Do not use existing business records as patch/delete targets.
- If a provider lacks the required OAuth scope, stop and record
  `BLOCKED_MISSING_PROVIDER_SCOPE` with the provider error.
- If the mounted workspace still exposes `new.json` templates and no
  `.adapter.md` / `.schema.json` files, stop and record
  `BLOCKED_OLD_ADAPTER_SURFACE`. That is a deployment or sync problem, not a
  passing file-native writeback run.

Evidence bundle:

Every run must write these files under `$EVAL_EVIDENCE_DIR`:

- `00-cli-version.txt`: `which relayfile`, `relayfile --help`, and timestamp.
- `01-setup.log`: setup, connect, mount, pull, and status commands.
- `02-tree-before.txt`: provider tree snapshots before writes.
- `03-discovery.txt`: all `.adapter.md`, `.schema.json`,
  `.create.example.json`, and `new.json` paths found in the mount.
- `04-writeback-status-before.json`: `relayfile writeback status --json`.
- `10-linear.json`: Linear operation paths, receipts, canonical record ids,
  status output, and provider URL.
- `20-slack.json`: Slack operation paths, receipts, canonical timestamp ids,
  status output, and provider permalinks if available.
- `30-notion.json`: Notion operation paths, receipts, canonical page ids,
  status output, and provider URL.
- `40-github.json`: GitHub review operation path, receipt or resulting review
  id, status output, and provider URL.
- `90-final-status.json`: final `relayfile writeback status --json`,
  `relayfile status`, and any dead-letter records.
- `SUMMARY.md`: human-readable result with PASS/BLOCKED/FAIL for each case.

Common polling helpers:

Use bounded polling instead of fixed sleeps when waiting for receipts, canonical
records, or writeback drain. The exact timeout may be adjusted for provider
latency, but every timeout must be recorded as BLOCKED or FAIL with the last
observed file/status.

```bash
wait_for_file_contains() {
  target="$1"
  needle="$2"
  timeout_seconds="${3:-180}"
  start="$(date +%s)"
  while true; do
    if [ -f "$target" ] && grep -Fq "$needle" "$target"; then
      return 0
    fi
    if [ "$(($(date +%s) - start))" -ge "$timeout_seconds" ]; then
      echo "TIMEOUT waiting for $target to contain $needle" >&2
      return 1
    fi
    sleep 5
  done
}

wait_for_writeback_drain() {
  timeout_seconds="${1:-180}"
  start="$(date +%s)"
  while true; do
    relayfile writeback status "$EVAL_WORKSPACE" --json > "$EVAL_EVIDENCE_DIR/writeback-status-current.json"
    pending="$(jq -r '.pending // 0' "$EVAL_EVIDENCE_DIR/writeback-status-current.json")"
    dead_count="$(jq -r '(.deadLettered // []) | length' "$EVAL_EVIDENCE_DIR/writeback-status-current.json")"
    if [ "$pending" = "0" ] && [ "$dead_count" = "0" ]; then
      return 0
    fi
    if [ "$dead_count" != "0" ]; then
      cat "$EVAL_EVIDENCE_DIR/writeback-status-current.json" >&2
      return 2
    fi
    if [ "$(($(date +%s) - start))" -ge "$timeout_seconds" ]; then
      cat "$EVAL_EVIDENCE_DIR/writeback-status-current.json" >&2
      return 1
    fi
    sleep 5
  done
}

wait_for_provider_roots() {
  timeout_seconds="${1:-180}"
  start="$(date +%s)"
  while true; do
    relayfile tree "$EVAL_WORKSPACE" / --depth 3 > "$EVAL_EVIDENCE_DIR/tree-current.txt"
    if grep -Fq "/linear" "$EVAL_EVIDENCE_DIR/tree-current.txt" \
      && grep -Fq "/slack" "$EVAL_EVIDENCE_DIR/tree-current.txt" \
      && grep -Fq "/notion" "$EVAL_EVIDENCE_DIR/tree-current.txt" \
      && grep -Fq "/github" "$EVAL_EVIDENCE_DIR/tree-current.txt"; then
      return 0
    fi
    if [ "$(($(date +%s) - start))" -ge "$timeout_seconds" ]; then
      cat "$EVAL_EVIDENCE_DIR/tree-current.txt" >&2
      return 1
    fi
    sleep 5
  done
}
```

## initial-e2e.setup-connect-mount

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, setup, mount, cloud, oauth
Human Review: true

### Message
Create or reuse a disposable hosted Relayfile workspace, connect the initial
integrations, mount it locally, and prove the mounted filesystem contains all
four provider roots.

### Procedure

```bash
{
  date -u
  which relayfile
  relayfile --help
} | tee "$EVAL_EVIDENCE_DIR/00-cli-version.txt"

relayfile setup \
  --provider none \
  --workspace "$EVAL_WORKSPACE" \
  --local-dir "$EVAL_LOCAL_DIR" \
  --no-open \
  --skip-mount 2>&1 | tee "$EVAL_EVIDENCE_DIR/01-setup.log"

for provider in linear slack notion github; do
  relayfile integration connect "$provider" \
    --workspace "$EVAL_WORKSPACE" \
    --no-open 2>&1 | tee -a "$EVAL_EVIDENCE_DIR/01-setup.log"
done

relayfile integration list --workspace "$EVAL_WORKSPACE" --json \
  | tee "$EVAL_EVIDENCE_DIR/integrations.json"

relayfile pull --workspace "$EVAL_WORKSPACE" --reason "$EVAL_RUN_ID initial e2e" \
  2>&1 | tee -a "$EVAL_EVIDENCE_DIR/01-setup.log"

relayfile mount "$EVAL_WORKSPACE" "$EVAL_LOCAL_DIR" --background \
  2>&1 | tee -a "$EVAL_EVIDENCE_DIR/01-setup.log"

wait_for_provider_roots 180
wait_for_writeback_drain 180
relayfile status "$EVAL_WORKSPACE" | tee "$EVAL_EVIDENCE_DIR/status-after-mount.txt"
relayfile tree "$EVAL_WORKSPACE" / --depth 3 | tee "$EVAL_EVIDENCE_DIR/02-tree-before.txt"
relayfile writeback status "$EVAL_WORKSPACE" --json \
  | tee "$EVAL_EVIDENCE_DIR/04-writeback-status-before.json"
```

### Deterministic Checks

ok: true
contentIncludes:
- /linear
- /slack
- /notion
- /github
- pending writebacks: 0
fileExists:
- $EVAL_EVIDENCE_DIR/00-cli-version.txt
- $EVAL_EVIDENCE_DIR/01-setup.log
- $EVAL_EVIDENCE_DIR/02-tree-before.txt

### Must

- Use `--no-open` and copy OAuth URLs into the evidence log.
- Show all four providers as connected or explicitly blocked by OAuth.
- Show a local mirror path and daemon state in `relayfile status`.

### Must Not

- Continue to writeback cases if any required provider is disconnected.
- Use a non-disposable workspace without an explicit written note in
  `SUMMARY.md`.

## initial-e2e.discovery-contract

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, discovery, schema, adapters
Human Review: true

### Message
Prove that the mounted workspace exposes discoverable write contracts for the
initial adapters and no longer depends on magic `new.json` templates.

### Procedure

```bash
find "$EVAL_LOCAL_DIR" \
  \( -name '.adapter.md' -o -name '.schema.json' -o -name '.create.example.json' -o -name 'new.json' \) \
  | sort | tee "$EVAL_EVIDENCE_DIR/03-discovery.txt"

if grep -q '/new.json$' "$EVAL_EVIDENCE_DIR/03-discovery.txt"; then
  echo "BLOCKED_OLD_ADAPTER_SURFACE: new.json templates still mounted" \
    | tee -a "$EVAL_EVIDENCE_DIR/SUMMARY.md"
  exit 20
fi

if ! grep -q '/\.adapter\.md$' "$EVAL_EVIDENCE_DIR/03-discovery.txt"; then
  echo "BLOCKED_NO_ADAPTER_DISCOVERY: no .adapter.md files mounted" \
    | tee -a "$EVAL_EVIDENCE_DIR/SUMMARY.md"
  exit 21
fi

node <<'NODE' | tee "$EVAL_EVIDENCE_DIR/schema-validation.json"
const fs = require("fs");
const path = require("path");
const root = process.env.EVAL_LOCAL_DIR;
const schemas = [];
function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full);
    else if (entry.name === ".schema.json") schemas.push(full);
  }
}
walk(root);
const result = [];
for (const schemaPath of schemas) {
  const schema = JSON.parse(fs.readFileSync(schemaPath, "utf8"));
  const props = schema.properties || {};
  const readOnly = Object.entries(props)
    .filter(([, value]) => value && value.readOnly === true)
    .map(([key]) => key);
  result.push({
    path: path.relative(root, schemaPath),
    draft202012: schema.$schema === "https://json-schema.org/draft/2020-12/schema",
    hasProperties: Object.keys(props).length > 0,
    readOnly
  });
}
console.log(JSON.stringify(result, null, 2));
if (result.length === 0) process.exit(2);
if (result.some((item) => !item.draft202012 || !item.hasProperties)) process.exit(3);
NODE
```

### Deterministic Checks

ok: true
contentIncludes:
- .adapter.md
- .schema.json
- .create.example.json
forbidPhrases:
- /new.json
- BLOCKED_OLD_ADAPTER_SURFACE
- BLOCKED_NO_ADAPTER_DISCOVERY

### Must

- Read each provider `.adapter.md` before performing writes.
- Confirm schemas are JSON Schema draft 2020-12.
- Confirm schemas expose read-only fields such as `id`, `createdAt`,
  `updatedAt`, `url`, `_webhook`, or `_connection` wherever those fields are
  present in synced records.

### Must Not

- Hand-feed write shapes from prompts when mounted schemas exist.
- Treat packaged adapter discovery as sufficient if the live mounted workspace
  does not expose it.

## initial-e2e.linear-file-native-writeback

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, linear, writeback, create, patch, delete
Human Review: true

### Message
Using only mounted Linear discovery files, create a Linear issue with a
non-canonical filename, patch its mutable fields, reject a read-only mutation,
create a comment, delete the issue, and prove the writeback queue is clean.

### Procedure

1. Read Linear discovery:

   ```bash
   find "$EVAL_LOCAL_DIR" -path '*/linear*' \
     \( -name '.adapter.md' -o -name '.schema.json' -o -name '.create.example.json' \) \
     | sort | tee "$EVAL_EVIDENCE_DIR/linear-discovery.txt"
   ```

2. Create an issue draft. Use the mounted create example as the starting
   document and fill `teamId` from `EVAL_LINEAR_TEAM_ID`.

   ```bash
   LINEAR_DRAFT="$EVAL_LOCAL_DIR/linear/issues/$EVAL_RUN_ID-create-issue.json"
   jq -n --arg teamId "$EVAL_LINEAR_TEAM_ID" --arg run "$EVAL_RUN_ID" '{
     teamId: $teamId,
     title: ($run + " Relayfile E2E issue"),
     description: ("Created by " + $run),
     priority: 0
   }' > "$LINEAR_DRAFT"
   ```

3. Wait until the draft is rewritten as a receipt containing `created` and
   `path`, then read the canonical issue file.

4. Patch the canonical issue file by writing only mutable fields:

   ```bash
   jq -n --arg run "$EVAL_RUN_ID" '{description: ("Patched by " + $run)}' \
     > "$EVAL_LOCAL_DIR/<canonical-linear-issue-path>"
   ```

5. Attempt a read-only mutation against the canonical issue:

   ```bash
   jq -n '{id: "not-the-real-id"}' > "$EVAL_LOCAL_DIR/<canonical-linear-issue-path>"
   relayfile writeback status "$EVAL_WORKSPACE" --json \
     | tee "$EVAL_EVIDENCE_DIR/linear-readonly-status.json"
   ```

6. Create a comment under the canonical issue using a non-canonical filename.

7. Delete the canonical issue file created in step 2. Do not delete any
   pre-existing Linear issue.

8. Write `10-linear.json` with all paths, receipts, issue ids, URLs, and final
   status.

### Deterministic Checks

ok: true
contentIncludes:
- created
- path
- Relayfile E2E issue
- Patched by
- ReadOnlyFieldError
fileExists:
- $EVAL_EVIDENCE_DIR/10-linear.json
forbidPhrases:
- new.json
deadLettered: 0

### Must

- Use a non-canonical create filename, not `new.json`.
- Prove the canonical issue file appears after create.
- Prove read-only mutation is rejected and queryable.
- Delete only the issue created by this eval.

### Must Not

- Patch by rewriting the full record including read-only fields.
- Count the case as passed if the provider API succeeds but the mounted receipt
  or canonical file never appears.

## initial-e2e.slack-file-native-writeback

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, slack, writeback, create, patch, delete
Human Review: true

### Message
Using only mounted Slack discovery files, post a message with a non-canonical
filename, patch it, create a reply, add and remove a reaction, delete created
Slack messages, and prove the writeback queue is clean.

### Procedure

1. Read Slack discovery for messages, replies, and reactions.
2. Create a top-level message:

   ```bash
   SLACK_MESSAGE_DRAFT="$EVAL_LOCAL_DIR/slack/channels/$EVAL_SLACK_CHANNEL_ID/messages/$EVAL_RUN_ID post message.json"
   jq -n --arg run "$EVAL_RUN_ID" '{text: ($run + " Relayfile E2E Slack message")}' \
     > "$SLACK_MESSAGE_DRAFT"
   ```

3. Wait for the draft receipt, then read the canonical message file.
4. Patch the canonical message text.
5. Create a thread reply with a non-canonical filename under
   `messages/<messageTs>/replies/`.
6. Add a reaction with a non-canonical filename under
   `messages/<messageTs>/reactions/`. Use a draft filename that does not match
   the Slack ID pattern, for example `$EVAL_RUN_ID add reaction.json`, and body
   `{"name":"white_check_mark"}`.
7. Delete the reaction canonical file if exposed by the receipt, then delete the
   reply and top-level message canonical files created by this eval.
8. Write `20-slack.json` with paths, receipts, canonical timestamps, and final
   status.

### Deterministic Checks

ok: true
contentIncludes:
- Relayfile E2E Slack message
- created
- path
- white_check_mark
fileExists:
- $EVAL_EVIDENCE_DIR/20-slack.json
forbidPhrases:
- new.json
deadLettered: 0

### Must

- Use the configured test channel only.
- Use create-by-filename for messages, replies, and reactions.
- Delete only Slack records created by this eval.
- Confirm the visible Slack message changes in Slack UI or through the mounted
  canonical file after sync.

### Must Not

- Use `new.json`.
- Leave test messages behind unless delete is blocked; if blocked, record the
  provider error and message URL.

## initial-e2e.notion-file-native-writeback

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, notion, writeback, create, patch
Human Review: true

### Message
Using only mounted Notion discovery files, create a Notion database page with a
non-canonical filename, patch mutable page properties or markdown according to
the mounted schema, and prove the writeback queue is clean.

### Procedure

1. Read Notion discovery for `/notion/databases/{databaseId}/pages`.
2. Read the database metadata to determine the title property name and expected
   property value shape.
3. Create a page draft under
   `/notion/databases/$EVAL_NOTION_DATABASE_ID/pages/<non-canonical>.json`.
   Use the mounted `.create.example.json` when possible and set the page title
   to `$EVAL_RUN_ID Relayfile E2E Notion page`.
4. Wait for the draft receipt and canonical page file.
5. Patch a mutable field from the schema. If the page has a `content.md` file
   after sync, also append a markdown marker containing `$EVAL_RUN_ID`.
6. Attempt a read-only field mutation and verify it is rejected.
7. Write `30-notion.json` with paths, receipts, page ids, URLs, and final
   status.

### Deterministic Checks

ok: true
contentIncludes:
- Relayfile E2E Notion page
- created
- path
fileExists:
- $EVAL_EVIDENCE_DIR/30-notion.json
forbidPhrases:
- new.json
deadLettered: 0

### Must

- Derive the create payload from the mounted schema and database metadata.
- Record the exact schema path used.
- Confirm the page appears in the mounted canonical file and in Notion UI or
  provider readback.

### Must Not

- Hard-code a Notion property shape that conflicts with the target database.
- Delete a pre-existing Notion page.

## initial-e2e.github-review-writeback

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, github, writeback, review
Human Review: true

### Message
Using only mounted GitHub discovery files, submit a body-only pull request
review through a non-canonical filename and prove the writeback queue is clean.

### Procedure

1. Read GitHub discovery for
   `/github/repos/{owner}/{repo}/pulls/{pullNumber}/reviews`.
2. Read the target PR metadata and verify the PR is open.
3. Create a review draft:

   ```bash
   REVIEW_DIR="$EVAL_LOCAL_DIR/github/repos/$EVAL_GITHUB_OWNER/$EVAL_GITHUB_REPO/pulls/$EVAL_GITHUB_PULL_NUMBER/reviews"
   REVIEW_DRAFT="$REVIEW_DIR/$EVAL_RUN_ID body review.json"
   jq -n --arg run "$EVAL_RUN_ID" '{
     event: "COMMENT",
     body: ($run + " Relayfile E2E GitHub review"),
     comments: []
   }' > "$REVIEW_DRAFT"
   ```

4. Wait for receipt or canonical review record.
5. Verify the review appears on the GitHub PR.
6. Write `40-github.json` with paths, review id, URL, and final status.

### Deterministic Checks

ok: true
contentIncludes:
- Relayfile E2E GitHub review
- COMMENT
fileExists:
- $EVAL_EVIDENCE_DIR/40-github.json
forbidPhrases:
- new.json
deadLettered: 0

### Must

- Use `event: "COMMENT"` so the test does not approve or request changes on a
  real PR.
- Use an open disposable PR, or record why the chosen PR is safe.
- Use create-by-filename and mounted schema discovery.

### Must Not

- Submit `APPROVE` or `REQUEST_CHANGES`.
- Use GitHub MCP/API directly to create the review.

## initial-e2e.final-health-and-regression-sweep

Executor: relayfile
Kind: live-e2e
Tags: initial-integrations, status, regression
Human Review: true

### Message
Prove that all writeback operations have drained, no dead letters remain, and
the old `new.json` contract did not reappear.

### Procedure

```bash
relayfile writeback status "$EVAL_WORKSPACE" --json \
  | tee "$EVAL_EVIDENCE_DIR/90-final-status.json"

relayfile status "$EVAL_WORKSPACE" \
  | tee "$EVAL_EVIDENCE_DIR/final-relayfile-status.txt"

find "$EVAL_LOCAL_DIR" \
  \( -name '.adapter.md' -o -name '.schema.json' -o -name '.create.example.json' -o -name 'new.json' \) \
  | sort | tee "$EVAL_EVIDENCE_DIR/final-discovery.txt"
```

### Deterministic Checks

ok: true
contentIncludes:
- '"pending": 0'
- '"deadLettered": []'
- .adapter.md
- .schema.json
forbidPhrases:
- /new.json
- dead_lettered
- failed
- BLOCKED

### Must

- Include links or provider ids for every created object.
- Include every cleanup action taken.
- Include exact command output for failed or blocked steps.
- Mark the whole eval `PASS` only if all provider cases pass and final status
  has no dead-lettered operations.

### Must Not

- Hide provider-specific partial failures under an overall pass.
- Leave behind Slack messages, Linear issues, or other provider records without
  documenting why cleanup was not possible.
