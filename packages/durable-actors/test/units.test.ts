import { createHash, generateKeyPairSync, sign } from "node:crypto";
import type { AddressInfo } from "node:net";
import { describe, expect, it } from "vitest";
import { artifactObject, artifactUuid, bundleSha256, deploymentBody, parseSecretRefs } from "../src/deploy/plan.js";
import { AuthError, BearerVerifier, mintHs256, verifyInternalHmac } from "../src/gateway/auth.js";
import { createBroker } from "../src/gateway/broker.js";
import { signV4 } from "../src/gateway/blobs.js";
import { loadGatewayConfig } from "../src/gateway/config.js";
import { pathFilterMatches } from "../src/runtime/workspace-runtime.js";
import { goValidPermissionRules, scopeMatches, scopeMatchesPath, scopePathValid } from "../src/runtime/scopes.js";
import { FakeActorDb } from "./fake-actor-db.js";
import { migrate, SCHEMA_VERSION } from "../src/runtime/schema.js";

describe("deploy plan", () => {
  it("derives a stable v4-shaped artifact id and the registerDeployment body", () => {
    const bytes = new TextEncoder().encode("export const actors = {}");
    const uuid = artifactUuid(bytes);
    expect(uuid).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(artifactUuid(bytes)).toBe(uuid);
    expect(artifactUuid(new TextEncoder().encode("other"))).not.toBe(uuid);
    const object = artifactObject(uuid);
    expect(object).toBe(`durable-actors/v3/artifacts/${uuid}/actors.mjs`);
    const body = deploymentBody({ organizationId: "relayfile", bucket: "b", object, generation: 7, bytes, contract: { version: 1 }, secretRefs: [] });
    expect(body).toEqual({
      organizationId: "relayfile",
      bundle: { bucket: "b", files: [{ path: "actors.mjs", object, generation: 7, sha256: createHash("sha256").update(bytes).digest("base64url") }] },
      contract: { version: 1 },
      secretRefs: [],
    });
    expect(bundleSha256(bytes)).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(() => deploymentBody({ organizationId: "x", bucket: "b", object, generation: 0, bytes, contract: {}, secretRefs: [] })).toThrow();
    expect(parseSecretRefs(" a, b ,,")).toEqual(["a", "b"]);
  });
});

describe("S3 SigV4", () => {
  it("matches the AWS documented GET Object example signature", () => {
    // https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html (GET Object, no Range header)
    const headers = signV4({
      method: "GET",
      url: new URL("https://examplebucket.s3.amazonaws.com/test.txt"),
      body: new Uint8Array(),
      region: "us-east-1",
      service: "s3",
      accessKeyId: "AKIAIOSFODNN7EXAMPLE",
      secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
      now: new Date("2013-05-24T00:00:00Z"),
    });
    expect(headers["x-amz-content-sha256"]).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(headers.authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE\/20130524\/us-east-1\/s3\/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=[0-9a-f]{64}$/,
    );
  });
});

