# Making Agents Proactive

Most agents are reactive — they respond when called. A proactive agent knows when something changed in the outside world and acts without being triggered manually.

Getting there without relayfile requires significant infrastructure. Here is the concrete comparison.

---

## Without Relayfile

To make an agent proactive against a single provider (Linear), you need:

### 1. A public webhook endpoint

```typescript
import express from 'express';
import crypto from 'crypto';
import { timingSafeEqual } from 'crypto';

const app = express();

// Must consume raw body before JSON parsing for signature verification
app.post('/webhooks/linear', express.raw({ type: '*/*' }), async (req, res) => {

  // 2. Verify Linear's HMAC-SHA256 signature
  const signature = req.headers['x-linear-signature'] as string;
  const expected = crypto
    .createHmac('sha256', process.env.LINEAR_WEBHOOK_SECRET!)
    .update(req.body)
    .digest('hex');

  if (!timingSafeEqual(Buffer.from(signature), Buffer.from(expected))) {
    return res.status(401).send('Unauthorized');
  }

  // 3. Must respond within 2 seconds or Linear retries for 2 hours
  res.status(200).send('OK');

  // 4. Hand off to queue immediately — do not process inline
  await queue.add('linear-event', {
    body: req.body.toString(),
    receivedAt: Date.now(),
  });
});
```

### 2. Queue infrastructure and a worker

```typescript
// Requires Redis or SQS running somewhere
import Queue from 'bull';
const queue = new Queue('linear-event', process.env.REDIS_URL!);

queue.process(async (job) => {
  const payload = JSON.parse(job.data.body);

  // 5. Deduplicate — Linear sends duplicates under load
  const dedupKey = `seen:${payload.webhookId}`;
  const already = await redis.get(dedupKey);
  if (already) return;
  await redis.set(dedupKey, '1', 'EX', 86400);

  // 6. Filter to relevant event types
  if (payload.type !== 'Issue') return;

  // 7. Fetch current state — webhook payload is often partial
  const issue = await linearClient.issue(payload.data.id);

  // 8. Load agent context for this workspace
  const context = await db.query(
    'SELECT * FROM agent_contexts WHERE team_id = $1',
    [issue.team.id]
  );

  // 9. Trigger agent
  await agent.handle({ issue, context: context.rows[0] });
});
```

### 3. Register the webhook with Linear

```typescript
// Run once per environment — and again whenever your URL changes
await linearClient.createWebhook({
  url: 'https://your-domain.com/webhooks/linear',
  teamId: process.env.LINEAR_TEAM_ID!,
  resourceTypes: ['Issue', 'Comment', 'Project'],
});
```

### That is one provider.

Now add GitHub, Jira, and Slack. Each requires its own endpoint, its own signature format, its own retry behavior, its own payload schema, and its own registration step:

| Provider | Signature header | Algorithm | Retry window | Partial payload? |
|---|---|---|---|---|
| Linear | `X-Linear-Signature` | HMAC-SHA256 | 2 hours | Yes |
| GitHub | `X-Hub-Signature-256` | HMAC-SHA256 | 3 attempts | No |
| Jira | `X-Hub-Signature` | HMAC-SHA256 (admin) or none | No retry | Yes |
| Slack | `X-Slack-Signature` | HMAC-SHA256 + timestamp | 3 retries | No |
| Notion | None (IP allowlist only) | — | No retry | Yes |

Four endpoints. Four verification implementations. Four registration flows. Four payload parsers. Ongoing maintenance when providers change schemas.

**Estimated build time: 4–8 weeks for production-ready multi-provider coverage.**

You now own this infrastructure. You are responsible for queue health, dead-letter monitoring, webhook re-registration when your URL changes, and debugging silent failures when a provider changes its schema.

---

## With Relayfile

Relayfile handles endpoint registration, signature verification, deduplication, ordering, normalization, and state persistence. The agent sees none of it.

### Setup: connect your providers once

```typescript
// In your relayfile workspace config — done once, not per-agent
{
  "workspace": "acme/ops",
  "providers": ["linear", "github", "jira", "slack"]
}
```

### Agent code: three lines

```typescript
import { RelayFileSync } from '@relayfile/sdk';

const sync = new RelayFileSync({
  workspace: 'acme/ops',
  token: process.env.RELAYFILE_TOKEN,
});

sync.on('change', async (file) => {
  await agent.handle(file);
});
```

The agent receives a normalized file update — not a raw webhook payload. The affected file path, the previous state, and the current state are already resolved. The agent does not know which provider originated the event and does not need to.

### What a change event looks like

```typescript
// file received by the agent
{
  path: '/linear/issues/ENG-412.json',
  event: 'updated',
  previous: { status: 'in_review', assignee: 'alice' },
  current:  { status: 'blocked',   assignee: 'alice', blockedReason: 'waiting on design' },
  occurredAt: '2026-05-10T14:32:01Z',
  source: 'linear.issue.updated',
}
```

The agent reads what changed and decides what to do. Everything else is handled.

### Handling multiple providers

```typescript
sync.on('change', async (file) => {
  if (file.path.startsWith('/linear/issues/')) {
    await handleLinearIssue(file);
  } else if (file.path.startsWith('/github/pulls/')) {
    await handleGithubPR(file);
  } else if (file.path.startsWith('/slack/messages/')) {
    await handleSlackMessage(file);
  }
});
```

No new infrastructure per provider. No new verification logic. No new registration steps.

---

## Side-by-Side

| | Without relayfile | With relayfile |
|---|---|---|
| Public endpoint required | Yes — you host and maintain it | No |
| Signature verification | Per provider, per algorithm | None |
| Queue infrastructure | Yes — Redis or SQS | None |
| Deduplication logic | Yes — per provider | None |
| Payload parsing | Per provider, per event type | Normalized file diff |
| Webhook registration | Per provider, per environment | Workspace config |
| Re-registration on URL change | Manual | N/A |
| State between agent runs | You build it | Built in |
| New provider | 1–2 weeks per provider | Config change |
| Time to first proactive agent | 4–8 weeks | < 1 day |

---

## What "Proactive" Actually Enables

A reactive agent waits. A proactive agent acts when the moment is right.

- **Support agent** closes a ticket the moment the customer replies, not the next time it is scheduled to check.
- **Coding agent** pauses mid-task when it detects the ticket scope changed, rather than finishing work that is now invalid.
- **Sales agent** enriches a contact record the moment a new deal is created, while the rep is still on the intake call.
- **Incident agent** starts a runbook the moment PagerDuty fires, not 60 seconds later when the next polling cycle runs.

The difference between reactive and proactive is the difference between an assistant that needs to be asked and one that already handled it.
