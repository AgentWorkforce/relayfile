import { readFile, readdir, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { Harness, RequestTransportError, asRecord, assert, assertStatus, type ApiResponse } from "./harness.js";

const execFileAsync = promisify(execFile);

interface FileBody {
  content?: string;
  revision?: string;
  targetRevision?: string;
  opId?: string;
}

interface EventItem {
  eventId?: string;
  type?: string;
  path?: string;
  revision?: string;
}

interface EventsBody {
  events?: EventItem[];
  nextCursor?: string | null;
}

interface InspectedFile {
  exists: boolean;
  content?: string;
  revision?: string;
}

interface StateInspection {
  files?: Record<string, InspectedFile>;
  eventCounts?: Record<string, number>;
  deadLetters?: Array<{ deliveryId: string; envelopeId: string }>;
  backpressureActive?: boolean;
  identityActive?: boolean;
  servingRuntime?: string;
  operations?: Array<{ opId: string; writebackAttempts: number; state: string }>;
}

export const CONFORMANCE_CASE_IDS = [
  "RF-AUTH-001", "RF-AUTH-002", "RF-AUTH-003", "RF-AUTH-004", "RF-AUTH-005", "RF-MODE-001",
  "RF-ING-001", "RF-ING-002", "RF-ING-003", "RF-CAS-001", "RF-CAS-002", "RF-WS-001",
  "RF-WS-002", "RF-WS-003", "RF-DUR-001", "RF-OAS-001", "RF-OAS-002", "RF-QUEUE-001",
  "RF-QUEUE-002", "RF-QUEUE-003", "RF-PROJ-001", "RF-PROJ-002", "RF-PROJ-003", "RF-TIMER-001",
  "RF-TIMER-002", "RF-LIMIT-001", "RF-SER-001", "RF-CRASH-001", "RF-MIG-001", "RF-FAILOVER-001",
  "RF-MOUNT-001",
] as const;

export const ADVANCED_CASE_IDS = [
  "RF-AUTH-005",
  "RF-WS-002",
  "RF-QUEUE-001",
  "RF-QUEUE-002",
  "RF-QUEUE-003",
  "RF-PROJ-001",
  "RF-PROJ-002",
  "RF-PROJ-003",
  "RF-TIMER-001",
  "RF-TIMER-002",
  "RF-LIMIT-001",
  "RF-SER-001",
  "RF-CRASH-001",
  "RF-MIG-001",
  "RF-FAILOVER-001",
  "RF-MOUNT-001",
] as const;

export type AdvancedCaseId = (typeof ADVANCED_CASE_IDS)[number];

export async function runScenarios(h: Harness): Promise<void> {
  await authAndTenantScenarios(h);
  await ingestionScenarios(h);
  await conditionalWriteScenarios(h);
  await reconnectAndRestartScenarios(h);
  await contractScenarios(h);
  await advancedAdapterScenarios(h);
}

async function authAndTenantScenarios(h: Harness): Promise<void> {
  await h.case("RF-AUTH-001", "missing bearer token is rejected", ["public-api", "tenant-auth"], async () => {
    const path = h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(h.path("auth/missing.md"))}`);
    const response = await h.request("GET", path, { token: false });
    assertStatus(response, 401);
    assertErrorEnvelope(response);
  });

  await h.case("RF-AUTH-002", "workspace token cannot probe another tenant", ["public-api", "tenant-auth"], async () => {
    const existingPath = h.path("auth/secondary-secret.md");
    const existing = await h.request(
      "PUT",
      h.workspacePath(h.target.workspaces.secondary, `/fs/file?path=${encodeURIComponent(existingPath)}`),
      {
        token: "secondary",
        headers: { "If-Match": "*" },
        body: { contentType: "text/plain", content: "secondary-only" },
      },
    );
    assertStatus(existing, [200, 202]);

    const startedExisting = performance.now();
    const deniedExisting = await h.request(
      "GET",
      h.workspacePath(h.target.workspaces.secondary, `/fs/file?path=${encodeURIComponent(existingPath)}`),
    );
    const existingMs = performance.now() - startedExisting;
    const startedMissing = performance.now();
    const deniedMissing = await h.request(
      "GET",
      h.workspacePath(
        h.target.workspaces.secondary,
        `/fs/file?path=${encodeURIComponent(h.path("auth/secondary-absent.md"))}`,
      ),
    );
    const missingMs = performance.now() - startedMissing;

    assert([403, 404].includes(deniedExisting.status), `existing cross-tenant path leaked with ${deniedExisting.status}`);
    assert(deniedMissing.status === deniedExisting.status, "existing and absent cross-tenant paths returned different statuses");
    const existingCode = asRecord(deniedExisting.data).code;
    const missingCode = asRecord(deniedMissing.data).code;
    assert(existingCode === missingCode, "existing and absent cross-tenant paths returned different error codes");
    const slow = Math.max(existingMs, missingMs);
    const fast = Math.max(1, Math.min(existingMs, missingMs));
    assert(slow < 1_000 && slow / fast < 20, `cross-tenant latency classes diverged (${existingMs}ms vs ${missingMs}ms)`);
  });

  await h.case("RF-AUTH-003", "path-scoped token is bounded to its declared subtree", ["public-api", "tenant-auth"], async () => {
    const inside = `/conformance/scoped/${h.seed}/inside.md`;
    const outside = `/conformance/unscoped/${h.seed}/outside.md`;
    const allowed = await h.request(
      "PUT",
      h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(inside)}`),
      {
        token: "pathScoped",
        headers: { "If-Match": "*" },
        body: { content: "inside" },
      },
    );
    assertStatus(allowed, [200, 202]);
    const denied = await h.request(
      "PUT",
      h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(outside)}`),
      {
        token: "pathScoped",
        headers: { "If-Match": "*" },
        body: { content: "outside" },
      },
    );
    assertStatus(denied, 403);
  });

  await h.case("RF-AUTH-004", "encoded traversal cannot select a different workspace", ["public-api", "tenant-auth"], async () => {
    const traversal = `/v1/workspaces/${encodeURIComponent(h.target.workspaces.primary)}%2F..%2F${encodeURIComponent(
      h.target.workspaces.secondary,
    )}/fs/file?path=${encodeURIComponent(h.path("auth/secondary-secret.md"))}`;
    const response = await h.request("GET", traversal);
    assert(![200, 201, 202].includes(response.status), `encoded traversal unexpectedly succeeded with ${response.status}`);
  });

  await h.case("RF-MODE-001", "read-only identity cannot write", ["public-api", "tenant-auth"], async () => {
    const response = await h.request(
      "PUT",
      h.workspacePath(
        h.target.workspaces.primary,
        `/fs/file?path=${encodeURIComponent(h.path("modes/read-only.md"))}`,
      ),
      { token: "readOnly", headers: { "If-Match": "*" }, body: { content: "must be denied" } },
    );
    assertStatus(response, 403);
    assertErrorEnvelope(response);
  });
}

async function ingestionScenarios(h: Harness): Promise<void> {
  await h.case("RF-ING-001", "duplicate delivery ID projects exactly once", ["public-api", "webhook-ingest"], async () => {
    const path = h.path("ingest/idempotent.json");
    const body = await webhookFixture(
      path,
      `${h.seed}-delivery-duplicate`,
      "newest",
      "duplicate-object",
    );
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const first = await h.request("POST", endpoint, { body });
    const second = await h.request("POST", endpoint, { body });
    assertStatus(first, 202);
    assertStatus(second, 202);
    assert(asRecord(first.data).id === asRecord(second.data).id, "duplicate delivery returned a different envelope ID");
    await waitForFile(h, path, "newest");
    const events = await readEvents(h);
    const mutations = events.filter((event) => event.path === path && /^file\.(created|updated)$/u.test(event.type ?? ""));
    assert(mutations.length === 1, `duplicate delivery produced ${mutations.length} file mutations`);
  });

  await h.case("RF-ING-002", "stale out-of-order delivery cannot overwrite newer state", ["public-api", "webhook-ingest"], async () => {
    const path = h.path("ingest/ordered.json");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const newer = webhookBody(path, `${h.seed}-delivery-new`, "newer", "2026-01-03T03:04:05.000Z", "ordered-object");
    const older = webhookBody(path, `${h.seed}-delivery-old`, "older", "2026-01-01T03:04:05.000Z", "ordered-object");
    assertStatus(await h.request("POST", endpoint, { body: newer }), 202);
    await waitForFile(h, path, "newer");
    assertStatus(await h.request("POST", endpoint, { body: older }), 202);
    await h.poll(
      "stale ingest event",
      () => readEvents(h),
      (events) => events.some((event) => event.path === path && event.type === "sync.stale"),
    );
    const current = await readFileResponse(h, path);
    assert(current.content === "newer", `stale delivery overwrote content with ${current.content}`);
  });

  await h.case("RF-ING-003", "concurrent independent deliveries all project", ["public-api", "webhook-ingest"], async () => {
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const count = 12;
    const responses = await Promise.all(
      Array.from({ length: count }, (_, index) => {
        const path = h.path(`ingest/concurrent-${String(index).padStart(2, "0")}.json`);
        return h.request("POST", endpoint, {
          body: webhookBody(
            path,
            `${h.seed}-delivery-${index}`,
            `value-${index}`,
            `2026-01-02T03:04:${String(index).padStart(2, "0")}.000Z`,
            `concurrent-${index}`,
          ),
        });
      }),
    );
    assert(responses.every((response) => response.status === 202), "one or more concurrent ingests failed");
    await h.poll(
      "all concurrent projections",
      () => readEvents(h),
      (events) =>
        new Set(
          events
            .filter((event) => event.path?.startsWith(h.path("ingest/concurrent-")) && /^file\./u.test(event.type ?? ""))
            .map((event) => event.path),
        ).size === count,
      10_000,
    );
  });
}

async function conditionalWriteScenarios(h: Harness): Promise<void> {
  await h.case("RF-CAS-001", "twenty conditional writers produce one winner", ["public-api"], async () => {
    const path = h.path("concurrency/shared.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`);
    assertStatus(
      await h.request("PUT", endpoint, { headers: { "If-Match": "*" }, body: { content: "base" } }),
      [200, 202],
    );
    const base = await readFileResponse(h, path);
    assert(base.revision, "base revision missing");

    const attempts = await Promise.all(
      Array.from({ length: 20 }, (_, index) =>
        h.request("PUT", endpoint, {
          headers: { "If-Match": base.revision! },
          body: { content: `writer-${String(index).padStart(2, "0")}` },
        }),
      ),
    );
    const winners = attempts.filter((response) => response.status === 200 || response.status === 202);
    const conflicts = attempts.filter((response) => response.status === 409);
    assert(winners.length === 1, `expected 1 winner, got ${winners.length}`);
    assert(conflicts.length === 19, `expected 19 conflicts, got ${conflicts.length}`);
    const current = await readFileResponse(h, path);
    assert(current.revision, "current revision missing");
    for (const conflict of conflicts) {
      assert(asRecord(conflict.data).currentRevision === current.revision, "conflict did not carry the winning revision");
    }
    const before = revisionNumber(base.revision);
    const after = revisionNumber(current.revision);
    if (before !== undefined && after !== undefined) {
      assert(after === before + 1, `winning mutation advanced revision by ${after - before}, expected 1`);
    }
  });

  await h.case("RF-CAS-002", "bulk content identity is replay-safe", ["public-api"], async () => {
    const path = h.path("concurrency/idempotent-bulk.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/fs/bulk");
    const body = {
      files: [
        {
          path,
          contentType: "text/plain",
          content: "one durable mutation",
          ifMatch: "*",
          contentIdentity: { kind: "conformance", key: `${h.seed}-bulk-command`, ttlSeconds: 3600 },
        },
      ],
    };
    const accepted = await h.request("POST", endpoint, { body });
    assertStatus(accepted, [200, 202]);
    const first = await readFileResponse(h, path);
    const replayed = await h.request("POST", endpoint, { body });
    assertStatus(replayed, [200, 202]);
    const replay = await readFileResponse(h, path);
    assert(replay.revision === first.revision, "idempotent replay created a new revision");
    const acceptedReceipt = asRecord((asRecord(accepted.data).results as unknown[])[0]);
    const replayedReceipt = asRecord((asRecord(replayed.data).results as unknown[])[0]);
    assert(acceptedReceipt.opId === replayedReceipt.opId, "idempotent replay returned a different operation receipt");
    assert(acceptedReceipt.revision === replayedReceipt.revision, "idempotent replay returned a different revision receipt");
  });
}