describe("bearer verification", () => {
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk = { ...publicKey.export({ format: "jwk" }), kid: "k1", alg: "RS256", use: "sig" };
  const jwks = async () => new Response(JSON.stringify({ keys: [jwk] }), { headers: { "content-type": "application/json" } });
  const rs256 = (claims: Record<string, unknown>, kid = "k1") => {
    const h = Buffer.from(JSON.stringify({ alg: "RS256", kid })).toString("base64url");
    const p = Buffer.from(JSON.stringify(claims)).toString("base64url");
    return `${h}.${p}.${sign("RSA-SHA256", Buffer.from(`${h}.${p}`), privateKey).toString("base64url")}`;
  };
  const base = { wks: "ws1", sub: "agent", aud: ["relayfile"], scopes: "fs:read fs:write", exp: Math.floor(Date.now() / 1000) + 60 };

  it("verifies relayauth RS256 tokens (with token-class prefixes) against JWKS", async () => {
    const verifier = new BearerVerifier({ jwksUrl: "https://auth/jwks", fetch: jwks as unknown as typeof fetch });
    const principal = await verifier.verify(`Bearer relay_pa_${rs256(base)}`);
    expect(principal).toMatchObject({ workspaceId: "ws1", agentName: "agent", scopes: ["fs:read", "fs:write"] });
  });

  it("rejects bad audience, expiry, unknown kid and HS256 without a secret", async () => {
    const verifier = new BearerVerifier({ jwksUrl: "https://auth/jwks", fetch: jwks as unknown as typeof fetch });
    await expect(verifier.verifyToken(rs256({ ...base, aud: "other" }))).rejects.toMatchObject({ status: 401, message: "invalid aud claim" });
    await expect(verifier.verifyToken(rs256({ ...base, exp: 1 }))).rejects.toMatchObject({ message: "token expired" });
    await expect(verifier.verifyToken(rs256(base, "nope"))).rejects.toBeInstanceOf(AuthError);
    await expect(verifier.verifyToken(rs256({ ...base, scopes: [] }))).rejects.toMatchObject({ status: 403 });
    const hs = `${Buffer.from('{"alg":"HS256"}').toString("base64url")}.${Buffer.from(JSON.stringify(base)).toString("base64url")}.sig`;
    await expect(verifier.verifyToken(hs)).rejects.toMatchObject({ message: "unsupported jwt algorithm" });
  });

  it("fails closed on missing kid and future nbf, and rate-limits unknown-kid JWKS refreshes", async () => {
    let fetches = 0;
    const counting = (async () => {
      fetches += 1;
      return jwks();
    }) as unknown as typeof fetch;
    const verifier = new BearerVerifier({ jwksUrl: "https://auth/jwks", hs256Secret: "hs-secret", fetch: counting });
    const noKid = (() => {
      const h = Buffer.from(JSON.stringify({ alg: "RS256" })).toString("base64url");
      const p = Buffer.from(JSON.stringify(base)).toString("base64url");
      return `${h}.${p}.${sign("RSA-SHA256", Buffer.from(`${h}.${p}`), privateKey).toString("base64url")}`;
    })();
    await expect(verifier.verifyToken(noKid)).rejects.toMatchObject({ message: "missing jwt kid" });
    expect(fetches).toBe(0);
    await expect(verifier.verifyToken(rs256({ ...base, nbf: Math.floor(Date.now() / 1000) + 600 }))).rejects.toMatchObject({ message: "token not yet valid" });
    expect(fetches).toBe(1);
    await Promise.all([1, 2, 3].map((n) => expect(verifier.verifyToken(rs256(base, `nope-${n}`))).rejects.toBeInstanceOf(AuthError)));
    expect(fetches).toBe(2); // one refresh for the burst of unknown kids, then the cooldown applies
    await expect(verifier.verifyToken(mintHs256("hs-secret", { ...base, nbf: Math.floor(Date.now() / 1000) + 600 }))).rejects.toMatchObject({ message: "token not yet valid" });
  });

  it("lets concurrent requests for a rotated kid share the running refresh", async () => {
    let release!: () => void;
    let calls = 0;
    const slow = (async () => {
      calls += 1;
      if (calls === 2) await new Promise<void>((resolve) => (release = resolve));
      return jwks();
    }) as unknown as typeof fetch;
    const verifier = new BearerVerifier({ jwksUrl: "https://auth/jwks", fetch: slow });
    await verifier.verifyToken(rs256(base));
    jwk.kid = "k2"; // rotation: the JWKS now serves k2
    const burst = Promise.all([1, 2, 3].map(() => verifier.verifyToken(rs256(base, "k2"))));
    await new Promise((resolve) => setTimeout(resolve, 10));
    release();
    await expect(burst).resolves.toHaveLength(3);
    expect(calls).toBe(2);
    jwk.kid = "k1";
  });

  it("reports an unreachable JWKS as 503", async () => {
    const verifier = new BearerVerifier({ jwksUrl: "https://auth/jwks", fetch: (async () => { throw new Error("down"); }) as unknown as typeof fetch });
    await expect(verifier.verifyToken(rs256(base))).rejects.toMatchObject({ status: 503 });
  });
});

