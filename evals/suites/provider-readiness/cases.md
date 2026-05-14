# Provider Readiness Cases

These fixture-backed evals mirror the Notion, Jira, Linear, Slack, GitHub, and
Confluence validation checklist used for the `rw_517d60b6` alias-tree rollout.
They intentionally model the observable relayfile mount contract rather than
calling provider APIs directly.

## provider-readiness.sync-materialization-six-providers
Executor: relayfile
Kind: regression
Tags: provider-readiness, reads, six-provider, alias-tree
Human Review: false

### Message
Verify that all in-scope provider read trees expose the expected indexes,
aliases, and documented empty subtrees after provider syncs complete.

### Mock
```json
{
  "files": {
    "/notion/_index.json": [{"provider":"notion","models":["NotionPage","NotionDatabase","NotionUser"]}],
    "/notion/databases/_index.json": [{"id":"2f96800c-1c90-80d7-82b0-c4443e84dae2","title":"Meeting Notes"}],
    "/notion/pages/by-database/meeting-notes__e84dae2/relayfile-eval-page__c24642bb.json": {"id":"3566800c-1c90-8035-a491-cdc3c24642bb","title":"Relayfile Eval Page","database_id":"2f96800c-1c90-80d7-82b0-c4443e84dae2"},
    "/jira/_index.json": [{"provider":"jira","syncs":["fetch-issues","fetch-projects","fetch-sprints"]}],
    "/jira/issues/_index.json": [{"id":"10000","key":"AGE-16","summary":"Relayfile validation issue","assigneeAccountId":"acct-1"}],
    "/jira/issues/by-assignee/acct-1/10000.json": {"id":"10000","key":"AGE-16"},
    "/jira/projects/by-id/10001.json": {"id":"10001","key":"AGE"},
    "/jira/sprints/.empty": "no sprints in upstream workspace for this eval fixture\n",
    "/linear/_index.json": [{"provider":"linear","syncs":["fetch-active-issues","fetch-comments","fetch-users","fetch-teams"]}],
    "/linear/issues/AGE-16.json": {"id":"issue-16","identifier":"AGE-16","title":"Relayfile specific tests"},
    "/linear/comments/2cb57783-fc28-46f0-80ac-634e300a4705.json": {"id":"2cb57783-fc28-46f0-80ac-634e300a4705","issueId":"issue-16","body":"Webhook comment"},
    "/linear/cycles/.empty": "no upstream cycles for this eval fixture\n",
    "/slack/_index.json": [{"provider":"slack"}],
    "/slack/channels/by-name/gtm-prospects.json": {"id":"C0ADE9B71CN","name":"gtm-prospects"},
    "/slack/channels/C0ADE9B71CN__gtm-prospects/meta.json": {"id":"C0ADE9B71CN","name":"gtm-prospects"},
    "/github/_index.json": [{"provider":"github","syncs":["fetch-repos","fetch-open-issues","fetch-open-prs"]}],
    "/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json": {"number":91,"state":"closed","title":"Relayfile writeback smoke"},
    "/github/repos/AgentWorkforce/relayfile-adapters/pulls/88/meta.json": {"number":88,"state":"open","title":"Relayfile adapter PR"},
    "/confluence/_index.json": [{"provider":"confluence","syncs":["fetch-pages","fetch-spaces"]}],
    "/confluence/spaces/_index.json": [{"id":"688132","key":"ENG","name":"Engineering"}],
    "/confluence/pages/_index.json": [{"id":"123456","title":"Relayfile validation page","spaceId":"688132"}]
  }
}
```

### Operations
```json
[
  {"op":"list","path":"/notion/pages/by-database/meeting-notes__e84dae2"},
  {"op":"read","path":"/notion/databases/_index.json"},
  {"op":"read","path":"/jira/issues/by-assignee/acct-1/10000.json"},
  {"op":"read","path":"/jira/sprints/.empty"},
  {"op":"read","path":"/linear/issues/AGE-16.json"},
  {"op":"read","path":"/linear/cycles/.empty"},
  {"op":"read","path":"/slack/channels/by-name/gtm-prospects.json"},
  {"op":"read","path":"/github/_index.json"},
  {"op":"read","path":"/confluence/_index.json"}
]
```

### Deterministic Checks
ok: true
contentIncludes:
- relayfile-eval-page__c24642bb.json
- Meeting Notes
- AGE-16
- no sprints
- no upstream cycles
- gtm-prospects
- fetch-repos
- confluence
fileExists:
- /notion/pages/by-database/meeting-notes__e84dae2/relayfile-eval-page__c24642bb.json
- /jira/issues/_index.json
- /jira/issues/by-assignee/acct-1/10000.json
- /github/_index.json
- /confluence/_index.json
maxToolCalls: 9