async function reconnectAndRestartScenarios(h: Harness): Promise<void> {
  await h.case("RF-WS-001", "WebSocket reconnect resumes exclusively from the last cursor", ["public-api", "event-cursor", "websocket-resume"], async () => {
    const anchorPath = h.path("events/anchor.md");
    await writeRemoteFile(h, anchorPath, "anchor");
    const anchorEvents = await h.poll(
      "anchor event",
      () => readEvents(h),
      (events) => events.some((event) => event.path === anchorPath && Boolean(event.eventId)),
    );
    const cursor = anchorEvents.find((event) => event.path === anchorPath)?.eventId;
    assert(cursor, "anchor event cursor missing");

    const firstConnection = await openWebSocket(h, cursor);
    await closeWebSocket(firstConnection.socket);
    const paths = [0, 1, 2].map((index) => h.path(`events/resume-${index}.md`));
    for (const [index, path] of paths.entries()) await writeRemoteFile(h, path, `event-${index}`);
    const received = await collectWebSocketEvents(h, cursor, paths);
    const resumed = received.filter((event) => paths.includes(event.path ?? ""));
    assert(resumed.map((event) => event.path).join("|") === paths.join("|"), "reconnected events were not delivered in order");
    const ids = resumed.map((event) => event.eventId);
    assert(ids.length === new Set(ids).size, "WebSocket reconnect delivered duplicate event IDs");
    assert(!received.some((event) => event.eventId === cursor), "exclusive cursor replayed the anchor event");
  });

  await h.case("RF-WS-003", "WebSocket authorization cannot cross tenant boundaries", ["tenant-auth", "websocket-resume"], async () => {
    await expectWebSocketRejected(h, h.target.tokens.secondary, h.target.workspaces.primary);
  });

  await h.case("RF-DUR-001", "acknowledged file and event survive runtime restart", ["public-api", "durable-restart"], async () => {
    const path = h.path("durability/restart.md");
    const accepted = await writeRemoteFile(h, path, "survives restart");
    const opId = asRecord(accepted.data).opId;
    assert(typeof opId === "string" && opId.length > 0, "202 acknowledgement omitted durable opId");
    const before = await readFileResponse(h, path);
    const beforeEvents = (await readEvents(h)).filter((event) => event.path === path).map((event) => event.eventId);
    await h.restart();
    const after = await readFileResponse(h, path);
    const afterEvents = (await readEvents(h)).filter((event) => event.path === path).map((event) => event.eventId);
    assert(after.content === before.content, "restart changed acknowledged content");
    assert(after.revision === before.revision, "restart changed acknowledged revision");
    assert(JSON.stringify(afterEvents) === JSON.stringify(beforeEvents), "restart lost or duplicated durable events");
    const operation = await h.request(
      "GET",
      h.workspacePath(h.target.workspaces.primary, `/ops/${encodeURIComponent(opId)}`),
    );
    assertStatus(operation, 200);
    assert(asRecord(operation.data).opId === opId, "restart lost the acknowledged writeback operation");
  });

  await h.case("RF-WS-002", "eviction reconnect resumes from a durable cursor without gaps", ["runtime-eviction", "websocket-resume"], async () => {
    const anchorPath = h.path("events/eviction-anchor.md");
    await writeRemoteFile(h, anchorPath, "anchor");
    const anchorEvents = await h.poll(
      "eviction anchor",
      () => readEvents(h),
      (events) => events.some((event) => event.path === anchorPath && Boolean(event.eventId)),
    );
    const cursor = anchorEvents.find((event) => event.path === anchorPath)?.eventId;
    assert(cursor, "eviction anchor cursor missing");
    const live = await openWebSocket(h, cursor);
    const closed = waitForWebSocketClose(live.socket);
    await h.control("runtime.evict");
    await closed;

    const paths = [0, 1, 2].map((index) => h.path(`events/after-eviction-${index}.md`));
    for (const [index, path] of paths.entries()) await writeRemoteFile(h, path, `after-eviction-${index}`);
    const received = await collectWebSocketEvents(h, cursor, paths);
    const resumed = received.filter((event) => paths.includes(event.path ?? ""));
    assert(resumed.length === paths.length, `eviction resume delivered ${resumed.length}/${paths.length} expected events`);
    assert(new Set(resumed.map((event) => event.eventId)).size === resumed.length, "eviction resume duplicated an event");
  });
}