describe("ACL rule grammar (Go isValidACLRuleValue)", () => {
  it("ignores malformed rules instead of evaluating them", () => {
    expect(scopePathValid("/github/x**")).toBe(false);
    expect(scopePathValid("/team/**")).toBe(true);
    expect(scopePathValid("/a/../b")).toBe(false);
    expect(scopePathValid("/a/b*")).toBe(true);
    expect(
      goValidPermissionRules([
        "scope:relayfile:fs:read:/github/x**",
        "scope:relayfile:fs:read:/team/**",
        "scope:finance",
        "deny:agent:bad name",
        "agent:agent-1",
        "public",
        "role:finance",
      ]),
    ).toEqual(["scope:relayfile:fs:read:/team/**", "scope:finance", "agent:agent-1", "public", "role:finance"]);
  });
});

describe("internal HMAC", () => {
  it("requires an RFC3339 timestamp like the Go verifier", () => {
    const now = Date.parse("2026-11-03T10:00:00Z");
    expect(verifyInternalHmac("k", "Tue, 03 Nov 2026 10:00:00 GMT", "00", new Uint8Array(), now)?.message).toBe("invalid timestamp");
    expect(verifyInternalHmac("k", "2026-11-03T10:00:00Z", "00", new Uint8Array(), now)?.message).toBe("invalid signature");
  });
});

describe("gateway configuration", () => {
  const base = { DURABLE_ACTORS_CONTROL_PLANE_URL: "http://cp:7100", RELAYFILE_JWT_SECRET: "s" };
  it("refuses the admin key on the gateway unless explicitly a dev sidecar", () => {
    expect(() => loadGatewayConfig({ ...base, DURABLE_ACTORS_SECRET: "admin" })).toThrow(/must not hold the durable-actors admin key/);
    expect(loadGatewayConfig({ ...base, DURABLE_ACTORS_SECRET: "admin", RELAYFILE_ACTORS_ALLOW_ADMIN_KEY: "true" }).adminKey).toBe("admin");
    expect(() => loadGatewayConfig({ ...base })).toThrow(/RELAYFILE_ACTORS_BROKER_URL/);
    const config = loadGatewayConfig({ ...base, RELAYFILE_ACTORS_BROKER_URL: "http://broker:8090", RELAYFILE_ACTORS_BROKER_TOKEN: "t", RELAYFILE_EXTERNAL_WRITEBACK_PROVIDERS: "GitHub, notion" });
    expect(config.adminKey).toBeNull();
    expect(config.externalWritebackProviders).toEqual(["github", "notion"]);
    expect(() => loadGatewayConfig({ DURABLE_ACTORS_CONTROL_PLANE_URL: "http://cp", RELAYFILE_ACTORS_BROKER_URL: "http://b", RELAYFILE_ACTORS_BROKER_TOKEN: "t" })).toThrow(/JWKS/);
    expect(() => loadGatewayConfig({ ...base, RELAYFILE_ACTORS_BROKER_URL: "http://b", RELAYFILE_ACTORS_BROKER_TOKEN: "t", RELAYFILE_BLOB_STORE: "disk" })).toThrow(/gcs, s3, or inline/);
  });
});