### Must
- Treat Linear cycles and Jira sprints as allowed-empty subtrees when upstream has no data.
- Require Jira `by-assignee` materialization when an assigned issue is present.
- Require Notion database-scoped page aliases, not only top-level page files.

### Must Not
- Interpret provider data from a different workspace as evidence.
- Count a provider as read-ready when its root index is missing.

## provider-readiness.writeback-paths-six-providers
Executor: relayfile
Kind: regression
Tags: provider-readiness, writeback, six-provider
Human Review: false

### Message
Exercise one file-native writeback path for each in-scope provider and verify
that each queues a provider writeback with no dead letters.

### Mock
```json
{
  "files": {
    "/jira/issues/_index.json": [],
    "/linear/issues/AGE-16.json": {"id":"issue-16","identifier":"AGE-16","title":"Relayfile specific tests"},
    "/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb.json": {"id":"3566800c-1c90-8035-a491-cdc3c24642bb","title":"Just Give the Agent Files"},
    "/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json": {"number":91,"state":"open","title":"Relayfile writeback smoke"},
    "/slack/channels/C0ADE9B71CN__gtm-prospects/meta.json": {"id":"C0ADE9B71CN","name":"gtm-prospects"},
    "/confluence/spaces/688132/pages/relayfile-validation__123456/content.md": "# Relayfile validation page\n"
  }
}
```

### Operations
```json
[
  {
    "op":"write",
    "path":"/jira/issues/relayfile-eval-issue.json",
    "content":{"fields":{"project":{"key":"AGE"},"summary":"Relayfile eval Jira issue","issuetype":{"name":"Task"}}},
    "writeback":{"adapter":"jira","mode":"create"}
  },
  {
    "op":"write",
    "path":"/linear/issues/AGE-16/comments/relayfile-eval-comment.json",
    "content":{"body":"Relayfile eval Linear comment"},
    "writeback":{"adapter":"linear","mode":"create"}
  },
  {
    "op":"write",
    "path":"/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb/properties.json",
    "content":{"properties":{"Name":{"type":"title","value":"Relayfile eval Notion properties writeback"}}},
    "writeback":{"adapter":"notion","mode":"patch"}
  },
  {
    "op":"write",
    "path":"/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json",
    "content":{"state":"closed"},
    "writeback":{"adapter":"github","mode":"patch"}
  },
  {
    "op":"write",
    "path":"/slack/channels/C0ADE9B71CN__gtm-prospects/messages/relayfile-eval-message.json",
    "content":{"text":"Relayfile eval Slack message"},
    "writeback":{"adapter":"slack","mode":"create"}
  },
  {
    "op":"write",
    "path":"/confluence/spaces/688132/pages/relayfile-validation__123456/content.md",
    "content":"# Relayfile validation page\n\nUpdated by Relayfile eval.\n",
    "writeback":{"adapter":"confluence","mode":"patch"}
  }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- Relayfile eval Jira issue
- Relayfile eval Linear comment
- Relayfile eval Notion properties writeback
- "state": "closed"
- Relayfile eval Slack message
- Updated by Relayfile eval
writebackQueued:
- {"adapter":"jira","path":"/jira/issues/relayfile-eval-issue.json"}
- {"adapter":"linear","path":"/linear/issues/AGE-16/comments/relayfile-eval-comment.json"}
- {"adapter":"notion","path":"/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb/properties.json"}
- {"adapter":"github","path":"/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json"}
- {"adapter":"slack","path":"/slack/channels/C0ADE9B71CN__gtm-prospects/messages/relayfile-eval-message.json"}
- {"adapter":"confluence","path":"/confluence/spaces/688132/pages/relayfile-validation__123456/content.md"}
deadLettered: 0
forbidPhrases:
- new.json

### Must
- Use the current Notion `properties.json` compatibility path.
- Use Slack `#gtm-prospects` by channel id or slugged canonical channel path.
- Use GitHub issue PATCH semantics to close a created issue.

### Must Not
- Rely on magic `new.json` create templates.
- Mark writeback ready when the operation is not queued for the provider.

## provider-readiness.webhook-materialization-supported-providers
Executor: relayfile
Kind: regression
Tags: provider-readiness, webhooks, materialization
Human Review: false

### Message
Model provider webhook deliveries for the providers exercised in the rollout
and verify that canonical files or aliases materialize with webhook evidence.

### Mock
```json
{
  "files": {
    "/linear/issues/AGE-16.json": {"id":"issue-16","identifier":"AGE-16","title":"Relayfile specific tests"},
    "/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb/content.md": "# Just Give the Agent Files\n",
    "/slack/channels/C0ADE9B71CN__gtm-prospects/meta.json": {"id":"C0ADE9B71CN","name":"gtm-prospects"},
    "/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json": {"number":91,"state":"open"}
  }
}
```