async function contractScenarios(h: Harness): Promise<void> {
  await h.case("RF-OAS-001", "repository OpenAPI surface check passes", ["public-api"], async () => {
    await execFileAsync("bash", ["scripts/check-contract-surface.sh"], {
      env: { ...process.env, E2E_TELEMETRY_DISABLED: "1" },
    });
  });

  await h.case("RF-OAS-002", "runtime errors use the documented envelope and correlation ID", ["public-api"], async () => {
    const path = h.path("contract/missing-if-match.md");
    const response = await h.request(
      "PUT",
      h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`),
      { body: { content: "missing precondition" } },
    );
    assertStatus(response, 412);
    assertErrorEnvelope(response);
    const body = asRecord(response.data);
    assert(body.correlationId === response.correlationId, "error did not echo X-Correlation-Id in correlationId");
  });
}

async function advancedAdapterScenarios(h: Harness): Promise<void> {
  if (h.target.runtime.kind === "terse-durable-actors") {
    await h.case("RF-AUTH-005", "runtime actor rejects missing and invalid shared secrets", ["runtime-auth-probe"], async () => {
      await h.verifyRuntimeAuthRejects();
    });
  } else {
    h.notApplicable(
      "RF-AUTH-005",
      "runtime actor rejects missing and invalid shared secrets",
      `not applicable to ${h.target.runtime.kind}`,
    );
  }

  await h.case("RF-QUEUE-001", "retry progresses after backoff with no inbound traffic", ["provider-faults", "clock-control"], async () => {
    const path = h.path("queue/retry.md");
    await h.control("provider.configure", { ingestFailures: 2, matchPath: path });
    const response = await h.request("POST", h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest"), {
      body: webhookBody(path, `${h.seed}-retry`, "retry succeeds", "2026-01-02T03:04:05.000Z", "retry-object"),
    });
    assertStatus(response, 202);
    await h.control("clock.advance", { milliseconds: 60_000 });
    const calls = await h.control<{ attempts: number }>("provider.calls", { matchPath: path });
    assert(calls.attempts === 3, `expected 3 attempts, got ${calls.attempts}`);
    assertInspectedFile(await inspectState(h, [path]), path, "retry succeeds");
    await waitForFile(h, path, "retry succeeds");
  });

  await h.case("RF-QUEUE-002", "poison record reaches DLQ without wedging later work and replays once", ["provider-faults", "clock-control"], async () => {
    const poisonPath = h.path("queue/poison.md");
    const goodPath = h.path("queue/good.md");
    await h.control("provider.configure", { permanentIngestFailurePath: poisonPath });
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const poison = await h.request("POST", endpoint, {
      body: webhookBody(poisonPath, `${h.seed}-poison`, "poison", "2026-01-02T03:04:05.000Z", "poison-object"),
    });
    assertStatus(poison, 202);
    await h.poll(
      "poison enters retry without exhaustion",
      () => h.control<{ attempts: number; state: string }>("provider.calls", { matchPath: poisonPath }),
      (calls) => calls.attempts >= 1 && calls.state === "retrying",
    );
    const good = await h.request("POST", endpoint, {
      body: webhookBody(goodPath, `${h.seed}-good`, "good", "2026-01-02T03:04:06.000Z", "good-object"),
    });
    assertStatus(good, 202);
    await h.poll(
      "good record bypasses poison before its retry budget expires",
      () => inspectState(h, [goodPath]),
      (state) => inspectedFile(state, goodPath)?.content === "good",
    );
    await waitForFile(h, goodPath, "good");
    await h.control("clock.advance", { milliseconds: 300_000 });
    const inspectedDlq = await h.poll(
      "poison DLQ record without actor wakeup",
      () => inspectState(h, [], { deliveryIds: [`${h.seed}-poison`] }),
      (state) => state.deadLetters?.some((item) => item.deliveryId === `${h.seed}-poison`) === true,
    );
    const item = inspectedDlq.deadLetters?.find(
      (candidate) => candidate.deliveryId === `${h.seed}-poison`,
    );
    assert(item && typeof item.envelopeId === "string", "poison record missing from DLQ");
    await h.control("provider.configure", { permanentIngestFailurePath: null });
    assertStatus(
      await h.request(
        "POST",
        h.workspacePath(h.target.workspaces.primary, `/sync/dead-letter/${encodeURIComponent(String(item.envelopeId))}/replay`),
        { body: {} },
      ),
      202,
    );
    await h.control("clock.advance", { milliseconds: 60_000 });
    const replayed = await inspectState(h, [poisonPath]);
    assertInspectedFile(replayed, poisonPath, "poison");
    assert(replayed.eventCounts?.[poisonPath] === 1, `DLQ replay produced ${replayed.eventCounts?.[poisonPath]} events`);
    await waitForFile(h, poisonPath, "poison");
    const events = (await readEvents(h)).filter((event) => event.path === poisonPath && /^file\./u.test(event.type ?? ""));
    assert(events.length === 1, `DLQ replay applied poison record ${events.length} times`);
  });

  await h.case("RF-QUEUE-003", "ingest backpressure returns Retry-After and recovers", ["provider-faults", "clock-control"], async () => {
    const path = h.path("queue/backpressure.md");
    await h.control("provider.configure", { ingestBackpressure: { count: 1, retryAfterSeconds: 2 }, matchPath: path });
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const body = webhookBody(path, `${h.seed}-backpressure`, "accepted after pressure", "2026-01-02T03:04:05.000Z", "backpressure-object");
    const throttled = await h.request("POST", endpoint, { body });
    assertStatus(throttled, 429);
    assert(Number(throttled.headers.get("retry-after")) >= 1, "429 response omitted a positive Retry-After");
    await h.control("clock.advance", { milliseconds: 2_000 });
    const pressure = await inspectState(h, [], { backpressurePath: path });
    assert(pressure.backpressureActive === false, "backpressure did not clear without inbound traffic");
    assertStatus(await h.request("POST", endpoint, { body }), 202);
    await waitForFile(h, path, "accepted after pressure");
  });

  await h.case("RF-PROJ-001", "provider mutation emits file and digest artifacts without recursion", ["webhook-ingest", "digest-projection", "clock-control"], async () => {
    const path = h.path("projection/record.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    await h.control("clock.advance", { milliseconds: 20_000 });
    const digestBefore = await inspectState(h, ["/digests/today.md"]);
    const digestEventsBefore = digestBefore.eventCounts?.["/digests/today.md"] ?? 0;
    assertStatus(
      await h.request("POST", endpoint, {
        body: webhookBody(path, `${h.seed}-projection`, "state: open", "2026-01-02T03:04:05.000Z", "projection-object"),
      }),
      202,
    );
    await h.control("clock.advance", { milliseconds: 20_000 });
    const projected = await inspectState(h, [path, "/digests/today.md"]);
    assertInspectedFile(projected, path, "state: open");
    assert(inspectedFile(projected, "/digests/today.md")?.content?.includes("generated_at"), "today digest was not regenerated out-of-band");
    await waitForFile(h, path, "state: open");
    const digest = await waitForFile(h, "/digests/today.md");
    assert(digest.content?.includes("generated_at"), "today digest was not regenerated");
    await h.control("clock.advance", { milliseconds: 20_000 });
    const digestAfter = await inspectState(h, ["/digests/today.md"]);
    assert(
      (digestAfter.eventCounts?.["/digests/today.md"] ?? 0) - digestEventsBefore === 1,
      "one provider mutation did not produce exactly one digest event; digest regeneration may be recursive",
    );
    const sentinel = `manual-${h.seed}`;
    const manualEventsBefore = digestAfter.eventCounts?.["/digests/today.md"] ?? 0;
    await writeRemoteFile(h, "/digests/today.md", sentinel);
    await h.control("clock.advance", { milliseconds: 20_000 });
    const manualDigest = await inspectState(h, ["/digests/today.md"]);
    assertInspectedFile(manualDigest, "/digests/today.md", sentinel);
    assert(
      (manualDigest.eventCounts?.["/digests/today.md"] ?? 0) - manualEventsBefore === 1,
      "a direct digest write regenerated recursively instead of emitting exactly its own event",
    );
  });

  await h.case("RF-PROJ-002", "terminal state persists until an upstream delete", ["webhook-ingest", "digest-projection", "clock-control"], async () => {
    const path = h.path("projection/terminal.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    assertStatus(
      await h.request("POST", endpoint, {
        body: webhookBody(path, `${h.seed}-terminal`, "state: closed", "2026-01-02T03:04:05.000Z", "terminal-object", {
          state: "closed",
        }),
      }),
      202,
    );
    await h.control("clock.advance", { milliseconds: 20_000 });
    const terminal = await inspectState(h, [path, "/digests/today.md"]);
    assertInspectedFile(terminal, path, "state: closed");
    assert(inspectedFile(terminal, "/digests/today.md")?.exists === true, "terminal mutation did not update digest");
    await waitForFile(h, path, "state: closed");
    await waitForFile(h, "/digests/today.md");
    assertStatus(
      await h.request("POST", endpoint, {
        body: webhookBody(path, `${h.seed}-terminal-delete`, "", "2026-01-02T03:05:05.000Z", "terminal-object", {}, "file.deleted"),
      }),
      202,
    );
    await h.poll(
      "upstream delete durable projection",
      () => inspectState(h, [path]),
      (state) => inspectedFile(state, path)?.exists === false,
    );
    await h.poll(
      "upstream delete projection",
      () => h.request("GET", h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`)),
      (response) => response.status === 404,
    );
  });

  await h.case("RF-PROJ-003", "writeback echo does not recursively trigger a provider write", ["provider-faults", "clock-control"], async () => {
    const path = h.path("projection/no-recursion.md");
    await h.control("provider.configure", { matchPath: path, echoWritebackWebhook: true });
    await writeRemoteFile(h, path, "one provider mutation");
    await h.control("clock.advance", { milliseconds: 60_000 });
    const calls = await h.control<{ writebackAttempts: number; echoDeliveries: number }>("provider.calls", { matchPath: path });
    assert(calls.writebackAttempts === 1, `writeback echo produced ${calls.writebackAttempts} provider calls`);
    assert(calls.echoDeliveries === 1, `adapter did not deliver exactly one upstream echo (${calls.echoDeliveries})`);
  });

  await h.case("RF-TIMER-001", "day rollover advances today to yesterday without traffic", ["digest-projection", "clock-control"], async () => {
    const sourcePath = h.path("timers/rollover-source.md");
    assertStatus(
      await h.request("POST", h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest"), {
        body: webhookBody(sourcePath, `${h.seed}-rollover`, "rollover", "2026-01-02T03:04:05.000Z", "rollover-object"),
      }),
      202,
    );
    await h.control("clock.advance", { milliseconds: 20_000 });
    const initialized = await inspectState(h, ["/digests/today.md", "/digests/yesterday.md"]);
    const before = inspectedFile(initialized, "/digests/today.md");
    assert(before?.exists && before.content, "self-contained rollover setup did not create today.md");
    const oldYesterdayRevision = inspectedFile(initialized, "/digests/yesterday.md")?.revision;
    await h.control("clock.advance", { milliseconds: 86_400_000 });
    const rolled = await inspectState(h, ["/digests/today.md", "/digests/yesterday.md"]);
    const yesterdayState = inspectedFile(rolled, "/digests/yesterday.md");
    assert(yesterdayState?.exists, "yesterday digest missing after out-of-band clock advance");
    assert(yesterdayState.content === before.content, "yesterday digest does not equal the prior today digest");
    assert(yesterdayState.revision !== oldYesterdayRevision, "rollover did not create a new yesterday revision");
    const yesterday = await waitForFile(h, "/digests/yesterday.md");
    assert(yesterday.content === before.content, "public yesterday digest differs from inspected rollover state");
  });

  await h.case("RF-TIMER-002", "idempotency identity expires only after clock advance", ["clock-control"], async () => {
    const path = h.path("timers/idempotency.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/fs/bulk");
    const body = {
      files: [{
        path,
        content: "ttl payload",
        ifMatch: "*",
        contentIdentity: { kind: "conformance", key: `${h.seed}-ttl`, ttlSeconds: 30 },
      }],
    };
    assertStatus(await h.request("POST", endpoint, { body }), [200, 202]);
    const first = await readFileResponse(h, path);
    assertStatus(await h.request("POST", endpoint, { body }), [200, 202]);
    assert((await readFileResponse(h, path)).revision === first.revision, "identity expired before its TTL");
    await h.control("clock.advance", { milliseconds: 31_000 });
    const identity = await inspectState(h, [], { contentIdentity: { kind: "conformance", key: `${h.seed}-ttl` } });
    assert(identity.identityActive === false, "idempotency identity did not expire without inbound traffic");
    assertStatus(await h.request("POST", endpoint, { body }), [200, 202]);
    assert((await readFileResponse(h, path)).revision !== first.revision, "identity did not expire after clock advance");
  });

  await h.case("RF-LIMIT-001", "bulk projection above the Terse 512-effect boundary commits", ["public-api", "large-effect-batch"], async () => {
    const anchorPath = h.path("effects/anchor.txt");
    await writeRemoteFile(h, anchorPath, "anchor");
    const anchorEvents = await readEvents(h);
    const cursor = anchorEvents.find((event) => event.path === anchorPath)?.eventId;
    assert(cursor, "large-effect anchor cursor missing");
    const subscriber = await openWebSocket(h, cursor);
    const files = Array.from({ length: 513 }, (_, index) => ({
      path: h.path(`effects/${String(index).padStart(3, "0")}.txt`),
      contentType: "text/plain",
      content: String(index),
      ifMatch: "*",
    }));
    try {
      const response = await h.request("POST", h.workspacePath(h.target.workspaces.primary, "/fs/bulk"), { body: { files } });
      assertStatus(response, [200, 202]);
      assert(asRecord(response.data).written === files.length, `bulk wrote ${asRecord(response.data).written}, expected 513`);
      await h.poll(
        "513 socket broadcast effects",
        async () => subscriber.received,
        (events) => new Set(events.filter((event) => files.some((file) => file.path === event.path)).map((event) => event.path)).size === files.length,
        30_000,
      );
      const delivered = subscriber.received.filter((event) => files.some((file) => file.path === event.path));
      assert(delivered.length === files.length, `socket delivered ${delivered.length}, expected exactly 513`);
    } finally {
      await closeWebSocket(subscriber.socket);
    }
  });

  await h.case("RF-SER-001", "a held mutation on object X does not block object Y", ["provider-faults"], async () => {
    const heldPath = h.path("serialization/held.md");
    const freePath = h.path("serialization/free.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    await h.control("provider.configure", { holdIngestPath: heldPath });
    const held = h.request("POST", endpoint, {
      body: webhookBody(heldPath, `${h.seed}-held`, "held", "2026-01-02T03:04:05.000Z", "held-object"),
    });
    await h.poll(
      "held provider call",
      () => h.control<{ held: boolean }>("provider.calls", { matchPath: heldPath }),
      (calls) => calls.held === true,
    );
    try {
      assertStatus(
        await h.request("POST", endpoint, {
          body: webhookBody(freePath, `${h.seed}-free`, "free", "2026-01-02T03:04:06.000Z", "free-object"),
        }),
        202,
      );
      await waitForFile(h, freePath, "free");
    } finally {
      await h.control("provider.configure", { releaseIngestPath: heldPath });
    }
    assertStatus(await held, 202);
    await waitForFile(h, heldPath, "held");
  });

  await h.case("RF-CRASH-001", "commit survives crash between durable mutation and response", ["runtime-crash", "durable-restart", "provider-faults", "state-inspection"], async () => {
    const beforePath = h.path("crash/before-commit.md");
    await crashWriteAtBarrier(h, beforePath, "must roll back", "before-commit");
    await h.restart();
    const rolledBack = await inspectState(h, [beforePath]);
    assert(inspectedFile(rolledBack, beforePath)?.exists === false, "crash-before-commit left a file behind");
    assert((rolledBack.eventCounts?.[beforePath] ?? 0) === 0, "crash-before-commit left an event behind");
    assert((rolledBack.operations ?? []).length === 0, "crash-before-commit left an outbox operation behind");

    const path = h.path("crash/after-commit.md");
    await crashWriteAtBarrier(h, path, "committed before crash", "after-commit");
    await h.restart();
    const committed = await inspectState(h, [path]);
    assertInspectedFile(committed, path, "committed before crash");
    assert(committed.eventCounts?.[path] === 1, `crash recovery left ${committed.eventCounts?.[path]} file events`);
    const operation = committed.operations?.find((candidate) => candidate.state === "pending" || candidate.state === "succeeded");
    assert(operation, "crash-after-commit lost the outbox operation");
    assert(operation.writebackAttempts === 1, `recovered outbox executed ${operation.writebackAttempts} times`);
    const file = await waitForFile(h, path, "committed before crash");
    assert(file.revision, "crash-recovered file has no revision");
    const events = (await readEvents(h)).filter((event) => event.path === path && /^file\./u.test(event.type ?? ""));
    assert(events.length === 1, `crash recovery left ${events.length} file events`);
    const publicOperation = await h.request("GET", h.workspacePath(h.target.workspaces.primary, `/ops/${encodeURIComponent(operation.opId)}`));
    assertStatus(publicOperation, 200);
  });

  await h.case("RF-MIG-001", "export/import preserves revisions, cursors, pending work, and DLQ", ["state-migration", "provider-faults", "clock-control"], async () => {
    const path = h.path("migration/state.md");
    const deadLetterId = `${h.seed}-dead-letter`;
    const historyPaths = [h.path("migration/history-a.md"), h.path("migration/history-b.md")];
    await writeRemoteFile(h, historyPaths[0]!, "history a");
    await writeRemoteFile(h, historyPaths[1]!, "history b");
    await writeRemoteFile(h, path, "migration state");
    const before = await readFileResponse(h, path);
    const eventsBefore = await readEvents(h);
    const eventIdsBefore = eventsBefore.flatMap((event) => event.eventId ? [event.eventId] : []);
    const migrationEventIds = eventsBefore
      .filter((event) => [...historyPaths, path].includes(event.path ?? ""))
      .flatMap((event) => event.eventId ? [event.eventId] : []);
    assert(migrationEventIds.length >= 3, "migration setup did not create enough cursor history");
    await h.control("provider.configure", {
      seedMigrationState: {
        pendingOutboxId: `${h.seed}-pending`,
        deadLetterId,
      },
    });
    const exported = await h.control<Record<string, unknown>>("state.export");
    assert(exported.artifact, "state export did not return an artifact handle");
    const exportedManifest = asRecord(exported.manifest);
    assert(
      Array.isArray(exportedManifest.pendingOutboxIds) && exportedManifest.pendingOutboxIds.includes(`${h.seed}-pending`),
      "state export omitted the pending outbox operation",
    );
    assert(
      Array.isArray(exportedManifest.deadLetterIds) && exportedManifest.deadLetterIds.includes(deadLetterId),
      "state export omitted the DLQ record",
    );
    const destinationRuntime = h.target.runtime.kind === "cloudflare-do" ? "terse-durable-actors" : "cloudflare-do";
    const imported = await h.control<Record<string, unknown>>("state.import", {
      artifact: exported.artifact,
      destinationRuntime,
    });
    assert(imported.servingRuntime === destinationRuntime, "import did not route the public edge to the destination runtime");
    const importedManifest = asRecord(imported.manifest);
    assert(
      JSON.stringify(importedManifest.pendingOutboxIds) === JSON.stringify(exportedManifest.pendingOutboxIds),
      "migration changed pending outbox contents",
    );
    assert(
      JSON.stringify(importedManifest.deadLetterIds) === JSON.stringify(exportedManifest.deadLetterIds),
      "migration changed DLQ contents",
    );
    const after = await readFileResponse(h, path);
    const eventsAfter = await readEvents(h);
    const eventIdsAfter = eventsAfter.flatMap((event) => event.eventId ? [event.eventId] : []);
    assert(after.revision === before.revision, "migration changed file revision");
    assert(
      JSON.stringify(eventIdsAfter) === JSON.stringify(eventIdsBefore),
      "migration changed event history or ordering",
    );
    const replayCursor = migrationEventIds[0]!;
    const replayExpected = migrationEventIds.slice(1);
    const replayedIds = await collectWebSocketEventIds(h, replayCursor, replayExpected);
    assert(
      JSON.stringify(replayedIds) === JSON.stringify(replayExpected),
      "migration cursor replay lost, duplicated, or reordered events",
    );
    const routed = await inspectState(h, [path], { operationIds: [`${h.seed}-pending`] });
    assert(routed.servingRuntime === destinationRuntime, "out-of-band inspection is not reading the imported destination");
    assertInspectedFile(routed, path, "migration state");
    assertStatus(
      await h.request(
        "POST",
        h.workspacePath(h.target.workspaces.primary, `/sync/dead-letter/${encodeURIComponent(deadLetterId)}/replay`),
        { body: {} },
      ),
      202,
    );
    await h.control("clock.advance", { milliseconds: 60_000 });
    const progressed = await inspectState(h, [path], {
      operationIds: [`${h.seed}-pending`],
      deliveryIds: [deadLetterId],
    });
    const pending = progressed.operations?.find((operation) => operation.opId === `${h.seed}-pending`);
    assert(pending?.state === "succeeded", "imported pending outbox operation did not execute on the destination");
    assert(pending.writebackAttempts === 1, `imported pending outbox executed ${pending.writebackAttempts} times`);
    assert(!progressed.deadLetters?.some((item) => item.envelopeId === deadLetterId), "destination DLQ replay did not clear the record");
  });

  await h.case("RF-FAILOVER-001", "replica switching retains a single monotonic writer", ["runtime-failover"], async () => {
    const path = h.path("failover/shared.md");
    await writeRemoteFile(h, path, "base");
    const base = await readFileResponse(h, path);
    const switchReady = await h.control<{ switchId: string; state: string }>("runtime.failover", {
      phase: "begin",
      pause: true,
    });
    assert(switchReady.switchId && switchReady.state === "fenced", "failover did not establish the fencing barrier");
    const writes = Promise.allSettled(
      ["left", "right"].map((content) =>
        h.request("PUT", h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`), {
          headers: { "If-Match": base.revision! },
          body: { content },
        }),
      ),
    );
    try {
      const waiting = await h.control<{ pendingWriters: number }>("runtime.failover", {
        phase: "await-writers",
        switchId: switchReady.switchId,
        count: 2,
      });
      assert(waiting.pendingWriters >= 2, "failover did not hold both writers behind the fence");
    } finally {
      await h.control("runtime.failover", { phase: "release", switchId: switchReady.switchId });
    }
    const settled = await writes;
    assert(settled.every((result) => result.status === "fulfilled"), "a fenced writer lost its public response");
    const results = settled.flatMap((result) => result.status === "fulfilled" ? [result.value] : []);
    assert(results.filter((response) => [200, 202].includes(response.status)).length === 1, "failover admitted multiple writers");
    assert(results.filter((response) => response.status === 409).length === 1, "failover did not fence the losing writer");
    const winnerIndex = results.findIndex((response) => [200, 202].includes(response.status));
    const winner = ["left", "right"][winnerIndex];
    const after = await readFileResponse(h, path);
    assert(after.content === winner, `failover readback ${after.content} did not match winner ${winner}`);
    const beforeRevision = revisionNumber(base.revision!);
    const afterRevision = revisionNumber(after.revision!);
    if (beforeRevision !== undefined && afterRevision !== undefined) {
      assert(afterRevision === beforeRevision + 1, "failover winning revision was not monotonic by exactly one");
    }
    const third = await h.request("PUT", h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`), {
      headers: { "If-Match": after.revision! },
      body: { content: "post-failover" },
    });
    assertStatus(third, [200, 202]);
    assert((await readFileResponse(h, path)).content === "post-failover", "new writer did not accept a post-failover write");
  });

  await h.case("RF-MOUNT-001", "read, write, and mirror transitions preserve durable mount state", ["mount-control", "runtime-eviction"], async () => {
    const remotePath = h.path("mount/transition.md");
    await writeRemoteFile(h, remotePath, "remote base");
    const readMount = await h.control<{ root: string; id: string }>("mount.start", { mode: "read" });
    const localPath = join(readMount.root, remotePath.replace(/^\//u, ""));
    await h.poll("read mount projection", () => readFile(localPath, "utf8").catch(() => ""), (content) => content === "remote base", 30_000);
    const readInfo = await stat(localPath);
    assert((readInfo.mode & 0o222) === 0, "read-mode projection remained writable");
    await h.control("mount.stop", { id: readMount.id });

    const writeMount = await h.control<{ root: string; id: string }>("mount.start", { mode: "write" });
    const writePath = join(writeMount.root, remotePath.replace(/^\//u, ""));
    await writeFile(writePath, "write-mode update");
    await h.poll("write-mode provider update", () => readFileResponse(h, remotePath), (file) => file.content === "write-mode update", 30_000);
    await h.control("mount.stop", { id: writeMount.id });

    const mirror = await h.control<{ root: string; id: string; maxReconnectDelayMs: number }>("mount.start", { mode: "mirror", resetAfterClobber: true });
    assert(mirror.maxReconnectDelayMs <= 30_000, `mount reconnect backoff exceeds 30s (${mirror.maxReconnectDelayMs}ms)`);
    const mirrorPath = join(mirror.root, remotePath.replace(/^\//u, ""));
    await h.poll("mirror transition backfill", () => readFile(mirrorPath, "utf8").catch(() => ""), (content) => content === "write-mode update", 30_000);
    await h.control("runtime.evict");
    await writeRemoteFile(h, remotePath, "post-eviction remote update");
    await h.poll(
      "mirror reconnect",
      () => readFile(mirrorPath, "utf8").catch(() => ""),
      (content) => content === "post-eviction remote update",
      30_000,
    );

    await writeFile(mirrorPath, "local conflict");
    await writeRemoteFile(h, remotePath, "remote conflict");
    await h.poll(
      "mirror conflict artifact",
      () => readdir(join(mirror.root, ".relay", "conflicts"), { recursive: true }).catch(() => []),
      (entries) => entries.some((entry) => String(entry).includes("transition.md") && String(entry).endsWith(".local")),
      30_000,
    );
    await h.control("mount.stop", { id: mirror.id });
  });
}

function webhookBody(
  path: string,
  deliveryId: string,
  content: string,
  timestamp: string,
  objectId: string,
  extraData: Record<string, unknown> = {},
  eventType = "file.updated",
): Record<string, unknown> {
  return {
    provider: "conformance",
    event_type: eventType,
    path,
    data: { content, contentType: "text/plain", providerObjectId: objectId, ...extraData },
    delivery_id: deliveryId,
    timestamp,
  };
}

async function webhookFixture(
  path: string,
  deliveryId: string,
  content: string,
  objectId: string,
): Promise<Record<string, unknown>> {
  const fixture = JSON.parse(
    await readFile("test/conformance/fixtures/webhook-upsert.json", "utf8"),
  ) as Record<string, unknown>;
  return {
    ...fixture,
    path,
    delivery_id: deliveryId,
    data: {
      ...asRecord(fixture.data),
      content,
      providerObjectId: objectId,
    },
  };
}

async function inspectState(
  h: Harness,
  paths: string[],
  extra: Record<string, unknown> = {},
): Promise<StateInspection> {
  return h.verifyStateInspection(() => h.control<StateInspection>("state.inspect", { paths, ...extra }));
}

function inspectedFile(state: StateInspection, path: string): InspectedFile | undefined {
  return state.files?.[path];
}

function assertInspectedFile(state: StateInspection, path: string, content: string): void {
  const file = inspectedFile(state, path);
  assert(file?.exists === true, `out-of-band inspection says ${path} does not exist`);
  assert(file.content === content, `out-of-band inspection returned unexpected content for ${path}`);
}

async function writeRemoteFile(h: Harness, path: string, content: string): Promise<ApiResponse<FileBody>> {
  const response = await h.request<FileBody>(
    "PUT",
    h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`),
    { headers: { "If-Match": "*" }, body: { contentType: "text/plain", content } },
  );
  assertStatus(response, [200, 202]);
  return response;
}

async function crashWriteAtBarrier(
  h: Harness,
  path: string,
  content: string,
  phase: "before-commit" | "after-commit",
): Promise<void> {
  await h.control("provider.configure", { crashBarrier: { matchPath: path, phase } });
  const outcome = h.request(
    "PUT",
    h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`),
    { headers: { "If-Match": "*" }, body: { content } },
  ).then(
    (response) => ({ kind: "response", status: response.status } as const),
    (error: unknown) => {
      if (error instanceof RequestTransportError) return { kind: "transport" } as const;
      return { kind: "error", error } as const;
    },
  );
  await h.poll(
    `${phase} crash barrier`,
    () => h.control<{ crashReady: boolean; crashPhase: string }>("provider.calls", { matchPath: path }),
    (calls) => calls.crashReady === true && calls.crashPhase === phase,
  );
  const crashed = await h.control<{ terminated: boolean }>("runtime.crash", { matchPath: path, phase });
  assert(crashed.terminated === true, `runtime did not terminate at the ${phase} barrier`);
  const result = await settleWithin(outcome, 5_000, `${phase} public write did not terminate after crash`);
  if (result.kind === "error") throw result.error;
  if (result.kind === "response") {
    throw new Error(`${phase} public write returned HTTP ${result.status} instead of losing its response`);
  }
  assert(result.kind === "transport", `${phase} public write did not lose its response`);
}

async function readFileResponse(h: Harness, path: string): Promise<FileBody> {
  const response = await h.request<FileBody>(
    "GET",
    h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`),
  );
  assertStatus(response, 200);
  return response.data;
}

async function waitForFile(h: Harness, path: string, content?: string): Promise<FileBody> {
  const response = await h.poll(
    `file ${path}`,
    () => h.request<FileBody>("GET", h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`)),
    (candidate) => candidate.status === 200 && (content === undefined || candidate.data.content === content),
    10_000,
  );
  return response.data;
}

async function readEvents(h: Harness): Promise<EventItem[]> {
  const events: EventItem[] = [];
  const seenCursors = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; page < 100; page++) {
    const query = new URLSearchParams({ limit: "1000", direction: "asc" });
    if (cursor) query.set("cursor", cursor);
    const response = await h.request<EventsBody>(
      "GET",
      h.workspacePath(h.target.workspaces.primary, `/fs/events?${query.toString()}`),
    );
    assertStatus(response, 200);
    assert(Array.isArray(response.data.events), "events response omitted events array");
    events.push(...response.data.events);
    const next = response.data.nextCursor ?? undefined;
    if (!next) return events;
    assert(!seenCursors.has(next), `events pagination repeated cursor ${next}`);
    seenCursors.add(next);
    cursor = next;
  }
  throw new Error("events pagination exceeded 100 pages");
}

interface SocketCapture {
  socket: WebSocket;
  received: EventItem[];
}

async function openWebSocket(
  h: Harness,
  cursor: string,
  token = h.target.tokens.primary,
  workspace = h.target.workspaces.primary,
): Promise<SocketCapture> {
  assert(token, "primary token missing");
  const wsUrl = `${h.target.baseUrl.replace(/^http/u, "ws")}${h.workspacePath(
    workspace,
    `/fs/ws?token=${encodeURIComponent(token)}&cursor=${encodeURIComponent(cursor)}`,
  )}`;
  const socket = new WebSocket(wsUrl);
  const received: EventItem[] = [];
  socket.addEventListener("message", (message) => {
    const parsed = JSON.parse(String(message.data)) as EventItem;
    if (parsed.eventId) received.push(parsed);
  });
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("WebSocket did not open")), 5_000);
    socket.addEventListener("open", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
    socket.addEventListener("error", () => reject(new Error("WebSocket open failed")), { once: true });
  });
  return { socket, received };
}

