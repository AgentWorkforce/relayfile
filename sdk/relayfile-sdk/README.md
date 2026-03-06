# @agent-relay/relayfile-sdk

TypeScript SDK for the RelayFile API contract in `openapi/relayfile-v1.openapi.yaml`.

## Install

```bash
npm install @agent-relay/relayfile-sdk
```

## Usage

```ts
import { InvalidStateError, QueueFullError, RelayFileClient, RevisionConflictError } from "@agent-relay/relayfile-sdk";

const client = new RelayFileClient({
  baseUrl: "https://relayfile.agent-relay.com",
  token: () => process.env.RELAYFILE_TOKEN ?? "",
  retry: {
    maxRetries: 3,
    baseDelayMs: 100,
    maxDelayMs: 2000,
    jitterRatio: 0.2
  }
});

const workspaceId = "ws_123";

const controller = new AbortController();
const tree = await client.listTree(workspaceId, {
  path: "/",
  depth: 2,
  signal: controller.signal
});
const file = await client.readFile(workspaceId, "/documents/page.md");
const query = await client.queryFiles(workspaceId, {
  path: "/documents",
  provider: "salesforce",  // Any provider
  relation: "account:123",
  properties: { status: "active" },
  limit: 25
});
const events = await client.getEvents(workspaceId, { provider: "salesforce", limit: 50 });
const ops = await client.listOps(workspaceId, { status: "dead_lettered", action: "file_upsert", provider: "salesforce", limit: 20 });
const sync = await client.getSyncStatus(workspaceId, { provider: "salesforce" });
const ingress = await client.getSyncIngressStatus(workspaceId, { provider: "salesforce" });
const adminIngress = await client.getAdminIngressStatus({ provider: "salesforce", alertProfile: "balanced", deadLetterThreshold: 2, nonZeroOnly: true, maxAlerts: 50, includeWorkspaces: true, includeAlerts: true });
const adminSync = await client.getAdminSyncStatus({ provider: "salesforce", nonZeroOnly: true, includeWorkspaces: true, limit: 100, lagSecondsThreshold: 45, maxAlerts: 50, includeAlerts: true });
const deadLetters = await client.getSyncDeadLetters(workspaceId, { provider: "salesforce", limit: 20 });
console.log(events.events.length);
console.log(query.items.length);
console.log(ops.items.length);
console.log(sync.providers[0]?.status, sync.providers[0]?.failureCodes, sync.providers[0]?.deadLetteredEnvelopes, sync.providers[0]?.deadLetteredOps);
console.log(ingress.queueDepth, ingress.droppedTotal);
console.log(ingress.queueUtilization, ingress.oldestPendingAgeSeconds);
console.log(ingress.coalescedTotal, ingress.suppressedTotal, ingress.staleTotal);
console.log(ingress.dedupeRate, ingress.coalesceRate);
console.log(ingress.deadLetterByProvider);
console.log(ingress.ingressByProvider["salesforce"]?.pendingTotal, ingress.ingressByProvider["salesforce"]?.oldestPendingAgeSeconds);
console.log(adminIngress.alertProfile, adminIngress.effectiveAlertProfile, adminIngress.workspaceCount, adminIngress.returnedWorkspaceCount, adminIngress.nextCursor, adminIngress.pendingTotal, adminIngress.thresholds.deadLetter, adminIngress.alertTotals.critical, adminIngress.alertsTruncated, adminIngress.alerts.length, Object.keys(adminIngress.workspaces));
console.log(adminSync.workspaceCount, adminSync.returnedWorkspaceCount, adminSync.nextCursor, adminSync.providerStatusCount, adminSync.errorCount, adminSync.failureCodes, adminSync.thresholds.lagSeconds, adminSync.alertTotals.critical, adminSync.alertsTruncated, adminSync.alerts.length);
console.log(deadLetters.items.length);
if (deadLetters.items.length > 0) {
  const detail = await client.getSyncDeadLetter(workspaceId, deadLetters.items[0].envelopeId);
  console.log(detail.lastError);
  await client.replaySyncDeadLetter(workspaceId, deadLetters.items[0].envelopeId);
  await client.ackSyncDeadLetter(workspaceId, deadLetters.items[0].envelopeId);
}
if (ops.items.length > 0) {
  await client.replayOp(workspaceId, ops.items[0].opId);
  await client.replayAdminOp(ops.items[0].opId);
}
await client.replayAdminEnvelope("env_123");

try {
  const write = await client.writeFile({
    workspaceId,
    path: file.path,
    baseRevision: file.revision,
    content: file.content + "\n\nUpdated by agent.",
    contentType: "text/markdown",
    semantics: {
      properties: { stage: "active" },
      relations: ["db:investments"],
      permissions: ["scope:fs:read"],
      comments: ["comment_123"]
    }
  });
  console.log(write.opId);
} catch (err) {
  if (err instanceof RevisionConflictError) {
    console.error("conflict", err.currentRevision);
  }
  if (err instanceof QueueFullError) {
    console.error("ingress saturated, retry in", err.retryAfterSeconds ?? 1, "seconds");
  }
  if (err instanceof InvalidStateError) {
    console.error("cannot replay in current state");
  }
  throw err;
}
```

## Notes

- All requests send `X-Correlation-Id` automatically if not provided.
- Requests retry transient `429/5xx` and network errors with jittered exponential backoff.
- Most option/input shapes accept `signal?: AbortSignal` for request cancellation.
- `writeFile` and `deleteFile` require optimistic concurrency via revision preconditions.
- `RevisionConflictError` is thrown for HTTP `409` conflict responses.
- `QueueFullError` is thrown for HTTP `429` with `code=queue_full` and surfaces `retryAfterSeconds`.
- `InvalidStateError` is thrown for HTTP `409` with `code=invalid_state` (for replay preconditions).
- `replayAdminEnvelope` and `replayAdminOp` call admin replay endpoints and require a token with `admin:replay`.
- `getBackendStatus`, `getAdminIngressStatus`, and `getAdminSyncStatus` are admin APIs and require `admin:read` (or `admin:replay`).