### Operations
```json
[
  {
    "op":"materializeWebhook",
    "provider":"linear",
    "path":"/linear/comments/2cb57783-fc28-46f0-80ac-634e300a4705.json",
    "receivedAt":"2026-05-13T12:40:11.172Z",
    "content":{"id":"2cb57783-fc28-46f0-80ac-634e300a4705","issueId":"issue-16","body":"Webhook comment on AGE-16","_webhook":{"receivedAt":"2026-05-13T12:40:11.172Z"}}
  },
  {
    "op":"materializeWebhook",
    "provider":"notion",
    "path":"/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb/content.md",
    "receivedAt":"2026-05-13T12:48:59.296Z",
    "content":"# Just Give the Agent Files\n\nNotion webhook test 2026-05-13\n"
  },
  {
    "op":"materializeWebhook",
    "provider":"slack",
    "path":"/slack/channels/C0ADE9B71CN/messages/1778680512_229979/meta.json",
    "receivedAt":"2026-05-13T13:55:13.658Z",
    "content":{"channel":"C0ADE9B71CN","text":"Relayfile Slack writeback smoke","_webhook":{"receivedAt":"2026-05-13T13:55:13.658Z"}}
  },
  {
    "op":"materializeWebhook",
    "provider":"github",
    "path":"/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json",
    "receivedAt":"2026-05-13T12:16:28.000Z",
    "content":{"number":91,"state":"closed","title":"Relayfile writeback smoke","_webhook":{"receivedAt":"2026-05-13T12:16:28.000Z"}}
  }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- Webhook comment on AGE-16
- Notion webhook test 2026-05-13
- Relayfile Slack writeback smoke
- "state": "closed"
fileContentIncludes:
- {"path":"/linear/comments/2cb57783-fc28-46f0-80ac-634e300a4705.json","value":"_webhook"}
- {"path":"/slack/channels/C0ADE9B71CN/messages/1778680512_229979/meta.json","value":"2026-05-13T13:55:13.658Z"}
- {"path":"/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json","value":"closed"}
webhookMaterialized:
- {"provider":"linear","path":"/linear/comments/2cb57783-fc28-46f0-80ac-634e300a4705.json"}
- {"provider":"notion","path":"/notion/pages/3566800c-1c90-8035-a491-cdc3c24642bb/content.md"}
- {"provider":"slack","path":"/slack/channels/C0ADE9B71CN/messages/1778680512_229979/meta.json"}
- {"provider":"github","path":"/github/repos/AgentWorkforce/relayfile-adapters/issues/91.json"}

### Must
- Treat webhook success as a filesystem materialization event, not just a provider delivery.
- Preserve enough provider evidence to correlate with CloudWatch or provider timestamps.

### Must Not
- Count a webhook as passing if no canonical file or alias changed.

## provider-readiness.known-webhook-gaps-explicit
Executor: relayfile
Kind: regression
Tags: provider-readiness, webhooks, gaps, jira, confluence
Human Review: false

### Message
Keep known webhook gaps explicit so Jira and Confluence cannot silently appear
green before webhook registration and router coverage are implemented.

### Mock
```json
{
  "files": {
    "/provider-readiness/webhook-status.json": {
      "jira": {"status":"blocked","trackingIssue":"AgentWorkforce/cloud#584","reason":"webhook registration not set up"},
      "confluence": {"status":"blocked","trackingIssue":"AgentWorkforce/cloud#584","reason":"webhook registration not set up"},
      "github": {"status":"needs-retest","reason":"requires deploy containing cloud#586"},
      "linear": {"status":"pass"},
      "notion": {"status":"pass"},
      "slack": {"status":"pass"}
    }
  }
}
```

### Operations
```json
[
  {"op":"read","path":"/provider-readiness/webhook-status.json"}
]
```

### Deterministic Checks
ok: true
contentIncludes:
- AgentWorkforce/cloud#584
- webhook registration not set up
fileContentIncludes:
- {"path":"/provider-readiness/webhook-status.json","value":"needs-retest"}
- {"path":"/provider-readiness/webhook-status.json","value":"\"linear\""}
- {"path":"/provider-readiness/webhook-status.json","value":"\"notion\""}
- {"path":"/provider-readiness/webhook-status.json","value":"\"slack\""}
fileExists:
- /provider-readiness/webhook-status.json

### Must
- Keep Jira and Confluence webhook setup visible as blocked until implemented.
- Keep GitHub webhook retest visible until a deployed fix is observed end to end.

### Must Not
- Present all six providers as webhook-green without concrete filesystem evidence.