async function collectWebSocketEvents(h: Harness, cursor: string, expectedPaths: string[]): Promise<EventItem[]> {
  const capture = await openWebSocket(h, cursor);
  try {
    await h.poll(
      "WebSocket cursor recovery",
      async () => capture.received,
      (events) => expectedPaths.every((path) => events.some((event) => event.path === path)),
      10_000,
    );
    return capture.received;
  } finally {
    await closeWebSocket(capture.socket);
  }
}

async function collectWebSocketEventIds(h: Harness, cursor: string, expectedIds: string[]): Promise<string[]> {
  const capture = await openWebSocket(h, cursor);
  try {
    await h.poll(
      "WebSocket migration cursor replay",
      async () => capture.received,
      (events) => expectedIds.every((id) => events.some((event) => event.eventId === id)),
      10_000,
    );
    const expected = new Set(expectedIds);
    return capture.received.flatMap((event) => event.eventId && expected.has(event.eventId) ? [event.eventId] : []);
  } finally {
    await closeWebSocket(capture.socket);
  }
}

async function closeWebSocket(socket: WebSocket): Promise<void> {
  if (socket.readyState === WebSocket.CLOSED) return;
  const closed = waitForWebSocketClose(socket);
  socket.close();
  await closed;
}

async function waitForWebSocketClose(socket: WebSocket): Promise<void> {
  if (socket.readyState === WebSocket.CLOSED) return;
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("WebSocket did not close within 5s")), 5_000);
    socket.addEventListener("close", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

async function expectWebSocketRejected(h: Harness, token: string | undefined, workspace: string): Promise<void> {
  assert(token, "secondary token missing");
  const wsUrl = `${h.target.baseUrl.replace(/^http/u, "ws")}${h.workspacePath(
    workspace,
    `/fs/ws?token=${encodeURIComponent(token)}&cursor=now`,
  )}`;
  const socket = new WebSocket(wsUrl);
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("cross-tenant WebSocket did not reject within 5s")), 5_000);
    socket.addEventListener("open", () => {
      clearTimeout(timer);
      socket.close();
      reject(new Error("cross-tenant WebSocket unexpectedly opened"));
    }, { once: true });
    const rejected = () => {
      clearTimeout(timer);
      resolve();
    };
    socket.addEventListener("error", rejected, { once: true });
    socket.addEventListener("close", rejected, { once: true });
  });
}

function assertErrorEnvelope(response: ApiResponse): void {
  const body = asRecord(response.data);
  assert(typeof body.code === "string" && body.code.length > 0, "error envelope omitted code");
  assert(typeof body.message === "string" && body.message.length > 0, "error envelope omitted message");
  assert(typeof body.correlationId === "string", "error envelope omitted correlationId");
}

function revisionNumber(revision: string): number | undefined {
  const match = /^rev_(\d+)$/u.exec(revision);
  return match ? Number(match[1]) : undefined;
}

async function settleWithin<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}