describe("session broker", () => {
  it("issues sessions and socket grants only to the gateway token", async () => {
    const calls: Array<{ url: string; auth: string | null; body: unknown }> = [];
    const fakeFetch = (async (url: string | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      calls.push({ url: String(url), auth: headers.get("authorization"), body: init?.body ? JSON.parse(String(init.body)) : null });
      if (String(url).endsWith("/sessions")) return Response.json({ token: "sess", expiresAtMs: Date.now() + 55_000 });
      if (String(url).endsWith("/find-websocket")) return Response.json({ websocketUrl: "ws://cp/v1/socket?t=1" });
      return new Response(null, { status: 404 });
    }) as unknown as typeof fetch;
    const token = "b".repeat(40);
    expect(() => createBroker({ port: 0, controlPlaneUrl: "http://cp", projectId: "p", adminKey: token, brokerToken: token })).toThrow(/differ/);
    const server = createBroker({ port: 0, controlPlaneUrl: "http://cp", projectId: "relayfile", adminKey: "admin-key", brokerToken: token, fetch: fakeFetch });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    try {
      expect((await fetch(`${url}/v1/session`, { method: "POST", headers: { authorization: "Bearer wrong" } })).status).toBe(401);
      const session = await (await fetch(`${url}/v1/session`, { method: "POST", headers: { authorization: `Bearer ${token}` } })).json();
      expect(session).toMatchObject({ projectId: "relayfile", controlPlaneUrl: "http://cp", token: "sess" });
      expect(calls[0]).toMatchObject({ url: "http://cp/v1/projects/relayfile/sessions", auth: "Bearer admin-key" });
      const bad = await fetch(`${url}/v1/socket-grant`, { method: "POST", headers: { authorization: `Bearer ${token}` }, body: JSON.stringify({ actorId: "../x" }) });
      expect(bad.status).toBe(400);
      const grant = await (await fetch(`${url}/v1/socket-grant`, { method: "POST", headers: { authorization: `Bearer ${token}` }, body: JSON.stringify({ actorId: "ws1", metadata: { a: 1 } }) })).json();
      expect(grant.websocketUrl).toBe("ws://cp/v1/socket?t=1");
      expect(calls[1]).toMatchObject({ url: "http://cp/v1/projects/relayfile/actors/Workspace/ws1/find-websocket", body: { metadata: { a: 1 } } });
      expect((await fetch(`${url}/v1/deployment`, { method: "POST", headers: { authorization: `Bearer ${token}` } })).status).toBe(404);
    } finally {
      server.close();
    }
  });
});

describe("scopes and socket filters", () => {
  it("matches Go scope semantics", () => {
    expect(scopeMatches(new Set(["relayfile:fs:manage"]), "fs:read")).toBe(true);
    expect(scopeMatches(new Set(["relayfile:ops:read"]), "fs:read")).toBe(false);
    expect(scopeMatchesPath(new Set(["fs:read", "relayfile:fs:read:/a/**"]), "fs:read", "/b/x")).toBe(false);
    expect(scopeMatchesPath(new Set(["workspace:*:read:/a/*"]), "fs:read", "/a/x")).toBe(true);
    expect(scopeMatchesPath(new Set(["fs:read"]), "fs:read", "/anything")).toBe(true);
  });
  it("filters /fs/ws events by path pattern", () => {
    expect(pathFilterMatches([], "/x")).toBe(true);
    expect(pathFilterMatches(["/root", "/root/**"], "/root/a/b")).toBe(true);
    expect(pathFilterMatches(["/root", "/root/**"], "/root")).toBe(true);
    expect(pathFilterMatches(["/root/**"], "/other/a")).toBe(false);
    expect(pathFilterMatches(["/a/*/c"], "/a/b/c")).toBe(true);
    expect(pathFilterMatches(["/a/*/c"], "/a/b/c/d")).toBe(false);
  });
});

describe("schema migrations", () => {
  it("are idempotent", () => {
    const db = new FakeActorDb();
    migrate(db);
    migrate(db);
    expect(db.db.prepare("SELECT COUNT(*) AS n FROM rf_migrations").get()).toEqual({ n: SCHEMA_VERSION });
    expect(SCHEMA_VERSION).toBe(2);
    const columns = (db.db.prepare("PRAGMA table_info(rf_events)").all() as Array<{ name: string }>).map((c) => c.name);
    expect(columns).toContain("acl_permissions");
  });
});
