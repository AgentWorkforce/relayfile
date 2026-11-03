import { createHash } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { WorkspaceRuntime, type SocketHandle } from "../src/runtime/workspace-runtime.js";
import type { ActorHttpRequest, CallerContext } from "../src/runtime/types.js";
import { FakeActorDb } from "./fake-actor-db.js";
import { api, startGateway, token, type TestGateway } from "./harness.js";

const WS = "ws_sec";
const sha = (value: string | Buffer) => createHash("sha256").update(value).digest("hex");
const file = (path: string) => `/v1/workspaces/${WS}/fs/file?path=${encodeURIComponent(path)}`;

describe("ACL parity with the Go server", () => {
  let gw: TestGateway;
  const owner = token(WS);
  const denied = token(WS, ["fs:read", "fs:write", "sync:trigger"], "agent-2");
  beforeEach(async () => {
    gw = await startGateway();
    const seeded = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, {
      token: owner,
      body: {
        files: [
          { path: "/team/hidden.md", content: "secret body", semantics: { permissions: ["deny:agent:agent-2", "public"] } },
          { path: "/team/open.md", content: "open" },
          { path: "/top.md", content: "top" },
        ],
      },
    });
    expect(seeded.json.written).toBe(3);
  });
  afterEach(async () => {
    await gw.close();
  });

  it("judges an existing file by its own rules even with If-Match: *", async () => {
    const forced = await api(gw, "PUT", file("/team/hidden.md"), { token: denied, body: { content: "pwned" }, headers: { "if-match": "*" } });
    expect(forced.status).toBe(403);
    const bulk = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: denied, body: { files: [{ path: "/team/hidden.md", content: "pwned", ifMatch: "*" }, { path: "/team/hidden.md", content: "pwned" }] } });
    expect(bulk.json.errors.map((e: { code: string }) => e.code)).toEqual(["forbidden", "forbidden"]);
    expect((await api(gw, "GET", file("/team/hidden.md"), { token: owner })).json.content).toBe("secret body");
  });

  it("checks permissions before contentIdentity dedup hits, and only answers hits for the same path", async () => {
    const identity = { kind: "webhook", key: "k1" };
    const first = await api(gw, "PUT", file("/team/open.md"), { token: owner, body: { content: "v2", contentIdentity: identity }, headers: { "if-match": "*" } });
    expect(first.status).toBe(202);
    const replay = await api(gw, "PUT", file("/team/hidden.md"), { token: denied, body: { content: "x", contentIdentity: identity }, headers: { "if-match": "*" } });
    expect(replay.status).toBe(403);
    const other = await api(gw, "PUT", file("/top.md"), { token: owner, body: { content: "t2", contentIdentity: identity }, headers: { "if-match": "*" } });
    expect(other.json.opId).not.toBe(first.json.opId);
    expect((await api(gw, "GET", file("/top.md"), { token: owner })).json.content).toBe("t2");
    const retry = await api(gw, "PUT", file("/top.md"), { token: owner, body: { content: "t2", contentIdentity: identity }, headers: { "if-match": "*" } });
    expect(retry.json.opId).toBe(other.json.opId);
  });

  it("does not leak content through conflicts", async () => {
    const deniedConflict = await api(gw, "PUT", file("/team/hidden.md"), { token: denied, body: { content: "x" }, headers: { "if-match": "rev_99" } });
    expect(deniedConflict.status).toBe(403);
    const writeOnly = token(WS, ["fs:write"], "writer");
    const conflict = await api(gw, "PUT", file("/top.md"), { token: writeOnly, body: { content: "x" }, headers: { "if-match": "rev_99" } });
    expect(conflict.status).toBe(409);
    expect(conflict.json.currentContentPreview).toBeUndefined();
    const reader = await api(gw, "PUT", file("/top.md"), { token: owner, body: { content: "x" }, headers: { "if-match": "rev_99" } });
    expect(reader.json.currentContentPreview).toBe("top");
  });

  it("scope-checks the resolved path, not the raw one", async () => {
    const scoped = token(WS, ["relayfile:fs:read:/team/**", "relayfile:fs:write:/team/**"], "agent-3");
    expect((await api(gw, "GET", file("/team/../top.md"), { token: scoped })).status).toBe(403);
    expect((await api(gw, "GET", file("/team/./open.md"), { token: scoped })).status).toBe(200);
    expect((await api(gw, "PUT", file("/team/../top.md"), { token: scoped, body: { content: "x" }, headers: { "if-match": "*" } })).status).toBe(403);
    expect((await api(gw, "DELETE", file("/team/../top.md"), { token: scoped, headers: { "if-match": "*" } })).status).toBe(403);
    const bulkRead = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk-read`, { token: scoped, body: { paths: ["/team/../top.md"] } });
    expect(bulkRead.status).toBe(403);
    const bulk = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: scoped, body: { files: [{ path: "/team/../top.md", content: "x" }] } });
    expect(bulk.json.errors[0].code).toBe("forbidden");
    expect((await api(gw, "GET", file("/top.md"), { token: owner })).json.content).toBe("top");
  });

  it("accepts path-scoped bulk writes and checks each file", async () => {
    for (const scope of ["relayfile:fs:write:/team/**", "workspace:agent-3:write:/team/**"]) {
      const scoped = token(WS, [scope], "agent-3");
      const res = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: scoped, body: { files: [{ path: "/team/new.md", content: "n" }, { path: "/top.md", content: "x" }] } });
      expect(res.status).toBe(202);
      expect(res.json.written).toBe(1);
      expect(res.json.errors).toEqual([expect.objectContaining({ path: "/top.md", code: "forbidden" })]);
    }
    const none = token(WS, ["fs:read"], "agent-3");
    expect((await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: none, body: { files: [{ path: "/team/new.md", content: "n" }] } })).status).toBe(403);
  });

  it("keeps a protected file's events hidden after it is deleted", async () => {
    expect((await api(gw, "DELETE", file("/team/hidden.md"), { token: owner, headers: { "if-match": "*" } })).status).toBe(202);
    const paths = async (t: string) =>
      (await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?limit=100`, { token: t })).json.events.map((e: { type: string; path: string }) => `${e.type}:${e.path}`);
    expect(await paths(owner)).toContain("file.deleted:/team/hidden.md");
    expect(await paths(owner)).toContain("file.created:/team/hidden.md");
    const seen = await paths(denied);
    expect(seen).toContain("file.created:/team/open.md");
    expect(seen.filter((p: string) => p.endsWith("/team/hidden.md"))).toEqual([]);
  });

  it("serves the event feed to path-restricted readers, filtered to their paths", async () => {
    const scoped = token(WS, ["relayfile:fs:read:/team/**"], "agent-3");
    const res = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events`, { token: scoped });
    expect(res.status).toBe(200);
    expect(res.json.events.map((e: { path: string }) => e.path)).toEqual(["/team/hidden.md", "/team/open.md"]);
    expect((await api(gw, "GET", `/v1/workspaces/${WS}/fs/events`, { token: token(WS, ["fs:write"], "w") })).status).toBe(403);
  });

  it("matches filesystem scope rules semantically, like Go aclScopeRuleMatches", async () => {
    await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, {
      token: owner,
      body: { files: [{ path: "/team/ruled.md", content: "r", semantics: { permissions: ["scope:relayfile:fs:read:/team/**", "scope:relayfile:fs:write:/team/**"] } }] },
    });
    const broad = token(WS, ["relayfile:fs:read:*"], "agent-4");
    expect((await api(gw, "GET", file("/team/ruled.md"), { token: broad })).status).toBe(200);
    const elsewhere = token(WS, ["relayfile:fs:read:/other/**"], "agent-5");
    expect((await api(gw, "GET", file("/team/ruled.md"), { token: elsewhere })).status).toBe(403);
    // A deleted file's events stay visible to callers its rule admitted.
    expect((await api(gw, "DELETE", file("/team/ruled.md"), { token: owner, headers: { "if-match": "*" } })).status).toBe(202);
    const events = await api(gw, "GET", `/v1/workspaces/${WS}/fs/events?limit=100`, { token: broad });
    expect(events.json.events.map((e: { type: string; path: string }) => `${e.type}:${e.path}`)).toContain("file.deleted:/team/ruled.md");
  });

  it("gates bearer webhook ingestion by the caller's file permissions", async () => {
    const update = await api(gw, "POST", `/v1/workspaces/${WS}/webhooks/ingest`, {
      token: denied,
      body: { provider: "team", event_type: "file.updated", path: "/team/hidden.md", delivery_id: "w1", data: { content: "pwned" } },
    });
    expect(update.status).toBe(202);
    const remove = await api(gw, "POST", `/v1/workspaces/${WS}/webhooks/ingest`, {
      token: denied,
      body: { provider: "team", event_type: "file.deleted", path: "/team/hidden.md", delivery_id: "w2" },
    });
    expect(remove.status).toBe(202);
    const after = await api(gw, "GET", file("/team/hidden.md"), { token: owner });
    expect(after.json.content).toBe("secret body");
    const status = await api(gw, "GET", `/v1/workspaces/${WS}/sync/status?provider=team`, { token: owner });
    expect(status.json.providers[0].deadLetteredEnvelopes).toBe(2);

    const allowed = await api(gw, "POST", `/v1/workspaces/${WS}/webhooks/ingest`, {
      token: denied,
      body: { provider: "team", event_type: "file.updated", path: "/team/open.md", delivery_id: "w3", data: { content: "synced" } },
    });
    expect(allowed.status).toBe(202);
    expect((await api(gw, "GET", file("/team/open.md"), { token: owner })).json.content).toBe("synced");
  });
});

describe("gateway byte handling", () => {
  let gw: TestGateway;
  const t = token(WS);
  beforeEach(async () => {
    gw = await startGateway({ inlineMaxBytes: 1024 });
  });
  afterEach(async () => {
    await gw.close();
  });

  const rawPut = (path: string, bytes: Buffer, headers: Record<string, string> = {}) =>
    fetch(`${gw.url}${file(path)}`, {
      method: "PUT",
      headers: { authorization: `Bearer ${t}`, "x-correlation-id": "c", "content-type": "application/octet-stream", ...headers },
      body: bytes,
    });

  it("ignores client-supplied blob references", async () => {
    const big = "s".repeat(5000);
    expect((await api(gw, "PUT", file("/other/secret.txt"), { token: t, body: { content: big }, headers: { "if-match": "0" } })).status).toBe(202);
    const forged = { content: "", contentBlob: { sha256: sha(big), size: 5000 }, contentHash: sha(big) };
    expect((await api(gw, "PUT", file("/mine.txt"), { token: t, body: forged, headers: { "if-match": "0" } })).status).toBe(202);
    const read = await api(gw, "GET", file("/mine.txt"), { token: t });
    expect(read.json.content).toBe("");
    expect(read.json.contentHash).toBe(sha(""));
    await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: t, body: { files: [{ path: "/mine2.txt", ...forged }, { path: "/mine3.txt", contentBlob: forged.contentBlob }] } });
    expect((await api(gw, "GET", file("/mine2.txt"), { token: t })).json.content).toBe("");
    expect((await api(gw, "GET", file("/mine3.txt"), { token: t })).json.content).toBe("");
  });

  it("uploads nothing for callers that may not write the target", async () => {
    const reader = token(WS, ["fs:read"], "reader");
    const big = "r".repeat(5000);
    const res = await api(gw, "PUT", file("/x.txt"), { token: reader, body: { content: big }, headers: { "if-match": "0" } });
    expect(res.status).toBe(403);
    const raw = await fetch(`${gw.url}${file("/y.bin")}`, {
      method: "PUT",
      headers: { authorization: `Bearer ${reader}`, "x-correlation-id": "c", "content-type": "application/octet-stream", "if-match": "0" },
      body: Buffer.alloc(5000, 1),
    });
    expect(raw.status).toBe(403);
    const scoped = token(WS, ["relayfile:fs:write:/team/**"], "w");
    await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: scoped, body: { files: [{ path: "/other/z.txt", content: big }] } });
    expect(gw.blobs.objects.size).toBe(0);
  });

  it("rejects malformed content the same way for small and large files", async () => {
    for (const content of ["A", "QQ=", "QQ= ", "Q Q==", "A".repeat(4001)]) {
      const res = await api(gw, "PUT", file("/b.bin"), { token: t, body: { content, encoding: "base64" }, headers: { "if-match": "*" } });
      expect([content, res.status, res.json.code]).toEqual([content, 400, "bad_request"]);
    }
    expect(gw.blobs.objects.size).toBe(0);
    expect((await api(gw, "PUT", file("/b.bin"), { token: t, body: { content: "QQ" , encoding: "base64" }, headers: { "if-match": "*" } })).status).toBe(202);
    expect((await api(gw, "PUT", file("/n.txt"), { token: t, body: { content: 123 }, headers: { "if-match": "*" } })).status).toBe(400);
    const bulk = await api(gw, "POST", `/v1/workspaces/${WS}/fs/bulk`, { token: t, body: { files: [{ path: "/n2.txt", content: { a: 1 } }] } });
    expect(bulk.json.errors[0].code).toBe("invalid_input");
    expect((await api(gw, "GET", file("/n.txt"), { token: t })).status).toBe(404);
  });

  it("reports byte sizes like the Go server", async () => {
    await api(gw, "PUT", file("/u.md"), { token: t, body: { content: "héllo" }, headers: { "if-match": "0" } });
    const tree = await api(gw, "GET", `/v1/workspaces/${WS}/fs/tree?path=/`, { token: t });
    expect(tree.json.entries.find((e: { path: string }) => e.path === "/u.md").size).toBe(Buffer.byteLength("héllo"));
  });

  it("normalizes the encoding like the actor before offloading", async () => {
    const bytes = Buffer.alloc(3000, 9);
    const b64 = bytes.toString("base64");
    for (const encoding of ["BASE64", " base64 "]) {
      const path = `/enc-${encoding.trim()}-${encoding.length}.bin`;
      expect((await api(gw, "PUT", file(path), { token: t, body: { content: b64, encoding }, headers: { "if-match": "0" } })).status).toBe(202);
      const read = await api(gw, "GET", file(path), { token: t });
      expect(read.json.encoding).toBe("base64");
      expect(read.json.content).toBe(b64);
      expect(read.json.contentHash).toBe(sha(bytes));
    }
  });

  it("stores raw non-UTF-8 bytes losslessly", async () => {
    const bytes = Buffer.from([0xff, 0xfe, 0x00, 0x41, 0xc3]);
    expect((await rawPut("/bin.dat", bytes, { "if-match": "0" })).status).toBe(202);
    const read = await api(gw, "GET", file("/bin.dat"), { token: t });
    expect(read.json.encoding).toBe("base64");
    expect(Buffer.from(read.json.content, "base64").equals(bytes)).toBe(true);
    expect(read.json.contentHash).toBe(sha(bytes));
    const text = Buffer.from("héllo", "utf8");
    expect((await rawPut("/text.txt", text, { "if-match": "0" })).status).toBe(202);
    const textRead = await api(gw, "GET", file("/text.txt"), { token: t });
    expect(textRead.json.encoding).toBeUndefined();
    expect(textRead.json.content).toBe("héllo");
  });

  it("converts zero-byte streaming writes and keeps the If-Match precondition", async () => {
    expect((await rawPut("/nomatch.txt", Buffer.from("x"))).status).toBe(412);
    expect((await rawPut("/empty.txt", Buffer.alloc(0))).status).toBe(412);
    const res = await rawPut("/empty.txt", Buffer.alloc(0), { "if-match": "0", "x-relayfile-mode": "600", "x-relayfile-content-type": "text/plain" });
    expect(res.status).toBe(202);
    const read = await api(gw, "GET", file("/empty.txt"), { token: t });
    expect(read.json).toMatchObject({ content: "", mode: 0o600, contentType: "text/plain", contentHash: sha("") });
  });
});

describe("workspace runtime", () => {
  const caller: CallerContext = { agentName: "agent-1", scopes: ["fs:read", "fs:write", "sync:read"], correlationId: "c", externalWritebackProviders: ["github"] };
  const bulk = (count: number, prefix: string): ActorHttpRequest => ({
    method: "POST",
    path: "/fs/bulk",
    query: [],
    headers: {},
    body: JSON.stringify({ files: Array.from({ length: count }, (_, i) => ({ path: `${prefix}/f${i}.md`, content: String(i) })) }),
  });

  it("re-migrates after a call that rolled back its migration", async () => {
    let fail = true;
    class FailingDb extends FakeActorDb {
      override exec<Row extends object>(sql: string, ...bindings: Parameters<FakeActorDb["exec"]>[1][]): Row[] {
        if (fail && /^\s*INSERT INTO rf_events/.test(sql)) throw new Error("disk I/O error");
        return super.exec<Row>(sql, ...bindings);
      }
    }
    const db = new FailingDb();
    const runtime = new WorkspaceRuntime(db, "ws");
    await expect(db.transaction(() => runtime.handle(bulk(1, "/a"), caller))).rejects.toThrow("disk I/O error");
    fail = false;
    const res = await db.transaction(() => runtime.handle(bulk(1, "/a"), caller));
    expect(res.status).toBe(202);
    expect(JSON.parse(res.body).written).toBe(1);
  });

  it("answers malformed bulk entries per file instead of failing the call", async () => {
    const runtime = new WorkspaceRuntime(new FakeActorDb(), "ws");
    const res = await runtime.handle({ method: "POST", path: "/fs/bulk", query: [], headers: {}, body: '{"files":[null,{"path":"/ok.md","content":"x"}]}' }, caller);
    expect(res.status).toBe(202);
    expect(JSON.parse(res.body)).toMatchObject({ written: 1, errorCount: 1 });
  });

  it("fans every event of a large bulk write out to sockets and lists every pending writeback", async () => {
    const frames: Array<Record<string, unknown>> = [];
    const socket: SocketHandle = { id: "s1", metadata: { agentName: "agent-1", scopes: ["fs:read"], cursor: null, fromNow: true, paths: [] }, send: (m) => frames.push(m) };
    const db = new FakeActorDb();
    const runtime = new WorkspaceRuntime(db, "ws", { sockets: { broadcast() {}, connections: async () => [socket] } });
    const res = await db.transaction(() => runtime.handle(bulk(1205, "/github/repos/o/r/issues"), caller));
    expect(JSON.parse(res.body).written).toBe(1205);
    expect(frames).toHaveLength(0); // nothing is pushed before the transaction commits
    await runtime.flush();
    expect(frames.map((frame) => frame.path)).toEqual(Array.from({ length: 1205 }, (_, i) => `/github/repos/o/r/issues/f${i}.md`));
    const pending = await runtime.handle({ method: "GET", path: "/writeback/pending", query: [], headers: {}, body: null }, caller);
    expect(JSON.parse(pending.body)).toHaveLength(1205);
  });
});

describe("fan-out and envelope delivery", () => {
  const caller: CallerContext = { agentName: "a", scopes: ["fs:read", "fs:write"], correlationId: "c" };
  it("survives a failing socket without rolling the write back", async () => {
    const good: Array<Record<string, unknown>> = [];
    const meta = { agentName: "a", scopes: ["fs:read"], cursor: null, fromNow: true, paths: [] };
    const sockets: SocketHandle[] = [
      { id: "bad", metadata: meta, send: () => { throw new Error("closed"); } },
      { id: "good", metadata: meta, send: (m) => good.push(m) },
    ];
    const db = new FakeActorDb();
    const runtime = new WorkspaceRuntime(db, "ws", { sockets: { broadcast() {}, connections: async () => sockets } });
    const put: ActorHttpRequest = { method: "PUT", path: "/fs/file", query: [["path", "/a.md"]], headers: { "if-match": "0" }, body: '{"content":"x"}' };
    expect((await db.transaction(() => runtime.handle(put, caller))).status).toBe(202);
    await runtime.flush();
    expect(good).toHaveLength(1);
    const read = await runtime.handle({ method: "GET", path: "/fs/file", query: [["path", "/a.md"]], headers: {}, body: null }, caller);
    expect(read.status).toBe(200);
  });

  it("applies a signed internal envelope without deliveryId once, keyed on its signature", async () => {
    const runtime = new WorkspaceRuntime(new FakeActorDb(), "ws");
    const internal: CallerContext = { agentName: "relayfile-internal", scopes: [], correlationId: "c", internal: true };
    const body = JSON.stringify({ workspaceId: "ws", provider: "linear", payload: { event_type: "file.updated", path: "/linear/issues/A-1.json", data: { content: "{}" } } });
    const send = () => runtime.handle({ method: "POST", path: "/internal/webhook-envelopes", query: [], headers: { "x-relay-signature": "ab".repeat(32) }, body }, internal);
    const first = JSON.parse((await send()).body);
    const second = JSON.parse((await send()).body);
    expect(second.id).toBe(first.id);
    const events = await runtime.handle({ method: "GET", path: "/fs/events", query: [], headers: {}, body: null }, caller);
    expect(JSON.parse(events.body).events.filter((e: { type: string }) => e.type === "file.updated")).toHaveLength(1);
  });
});
