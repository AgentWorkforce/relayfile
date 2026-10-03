import { readFile, readdir, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { Harness, asRecord, assert, assertStatus, type ApiResponse } from "./harness.js";

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
    const inside = "/conformance/scoped/inside.md";
    const outside = h.path("auth/outside.md");
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

    const token = h.target.tokens.primary;
    assert(token, "primary token missing");
    const wsUrl = `${h.target.baseUrl.replace(/^http/u, "ws")}${h.workspacePath(
      h.target.workspaces.primary,
      `/fs/ws?token=${encodeURIComponent(token)}&cursor=${encodeURIComponent(cursor)}`,
    )}`;
    const socket = new WebSocket(wsUrl);
    const received: EventItem[] = [];
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("WebSocket did not open")), 5_000);
      socket.addEventListener("open", () => {
        clearTimeout(timer);
        resolve();
      }, { once: true });
      socket.addEventListener("error", () => reject(new Error("WebSocket open failed")), { once: true });
    });
    socket.addEventListener("message", (message) => {
      const parsed = JSON.parse(String(message.data)) as EventItem;
      if (parsed.eventId) received.push(parsed);
    });
    const paths = [0, 1, 2].map((index) => h.path(`events/resume-${index}.md`));
    for (const [index, path] of paths.entries()) await writeRemoteFile(h, path, `event-${index}`);
    await h.poll("WebSocket resumed events", async () => received, (events) => paths.every((path) => events.some((event) => event.path === path)));
    socket.close();
    const ids = received.filter((event) => paths.includes(event.path ?? "")).map((event) => event.eventId);
    assert(ids.length === new Set(ids).size, "WebSocket reconnect delivered duplicate event IDs");
    assert(!received.some((event) => event.eventId === cursor), "exclusive cursor replayed the anchor event");
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
    await h.control("runtime.evict");

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
  await h.case("RF-QUEUE-001", "retry progresses after backoff with no inbound traffic", ["provider-faults", "clock-control"], async () => {
    const path = h.path("queue/retry.md");
    await h.control("provider.configure", { ingestFailures: 2, matchPath: path });
    const response = await h.request("POST", h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest"), {
      body: webhookBody(path, `${h.seed}-retry`, "retry succeeds", "2026-01-02T03:04:05.000Z", "retry-object"),
    });
    assertStatus(response, 202);
    await h.control("clock.advance", { milliseconds: 60_000 });
    await waitForFile(h, path, "retry succeeds");
    const calls = await h.control<{ attempts: number }>("provider.calls", { matchPath: path });
    assert(calls.attempts === 3, `expected 3 attempts, got ${calls.attempts}`);
  });

  await h.case("RF-QUEUE-002", "poison record reaches DLQ without wedging later work and replays once", ["provider-faults", "clock-control"], async () => {
    const poisonPath = h.path("queue/poison.md");
    const goodPath = h.path("queue/good.md");
    await h.control("provider.configure", { permanentIngestFailurePath: poisonPath });
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    const [poison, good] = await Promise.all([
      h.request("POST", endpoint, {
        body: webhookBody(poisonPath, `${h.seed}-poison`, "poison", "2026-01-02T03:04:05.000Z", "poison-object"),
      }),
      h.request("POST", endpoint, {
        body: webhookBody(goodPath, `${h.seed}-good`, "good", "2026-01-02T03:04:06.000Z", "good-object"),
      }),
    ]);
    assertStatus(poison, 202);
    assertStatus(good, 202);
    await h.control("clock.advance", { milliseconds: 300_000 });
    await waitForFile(h, goodPath, "good");
    const deadLetters = await h.poll(
      "poison DLQ record",
      () => h.request<Record<string, unknown>>("GET", h.workspacePath(h.target.workspaces.primary, "/sync/dead-letter")),
      (response) => Array.isArray(asRecord(response.data).items) && (asRecord(response.data).items as unknown[]).length > 0,
    );
    const item = (asRecord(deadLetters.data).items as Array<Record<string, unknown>>).find(
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
    assertStatus(await h.request("POST", endpoint, { body }), 202);
    await waitForFile(h, path, "accepted after pressure");
  });

  await h.case("RF-PROJ-001", "provider mutation emits file and digest artifacts without recursion", ["webhook-ingest", "digest-projection", "clock-control"], async () => {
    const path = h.path("projection/record.md");
    const endpoint = h.workspacePath(h.target.workspaces.primary, "/webhooks/ingest");
    assertStatus(
      await h.request("POST", endpoint, {
        body: webhookBody(path, `${h.seed}-projection`, "state: open", "2026-01-02T03:04:05.000Z", "projection-object"),
      }),
      202,
    );
    await h.control("clock.advance", { milliseconds: 20_000 });
    await waitForFile(h, path, "state: open");
    const digest = await waitForFile(h, "/digests/today.md");
    assert(digest.content?.includes("generated_at"), "today digest was not regenerated");
    const sentinel = `manual-${h.seed}`;
    await writeRemoteFile(h, "/digests/today.md", sentinel);
    await h.control("clock.advance", { milliseconds: 20_000 });
    const after = await readFileResponse(h, "/digests/today.md");
    assert(after.content === sentinel, "digest write recursively regenerated the digest");
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
    await waitForFile(h, path, "state: closed");
    await waitForFile(h, "/digests/today.md");
    assertStatus(
      await h.request("POST", endpoint, {
        body: webhookBody(path, `${h.seed}-terminal-delete`, "", "2026-01-02T03:05:05.000Z", "terminal-object", {}, "file.deleted"),
      }),
      202,
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
    const before = await readFileResponse(h, "/digests/today.md");
    await h.control("clock.advance", { milliseconds: 86_400_000 });
    const yesterday = await waitForFile(h, "/digests/yesterday.md");
    assert(yesterday.content && yesterday.content.length > 0, "yesterday digest missing after rollover");
    assert(yesterday.content !== before.content || yesterday.content?.includes("covers"), "rollover did not close the previous day");
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
    assertStatus(await h.request("POST", endpoint, { body }), [200, 202]);
    assert((await readFileResponse(h, path)).revision !== first.revision, "identity did not expire after clock advance");
  });

  await h.case("RF-LIMIT-001", "bulk projection above the Terse 512-effect boundary commits", ["public-api", "large-effect-batch"], async () => {
    const files = Array.from({ length: 513 }, (_, index) => ({
      path: h.path(`effects/${String(index).padStart(3, "0")}.txt`),
      contentType: "text/plain",
      content: String(index),
      ifMatch: "*",
    }));
    const response = await h.request("POST", h.workspacePath(h.target.workspaces.primary, "/fs/bulk"), { body: { files } });
    assertStatus(response, [200, 202]);
    assert(asRecord(response.data).written === files.length, `bulk wrote ${asRecord(response.data).written}, expected 513`);
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

  await h.case("RF-CRASH-001", "commit survives crash between durable mutation and response", ["runtime-crash", "durable-restart", "provider-faults"], async () => {
    const path = h.path("crash/after-commit.md");
    await h.control("provider.configure", { crashAfterCommitPath: path });
    try { await writeRemoteFile(h, path, "committed before crash"); } catch { /* connection loss is expected */ }
    await h.restart();
    const file = await waitForFile(h, path, "committed before crash");
    assert(file.revision, "crash-recovered file has no revision");
    const events = (await readEvents(h)).filter((event) => event.path === path && /^file\./u.test(event.type ?? ""));
    assert(events.length === 1, `crash recovery left ${events.length} file events`);
  });

  await h.case("RF-MIG-001", "export/import preserves revisions, cursors, pending work, and DLQ", ["state-migration", "provider-faults"], async () => {
    const path = h.path("migration/state.md");
    await writeRemoteFile(h, path, "migration state");
    const before = await readFileResponse(h, path);
    const eventsBefore = await readEvents(h);
    await h.control("provider.configure", {
      seedMigrationState: {
        pendingOutboxId: `${h.seed}-pending`,
        deadLetterId: `${h.seed}-dead-letter`,
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
      Array.isArray(exportedManifest.deadLetterIds) && exportedManifest.deadLetterIds.includes(`${h.seed}-dead-letter`),
      "state export omitted the DLQ record",
    );
    const imported = await h.control<Record<string, unknown>>("state.import", {
      artifact: exported.artifact,
      destinationRuntime: "terse-durable-actors",
    });
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
    assert(after.revision === before.revision, "migration changed file revision");
    assert(lastEventId(eventsAfter) === lastEventId(eventsBefore), "migration changed the event cursor");
  });

  await h.case("RF-FAILOVER-001", "replica switching retains a single monotonic writer", ["runtime-failover"], async () => {
    const path = h.path("failover/shared.md");
    await writeRemoteFile(h, path, "base");
    const base = await readFileResponse(h, path);
    const failover = h.control("runtime.failover", { phase: "switch", pause: true });
    const writes = Promise.all(
      ["left", "right"].map((content) =>
        h.request("PUT", h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`), {
          headers: { "If-Match": base.revision! },
          body: { content },
        }),
      ),
    );
    await failover;
    const results = await writes;
    assert(results.filter((response) => [200, 202].includes(response.status)).length === 1, "failover admitted multiple writers");
    assert(results.filter((response) => response.status === 409).length === 1, "failover did not fence the losing writer");
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
    await h.poll("mirror reconnect", () => readFile(mirrorPath, "utf8").catch(() => ""), (content) => content === "write-mode update", 30_000);

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

async function writeRemoteFile(h: Harness, path: string, content: string): Promise<ApiResponse<FileBody>> {
  const response = await h.request<FileBody>(
    "PUT",
    h.workspacePath(h.target.workspaces.primary, `/fs/file?path=${encodeURIComponent(path)}`),
    { headers: { "If-Match": "*" }, body: { contentType: "text/plain", content } },
  );
  assertStatus(response, [200, 202]);
  return response;
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
  const response = await h.request<EventsBody>(
    "GET",
    h.workspacePath(h.target.workspaces.primary, "/fs/events?limit=1000&direction=asc"),
  );
  assertStatus(response, 200);
  assert(Array.isArray(response.data.events), "events response omitted events array");
  return response.data.events;
}

async function collectWebSocketEvents(h: Harness, cursor: string, expectedPaths: string[]): Promise<EventItem[]> {
  const token = h.target.tokens.primary;
  assert(token, "primary token missing");
  const wsUrl = `${h.target.baseUrl.replace(/^http/u, "ws")}${h.workspacePath(
    h.target.workspaces.primary,
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
  try {
    await h.poll(
      "WebSocket cursor recovery",
      async () => received,
      (events) => expectedPaths.every((path) => events.some((event) => event.path === path)),
      10_000,
    );
    return received;
  } finally {
    socket.close();
  }
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

function lastEventId(events: EventItem[]): string | undefined {
  return [...events].reverse().find((event) => event.eventId)?.eventId;
}
