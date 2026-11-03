import { createHash, createHmac } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { api, HMAC_SECRET, startGateway, token, type TestGateway } from "./harness.js";

const WS = "ws_test";
const sha = (value: string | Buffer) => createHash("sha256").update(value).digest("hex");

describe("relayfile gateway on workspace actors", () => {
  let gw: TestGateway;
  const t = token(WS);
  beforeEach(async () => {
    gw = await startGateway();
  });
  afterEach(async () => {
    await gw.close();
  });

  it("advertises health features like the Go server", async () => {
    const res = await api(gw, "GET", "/health", { correlation: false });
    expect(res.status).toBe(200);
    expect(res.json.status).toBe("ok");
    expect(res.json.features).toContain("symlink-v1");
    expect(res.json.features).not.toContain("file-stream-v1");
  });

  it("authenticates before routing to an actor", async () => {
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree`)).status).toBe(401);
    const other = await api(gw, "GET", `/v1/workspaces/ws_other/fs/tree`, { token: t });
    expect(other.status).toBe(403);
    expect(other.json.message).toBe("workspace mismatch");
    expect(gw.actors.runtimes.has("ws_other")).toBe(false);
    const noCorrelation = await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree`, { token: t, correlation: false });
    expect(noCorrelation.status).toBe(400);
    const forged = t.slice(0, -4) + "AAAA";
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree`, { token: forged })).status).toBe(401);
  });

  it("writes, reads and conflicts with Go wire semantics", async () => {
    const path = `/v1/workspaces/${WS}/fs/file?path=/notes/a.md`;
    expect((await api(gw, "PUT", path, { token: t, body: { content: "x" } })).status).toBe(412);
    expect((await api(gw, "PUT", path, { token: t, body: { content: "x" }, headers: { "if-match": "rev_9" } })).status).toBe(404);

    const created = await api(gw, "PUT", path, { token: t, body: { content: "hello" }, headers: { "if-match": "0" } });
    expect(created.status).toBe(202);
    expect(created.json).toMatchObject({ status: "queued", targetRevision: "rev_1", writeback: { provider: "notes", state: "succeeded" } });
    expect(created.json.opId).toMatch(/^op_\d+$/);

    const read = await api(gw, "GET", path, { token: t });
    expect(read.status).toBe(200);
    expect(read.headers.get("etag")).toBe("rev_1");
    expect(read.json).toMatchObject({ path: "/notes/a.md", revision: "rev_1", content: "hello", contentType: "text/markdown", contentHash: sha("hello"), semantics: {} });
    expect(read.json.encoding).toBeUndefined();

    const stale = await api(gw, "PUT", path, { token: t, body: { content: "again" }, headers: { "if-match": "0" } });
    expect(stale.status).toBe(409);
    expect(stale.json).toMatchObject({ code: "revision_conflict", expectedRevision: "0", currentRevision: "rev_1", currentContentPreview: "hello", correlationId: "corr-test" });

    const updated = await api(gw, "PUT", path, { token: t, body: { content: "v2" }, headers: { "if-match": '"rev_1"' } });
    expect(updated.status).toBe(202);
    const op = await api(gw, "GET", `/v1/workspaces/${WS}/ops/${updated.json.opId}`, { token: t });
    expect(op.json).toMatchObject({ status: "succeeded", action: "file_upsert", providerResult: { providerRevision: updated.json.targetRevision } });

    const deleted = await api(gw, "DELETE", path, { token: t, headers: { "if-match": "*" } });
    expect(deleted.status).toBe(202);
    expect((await api(gw, "GET", path, { token: t })).status).toBe(404);
  });

  it("serves tree, events and export for the mount", async () => {
    await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, {
      token: t,
      body: {
        files: [
          { path: "/a/one.md", content: "1", ifMatch: "0" },
          { path: "/a/b/two.md", content: "Mg==", encoding: "base64" },
          { path: "/link", type: "symlink", target: "/a/one.md", content: "/a/one.md" },
          { path: "/bad", content: "%%%", encoding: "base64" },
        ],
      },
    });
    const tree = await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree?path=/&depth=3`, { token: t });
    expect(tree.status).toBe(200);
    expect(tree.json.totalFiles).toBe(3);
    expect(tree.json.nextCursor).toBeNull();
    expect(tree.json.entries.map((e: { path: string; type: string }) => `${e.type}:${e.path}`)).toEqual([
      "dir:/a",
      "dir:/a/b",
      "file:/a/b/two.md",
      "file:/a/one.md",
      "symlink:/link",
    ]);
    const shallow = await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree?path=/&depth=1`, { token: t });
    expect(shallow.json.entries.map((e: { path: string }) => e.path)).toEqual(["/a", "/link"]);
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree?cursor=/nope`, { token: t })).json.code).toBe("invalid_cursor");

    const events = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?limit=2`, { token: t });
    expect(events.json.events.map((e: { eventId: string }) => e.eventId)).toEqual(["evt_1", "evt_2"]);
    expect(events.json.nextCursor).toBe("evt_2");
    const rest = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?cursor=evt_2`, { token: t });
    expect(rest.json.events.map((e: { eventId: string }) => e.eventId)).toEqual(["evt_3"]);
    expect(rest.json.events[0].typeMetadata).toEqual({ type: "symlink", target: "/a/one.md" });
    expect(rest.json.nextCursor).toBeNull();
    const latest = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?direction=desc&limit=1`, { token: t });
    expect(latest.json.events[0].eventId).toBe("evt_3");

    const exported = await api(gw, "GET", `/v1/workspaces/${WS}/fs/export?format=json&path=/a`, { token: t });
    expect(exported.json.map((f: { path: string }) => f.path)).toEqual(["/a/b/two.md", "/a/one.md"]);
    expect(exported.json[0]).toMatchObject({ encoding: "base64", content: "Mg==", contentHash: sha("2") });
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/export?format=tar`, { token: t })).status).toBe(404);
    expect((await api(gw, "POST", `/v1/workspaces/${WS}/fs/merge?path=/a/one.md`, { token: t, body: {} })).status).toBe(404);
  });

  it("reports per-file bulk errors and reads in bulk", async () => {
    const res = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, {
      token: t,
      body: { files: [{ path: "/x.md", content: "x", ifMatch: "0" }, { path: "/x.md", content: "y", ifMatch: "0" }, { path: "/bad", content: "%%", encoding: "base64" }] },
    });
    expect(res.status).toBe(202);
    expect(res.json.written).toBe(1);
    expect(res.json.errors).toEqual([
      expect.objectContaining({ path: "/x.md", code: "conflict", expectedRevision: "0", currentRevision: "rev_1" }),
      expect.objectContaining({ path: "/bad", code: "invalid_content" }),
    ]);
    const read = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk-read`, { token: t, body: { paths: ["/x.md", "/missing.md"] } });
    expect(read.json.files[0]).toMatchObject({ path: "/x.md", content: "x", revision: "rev_1" });
    expect(read.json.files[1]).toEqual({ path: "/missing.md", error: { status: 404, code: "not_found", message: "file not found" } });
    expect((await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk-read`, { token: t, body: { paths: ["/x.md"], extra: 1 } })).status).toBe(400);
  });

  it("keeps large file bytes out of the actor and hydrates them on read", async () => {
    const big = "z".repeat(5000);
    const put = await api(gw, "PUT", `/v1/workspaces/${WS}/fs/file?path=/big.txt`, { token: t, body: { content: big, contentType: "text/plain" }, headers: { "if-match": "0" } });
    expect(put.status).toBe(202);
    const key = `relayfile/blobs/sha256/${sha(big).slice(0, 2)}/${sha(big)}`;
    expect(Buffer.from(gw.blobs.objects.get(key)!).toString()).toBe(big);
    const row = gw.actors.dbs.get(WS)!.db.prepare("SELECT content, blob_sha256, size FROM rf_files WHERE path = '/big.txt'").get() as Record<string, unknown>;
    expect(row).toEqual({ content: null, blob_sha256: sha(big), size: 5000 });
    const read = await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/big.txt`, { token: t });
    expect(read.json.content).toBe(big);
    expect(read.json.contentHash).toBe(sha(big));
    expect(read.json.contentBlob).toBeUndefined();
    const bulk = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk-read`, { token: t, body: { paths: ["/big.txt"] } });
    expect(bulk.json.files[0].content).toBe(big);

    // Streaming PUT (raw bytes) also lands in object storage.
    const raw = Buffer.alloc(4096, 7);
    const stream = await fetch(`${gw.url}/v1/workspaces/${WS}/fs/file?path=/raw.bin`, {
      method: "PUT",
      headers: {
        authorization: `Bearer ${t}`,
        "x-correlation-id": "c",
        "content-type": "application/octet-stream",
        "x-relayfile-encoding": "base64",
        "x-relayfile-mode": "644",
      },
      body: raw,
    });
    expect(stream.status).toBe(202);
    const rawRead = await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/raw.bin`, { token: t });
    expect(Buffer.from(rawRead.json.content, "base64").equals(raw)).toBe(true);
    expect(rawRead.json.mode).toBe(0o644);
  });

  it("enforces path-scoped tokens and directory ACLs", async () => {
    await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, {
      token: t,
      body: { files: [{ path: "/team/a.md", content: "a" }, { path: "/secret/s.md", content: "s" }, { path: "/secret/.relayfile.acl", content: "", semantics: { permissions: ["deny:agent:agent-2"] } }] },
    });
    const scoped = token(WS, ["relayfile:fs:read:/team/**"], "agent-3");
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/team/a.md`, { token: scoped })).status).toBe(200);
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/secret/s.md`, { token: scoped })).status).toBe(403);
    const tree = await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree?path=/team`, { token: scoped });
    expect(tree.json.entries.map((e: { path: string }) => e.path)).toEqual(["/team/a.md"]);

    const denied = token(WS, ["fs:read"], "agent-2");
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/secret/s.md`, { token: denied })).status).toBe(403);
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/team/a.md`, { token: denied })).status).toBe(200);
    expect((await api(gw, "PUT", `/v1/workspaces/${WS}/fs/file?path=/team/a.md`, { token: denied, body: { content: "b" }, headers: { "if-match": "*" } })).status).toBe(403);
  });

  it("ingests provider webhooks and the HMAC internal envelope path", async () => {
    const ingest = await api(gw, "POST", `/v1/workspaces/${WS}/webhooks/ingest`, {
      token: t,
      body: { provider: "notion", event_type: "file.created", path: "/notion/pages/p1.md", delivery_id: "d1", data: { content: "# Page", contentType: "text/markdown" } },
    });
    expect(ingest.status).toBe(202);
    expect(ingest.json).toMatchObject({ status: "queued" });
    const again = await api(gw, "POST", `/v1/workspaces/${WS}/webhooks/ingest`, {
      token: t,
      body: { provider: "notion", event_type: "file.created", path: "/notion/pages/p1.md", delivery_id: "d1", data: { content: "# Page" } },
    });
    expect(again.json.id).toBe(ingest.json.id);
    const file = await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/notion/pages/p1.md`, { token: t });
    expect(file.json).toMatchObject({ content: "# Page", provider: "notion", contentHash: sha("# Page") });

    const body = JSON.stringify({ envelopeId: "env_x", workspaceId: WS, provider: "linear", deliveryId: "L1", receivedAt: new Date().toISOString(), payload: { event_type: "file.updated", path: "/linear/issues/A-1.json", data: { content: "{}" } }, correlationId: "c1" });
    const ts = new Date().toISOString();
    const sig = createHmac("sha256", HMAC_SECRET).update(`${ts}\n${body}`).digest("hex");
    const signed = (signature: string) =>
      fetch(`${gw.url}/v1/internal/webhook-envelopes`, { method: "POST", headers: { "x-correlation-id": "c1", "x-relay-timestamp": ts, "x-relay-signature": signature, "content-type": "application/json" }, body });
    expect((await signed("00".repeat(32))).status).toBe(401);
    expect((await signed(sig)).status).toBe(202);
    expect((await signed(sig)).status).toBe(401); // replay
    const linear = await api(gw, "GET", `/v1/workspaces/${WS}/fs/file?path=/linear/issues/A-1.json`, { token: t });
    expect(linear.status).toBe(200);
  });
});

describe("external writeback workers", () => {
  let gw: TestGateway;
  beforeEach(async () => {
    gw = await startGateway({ externalWritebackProviders: ["github"] });
  });
  afterEach(async () => {
    await gw.close();
  });

  it("keeps provider writebacks pending until a worker acks them", async () => {
    const t = token(WS);
    const put = await api(gw, "PUT", `/v1/workspaces/${WS}/fs/file?path=/github/repos/o/r/issues/1/comments/new.json`, { token: t, body: { content: "{}" }, headers: { "if-match": "0" } });
    expect(put.json.writeback).toEqual({ provider: "github", state: "pending" });
    const local = await api(gw, "PUT", `/v1/workspaces/${WS}/fs/file?path=/scratch/n.md`, { token: t, body: { content: "n" }, headers: { "if-match": "0" } });
    expect(local.json.writeback.state).toBe("succeeded");

    const pending = await api(gw, "GET", `/v1/workspaces/${WS}/writeback/pending`, { token: t });
    expect(pending.json).toEqual([{ id: put.json.opId, workspaceId: WS, path: "/github/repos/o/r/issues/1/comments/new.json", revision: put.json.targetRevision, correlationId: "corr-test" }]);
    expect((await api(gw, "POST", `/v1/workspaces/${WS}/writeback/${put.json.opId}/ack`, { token: t, body: {} })).status).toBe(400);
    const ack = await api(gw, "POST", `/v1/workspaces/${WS}/writeback/${put.json.opId}/ack`, { token: t, body: { success: true, externalId: "123" } });
    expect(ack.json).toMatchObject({ status: "acknowledged", id: put.json.opId, success: true });
    const op = await api(gw, "GET", `/v1/workspaces/${WS}/ops/${put.json.opId}`, { token: t });
    expect(op.json).toMatchObject({ status: "succeeded", providerResult: { externalId: "123", providerRevision: put.json.targetRevision } });
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/writeback/pending`, { token: t })).json).toEqual([]);
    const events = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?direction=desc&limit=1`, { token: t });
    expect(events.json.events[0].type).toBe("writeback.succeeded");
    const ops = await api(gw, "GET", `/v1/workspaces/${WS}/ops?limit=1`, { token: t });
    expect(ops.json.items[0].opId).toBe(local.json.opId); // newest first
  });
});

describe("inline mode (no blob store)", () => {
  it("rejects files above the inline limit instead of bloating actor state", async () => {
    const gw = await startGateway({ blobs: false });
    try {
      const t = token(WS);
      const big = "q".repeat(10 * 1024 * 1024 + 1);
      const res = await api(gw, "PUT", `/v1/workspaces/${WS}/fs/file?path=/huge`, { token: t, body: { content: big }, headers: { "if-match": "0" } });
      expect(res.status).toBe(413);
    } finally {
      await gw.close();
    }
  });
});
