# Slack Event Subscription Cases

These fixture-backed cases encode the Slack Events API subscriptions that the
Relayfile Slack app requests. They verify that channel, private channel, user,
team, and message events can be represented as mounted files without touching a
live Slack workspace.

## slack-events.channel-and-group-lifecycle
Executor: relayfile
Kind: regression
Tags: integrations, slack, webhooks, subscriptions
Human Review: false

### Message
Materialize public and private Slack channel lifecycle events into the relayfile mount.

### Operations
```json
[
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CARCH__archived/meta.json", "content": { "eventType": "channel_archive", "id": "CARCH", "is_archived": true } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CCREATE__created/meta.json", "content": { "eventType": "channel_created", "id": "CCREATE", "name": "created" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CDELETE/meta.json", "content": { "eventType": "channel_deleted", "id": "CDELETE", "deleted": true } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CRENAME__renamed/meta.json", "content": { "eventType": "channel_rename", "id": "CRENAME", "name": "renamed" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CUNARCH__unarchived/meta.json", "content": { "eventType": "channel_unarchive", "id": "CUNARCH", "is_archived": false } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/GARCH__private-archived/meta.json", "content": { "eventType": "group_archive", "id": "GARCH", "is_private": true, "is_archived": true } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/GDELETE/meta.json", "content": { "eventType": "group_deleted", "id": "GDELETE", "is_private": true, "deleted": true } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/GRENAME__private-renamed/meta.json", "content": { "eventType": "group_rename", "id": "GRENAME", "is_private": true, "name": "private-renamed" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/GUNARCH__private-unarchived/meta.json", "content": { "eventType": "group_unarchive", "id": "GUNARCH", "is_private": true, "is_archived": false } },
  { "op": "list", "path": "/slack/channels" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- channel_archive
- channel_created
- channel_deleted
- channel_rename
- channel_unarchive
- group_archive
- group_deleted
- group_rename
- group_unarchive
- GDELETE
fileExists:
- /slack/channels/CARCH__archived/meta.json
- /slack/channels/GRENAME__private-renamed/meta.json
maxToolCalls: 10

### Must
- Preserve public channel and private channel lifecycle events as channel-shaped mount records.
- Treat deleted public and private channels as deletion events.

### Must Not
- Drop private channel events because their Slack event names start with `group_`.

## slack-events.members-users-and-messages
Executor: relayfile
Kind: regression
Tags: integrations, slack, webhooks, subscriptions
Human Review: false

### Message
Materialize Slack membership, message, team, and user events into the relayfile mount.

### Operations
```json
[
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CJOIN__gtm-prospects/meta.json", "content": { "eventType": "member_joined_channel", "id": "CJOIN", "user": "UJOIN" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CLEFT__gtm-prospects/meta.json", "content": { "eventType": "member_left_channel", "id": "CLEFT", "user": "ULEFT" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/CMSG__gtm-prospects/messages/1711111111_000100/meta.json", "content": { "eventType": "message.channels", "channel": "CMSG", "ts": "1711111111.000100", "text": "public message" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/GMSG__private/messages/1711111112_000100/meta.json", "content": { "eventType": "message.groups", "channel": "GMSG", "ts": "1711111112.000100", "text": "private message" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/DMSG/messages/1711111113_000100/meta.json", "content": { "eventType": "message.im", "channel": "DMSG", "ts": "1711111113.000100", "text": "direct message" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/channels/MPMSG/messages/1711111114_000100/meta.json", "content": { "eventType": "message.mpim", "channel": "MPMSG", "ts": "1711111114.000100", "text": "multiparty dm message" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/users/UJOINED__joined-user.json", "content": { "eventType": "team_join", "id": "UJOINED", "name": "joined-user" } },
  { "op": "materializeWebhook", "provider": "slack", "path": "/slack/users/UCHANGED__changed-user.json", "content": { "eventType": "user_change", "id": "UCHANGED", "name": "changed-user" } },
  { "op": "grep", "path": "/slack", "pattern": "eventType" }
]
```

### Deterministic Checks
ok: true
contentIncludes:
- member_joined_channel
- member_left_channel
- message.channels
- message.groups
- message.im
- message.mpim
- team_join
- user_change
fileExists:
- /slack/channels/CMSG__gtm-prospects/messages/1711111111_000100/meta.json
- /slack/users/UCHANGED__changed-user.json
maxToolCalls: 9

### Must
- Represent public, private, DM, and MPDM message subscriptions under stable Slack mount paths.
- Represent team and user events under `/slack/users`.

### Must Not
- Conflate membership events with messages.
