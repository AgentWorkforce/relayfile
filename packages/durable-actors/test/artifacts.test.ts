import { describe, expect, it } from "vitest";
import { defaultCredentialProvider } from "../src/aws/credentials.js";
import { signV4 } from "../src/aws/sigv4.js";
import { isPrivateHost, loadArtifactStoreConfig, uploadImmutable, type ArtifactStoreConfig } from "../src/deploy/artifacts.js";
import { S3BlobStore } from "../src/gateway/blobs.js";
import { loadGatewayConfig } from "../src/gateway/config.js";

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: Uint8Array;
  form?: URLSearchParams;
  redirect?: RequestRedirect;
}

/** A fetch stub that records requests and answers them in order. */
function stubFetch(...responses: Array<() => Response>) {
  const calls: Call[] = [];
  const impl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      url: String(input),
      method: init?.method ?? "GET",
      headers: { ...((init?.headers as Record<string, string>) ?? {}) },
      body: init?.body as Uint8Array | undefined,
      form: typeof init?.body === "string" ? new URLSearchParams(init.body) : undefined,
      redirect: init?.redirect,
    });
    const next = responses.shift();
    if (!next) throw new Error(`unexpected request ${init?.method ?? "GET"} ${String(input)}`);
    return next();
  }) as typeof fetch;
  return { calls, fetch: impl };
}

const bytes = new TextEncoder().encode("export const actors = {}");
const object = "durable-actors/v3/artifacts/0b5f2c1e-0000-4000-8000-000000000000/actors.mjs";
const credentials = async () => ({ accessKeyId: "AKIDEXAMPLE", secretAccessKey: "secret", sessionToken: "session" });
const now = () => new Date("2026-11-03T00:00:00Z");
const s3: ArtifactStoreConfig = { kind: "s3", region: "us-east-1", endpoint: "https://s3.us-east-1.amazonaws.com", forcePathStyle: false };

describe("artifact store config", () => {
  it("defaults to gcs so existing deployments are unchanged", () => {
    expect(loadArtifactStoreConfig({})).toEqual({ kind: "gcs", emulatorHost: undefined });
    expect(loadArtifactStoreConfig({ STORAGE_EMULATOR_HOST: "fake-gcs:4443" })).toEqual({ kind: "gcs", emulatorHost: "fake-gcs:4443" });
  });

  it("requires a region for s3 and falls back to AWS_REGION", () => {
    expect(() => loadArtifactStoreConfig({ DURABLE_ACTORS_ARTIFACT_STORE: "s3" })).toThrow(/DURABLE_ACTORS_S3_REGION/);
    expect(loadArtifactStoreConfig({ DURABLE_ACTORS_ARTIFACT_STORE: "s3", AWS_REGION: "eu-west-1" })).toEqual({
      kind: "s3",
      region: "eu-west-1",
      endpoint: "https://s3.eu-west-1.amazonaws.com",
      forcePathStyle: false,
    });
    expect(
      loadArtifactStoreConfig({ DURABLE_ACTORS_ARTIFACT_STORE: "S3", DURABLE_ACTORS_S3_REGION: "us-west-2", AWS_REGION: "eu-west-1" }).kind,
    ).toBe("s3");
  });

  it("accepts a MinIO endpoint with path-style addressing", () => {
    expect(
      loadArtifactStoreConfig({
        DURABLE_ACTORS_ARTIFACT_STORE: "s3",
        DURABLE_ACTORS_S3_REGION: "us-east-1",
        DURABLE_ACTORS_S3_ENDPOINT: "http://minio:9000",
        DURABLE_ACTORS_S3_FORCE_PATH_STYLE: "true",
      }),
    ).toEqual({ kind: "s3", region: "us-east-1", endpoint: "http://minio:9000", forcePathStyle: true });
  });

  it("rejects unknown stores, bad booleans and bad endpoints", () => {
    expect(() => loadArtifactStoreConfig({ DURABLE_ACTORS_ARTIFACT_STORE: "azure" })).toThrow(/gcs or s3/);
    const base = { DURABLE_ACTORS_ARTIFACT_STORE: "s3", DURABLE_ACTORS_S3_REGION: "us-east-1" };
    expect(() => loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_FORCE_PATH_STYLE: "maybe" })).toThrow(/FORCE_PATH_STYLE/);
    expect(() => loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: "ftp://x" })).toThrow(/http/);
  });

  it("allows plain http only for in-cluster or private endpoints", () => {
    const base = { DURABLE_ACTORS_ARTIFACT_STORE: "s3", DURABLE_ACTORS_S3_REGION: "us-east-1" };
    for (const endpoint of ["http://minio:9000", "http://minio.storage.svc.cluster.local:9000", "http://10.0.0.5:9000", "http://127.0.0.1:9000"]) {
      expect(loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: endpoint }).kind).toBe("s3");
    }
    expect(() => loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: "http://s3.example.com" })).toThrow(/https/);
    expect(() => loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: "http://8.8.8.8" })).toThrow(/https/);
    expect(isPrivateHost("172.20.1.1")).toBe(true);
    expect(isPrivateHost("172.32.1.1")).toBe(false);
    expect(isPrivateHost("[fd00:ec2::23]")).toBe(true);
    expect(isPrivateHost("[::1]")).toBe(true);
    expect(isPrivateHost("[2606:4700:4700::1111]")).toBe(false);
    expect(() => loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: "http://[2606:4700:4700::1111]:9000" })).toThrow(/https/);
    expect(loadArtifactStoreConfig({ ...base, DURABLE_ACTORS_S3_ENDPOINT: "https://s3.example.com" }).kind).toBe("s3");
  });
});

describe("S3 artifact upload", () => {
  it("creates the object with If-None-Match, da-generation metadata and a signed request", async () => {
    const stub = stubFetch(() => new Response(null, { status: 200 }));
    const generation = await uploadImmutable(s3, "artifacts", object, bytes, { fetch: stub.fetch, credentials, now });
    expect(generation).toBe(1);
    expect(stub.calls).toHaveLength(1);
    const [put] = stub.calls;
    expect(put!.method).toBe("PUT");
    expect(put!.url).toBe(`https://artifacts.s3.us-east-1.amazonaws.com/${object}`);
    expect(put!.body).toBe(bytes);
    expect(put!.headers).toMatchObject({
      "content-type": "text/javascript",
      "if-none-match": "*",
      "x-amz-meta-da-generation": "1",
      "x-amz-security-token": "session",
    });
    expect(put!.headers.authorization).toMatch(
      /SignedHeaders=content-type;host;if-none-match;x-amz-content-sha256;x-amz-date;x-amz-meta-da-generation;x-amz-security-token,/,
    );
  });

  it("uses path-style URLs for MinIO", async () => {
    const stub = stubFetch(() => new Response(null, { status: 200 }));
    const minio: ArtifactStoreConfig = { kind: "s3", region: "us-east-1", endpoint: "http://minio:9000", forcePathStyle: true };
    await uploadImmutable(minio, "artifacts", object, bytes, { fetch: stub.fetch, credentials, now });
    expect(stub.calls[0]!.url).toBe(`http://minio:9000/artifacts/${object}`);
  });

  it.each([412, 409])("reads the existing generation with HeadObject on %i", async (status) => {
    const stub = stubFetch(
      () => new Response("<Error><Code>PreconditionFailed</Code></Error>", { status }),
      () => new Response(null, { status: 200, headers: { "x-amz-meta-da-generation": "1" } }),
    );
    const generation = await uploadImmutable(s3, "artifacts", object, bytes, { fetch: stub.fetch, credentials, now });
    expect(generation).toBe(1);
    expect(stub.calls.map((c) => c.method)).toEqual(["PUT", "HEAD"]);
    expect(stub.calls[1]!.url).toBe(stub.calls[0]!.url);
    expect(stub.calls[1]!.headers["if-none-match"]).toBeUndefined();
  });

  it("retries the conditional PUT when a 409 conflict leaves the object absent", async () => {
    const sleeps: number[] = [];
    const stub = stubFetch(
      () => new Response("<Error><Code>ConditionalRequestConflict</Code></Error>", { status: 409 }),
      () => new Response(null, { status: 404 }),
      () => new Response(null, { status: 200 }),
    );
    const generation = await uploadImmutable(s3, "artifacts", object, bytes, {
      fetch: stub.fetch,
      credentials,
      now,
      sleep: async (ms) => void sleeps.push(ms),
    });
    expect(generation).toBe(1);
    expect(stub.calls.map((c) => c.method)).toEqual(["PUT", "HEAD", "PUT"]);
    expect(sleeps).toEqual([250]);
  });

  it("gives up after bounded 409 retries and does not retry a 412 whose object is missing", async () => {
    const conflict = stubFetch(
      ...Array.from({ length: 5 }, () => [() => new Response(null, { status: 409 }), () => new Response(null, { status: 404 })]).flat(),
    );
    const sleeps: number[] = [];
    await expect(
      uploadImmutable(s3, "artifacts", object, bytes, { fetch: conflict.fetch, credentials, now, sleep: async (ms) => void sleeps.push(ms) }),
    ).rejects.toThrow(/metadata read failed: HTTP 404/);
    expect(conflict.calls).toHaveLength(10);
    expect(sleeps).toEqual([250, 500, 1000, 2000]);
    const precondition = stubFetch(
      () => new Response(null, { status: 412 }),
      () => new Response(null, { status: 404 }),
    );
    await expect(
      uploadImmutable(s3, "artifacts", object, bytes, { fetch: precondition.fetch, credentials, now, sleep: async () => {} }),
    ).rejects.toThrow(/HTTP 404/);
    expect(precondition.calls).toHaveLength(2);
  });

  it("uses path style for a dotted bucket over https", async () => {
    const stub = stubFetch(() => new Response(null, { status: 200 }));
    await uploadImmutable(s3, "team.artifacts", object, bytes, { fetch: stub.fetch, credentials, now });
    expect(stub.calls[0]!.url).toBe(`https://s3.us-east-1.amazonaws.com/team.artifacts/${object}`);
  });

  it("fails clearly when the existing object has no da-generation metadata", async () => {
    const stub = stubFetch(
      () => new Response(null, { status: 412 }),
      () => new Response(null, { status: 200 }),
    );
    await expect(uploadImmutable(s3, "artifacts", object, bytes, { fetch: stub.fetch, credentials, now })).rejects.toThrow(
      /already exists without da-generation metadata/,
    );
  });

  it("rejects invalid metadata and surfaces other failures", async () => {
    const bad = stubFetch(
      () => new Response(null, { status: 412 }),
      () => new Response(null, { status: 200, headers: { "x-amz-meta-da-generation": "abc" } }),
    );
    await expect(uploadImmutable(s3, "artifacts", object, bytes, { fetch: bad.fetch, credentials, now })).rejects.toThrow(/invalid da-generation/);
    const denied = stubFetch(() => new Response("AccessDenied", { status: 403 }));
    await expect(uploadImmutable(s3, "artifacts", object, bytes, { fetch: denied.fetch, credentials, now })).rejects.toThrow(/HTTP 403/);
    const headFails = stubFetch(
      () => new Response(null, { status: 412 }),
      () => new Response(null, { status: 404 }),
    );
    await expect(uploadImmutable(s3, "artifacts", object, bytes, { fetch: headFails.fetch, credentials, now })).rejects.toThrow(
      /metadata read failed: HTTP 404/,
    );
  });
});

describe("GCS artifact upload", () => {
  it("creates with ifGenerationMatch=0 and returns the new generation", async () => {
    const stub = stubFetch(() => Response.json({ generation: "1730592000000000" }));
    const generation = await uploadImmutable({ kind: "gcs" }, "artifacts", object, bytes, { fetch: stub.fetch, gcsToken: async () => "tok" });
    expect(generation).toBe(1730592000000000);
    const url = new URL(stub.calls[0]!.url);
    expect(url.origin + url.pathname).toBe("https://storage.googleapis.com/upload/storage/v1/b/artifacts/o");
    expect(url.searchParams.get("ifGenerationMatch")).toBe("0");
    expect(url.searchParams.get("name")).toBe(object);
    expect(stub.calls[0]!.headers).toMatchObject({ authorization: "Bearer tok", "content-type": "text/javascript" });
  });

  it("reads the existing generation on 412 and skips auth for the emulator", async () => {
    const stub = stubFetch(
      () => new Response(null, { status: 412 }),
      () => Response.json({ generation: "42" }),
    );
    const generation = await uploadImmutable({ kind: "gcs", emulatorHost: "fake-gcs:4443" }, "artifacts", object, bytes, { fetch: stub.fetch });
    expect(generation).toBe(42);
    expect(stub.calls[1]!.url).toBe(`http://fake-gcs:4443/storage/v1/b/artifacts/o/${encodeURIComponent(object)}`);
    expect(stub.calls.every((c) => !c.headers.authorization)).toBe(true);
  });
});

describe("AWS default credential chain", () => {
  it("prefers environment keys", async () => {
    const provider = defaultCredentialProvider({
      env: { AWS_ACCESS_KEY_ID: "AKID", AWS_SECRET_ACCESS_KEY: "s", AWS_SESSION_TOKEN: "t" },
      fetch: stubFetch().fetch,
    });
    expect(await provider()).toEqual({ accessKeyId: "AKID", secretAccessKey: "s", sessionToken: "t" });
  });

  it("uses EKS Pod Identity with the token file and caches until near expiry", async () => {
    const expiration = "2026-11-03T06:00:00Z";
    const stub = stubFetch(() => Response.json({ AccessKeyId: "ASIA", SecretAccessKey: "s", Token: "t", Expiration: expiration }));
    const provider = defaultCredentialProvider({
      env: {
        AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://169.254.170.23/v1/credentials",
        AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token",
      },
      fetch: stub.fetch,
      readFile: async () => "pod-token\n",
      now,
    });
    expect(await provider()).toEqual({ accessKeyId: "ASIA", secretAccessKey: "s", sessionToken: "t", expiration: new Date(expiration) });
    await provider();
    expect(stub.calls).toHaveLength(1);
    expect(stub.calls[0]!.headers.authorization).toBe("pod-token");
    // Never follow a redirect away from the checked host with the token.
    expect(stub.calls[0]!.redirect).toBe("error");
  });

  it("refreshes inside the five-minute window, shares the refresh, and retries after a failed one", async () => {
    let clock = new Date("2026-11-03T00:00:00Z");
    const creds = (id: string, expiration: string) => () => Response.json({ AccessKeyId: id, SecretAccessKey: "s", Token: "t", Expiration: expiration });
    const stub = stubFetch(
      creds("ASIA1", "2026-11-03T01:00:00Z"),
      () => new Response("unavailable", { status: 503 }),
      creds("ASIA2", "2026-11-03T02:00:00Z"),
    );
    const provider = defaultCredentialProvider({
      env: { AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://169.254.170.23/v1/credentials" },
      fetch: stub.fetch,
      now: () => clock,
    });
    expect((await provider()).accessKeyId).toBe("ASIA1");
    clock = new Date("2026-11-03T00:54:00Z"); // more than five minutes left: cached
    expect((await provider()).accessKeyId).toBe("ASIA1");
    expect(stub.calls).toHaveLength(1);
    clock = new Date("2026-11-03T00:56:00Z"); // inside the refresh window
    await expect(provider()).rejects.toThrow(/HTTP 503/);
    const [a, b] = await Promise.all([provider(), provider()]);
    expect([a.accessKeyId, b.accessKeyId]).toEqual(["ASIA2", "ASIA2"]);
    expect(stub.calls).toHaveLength(3);
  });

  it("gives credentials without an Expiration a bounded lifetime", async () => {
    let clock = new Date("2026-11-03T00:00:00Z");
    const stub = stubFetch(
      () => Response.json({ AccessKeyId: "ASIA1", SecretAccessKey: "s", Token: "t" }),
      () => Response.json({ AccessKeyId: "ASIA2", SecretAccessKey: "s", Token: "t" }),
    );
    const provider = defaultCredentialProvider({
      env: { AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://169.254.170.23/v1/credentials" },
      fetch: stub.fetch,
      now: () => clock,
    });
    expect((await provider()).accessKeyId).toBe("ASIA1");
    clock = new Date("2026-11-03T00:16:00Z");
    expect((await provider()).accessKeyId).toBe("ASIA2");
  });

  it("rejects a half-configured static key pair instead of using another identity", async () => {
    const provider = defaultCredentialProvider({
      env: { AWS_ACCESS_KEY_ID: "AKID", AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://169.254.170.23/v1/credentials" },
      fetch: stubFetch().fetch,
    });
    await expect(provider()).rejects.toThrow(/must be set together/);
  });

  it("refuses plain-http container endpoints outside the allowed hosts", async () => {
    const provider = defaultCredentialProvider({
      env: { AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://attacker.example/creds" },
      fetch: stubFetch().fetch,
    });
    await expect(provider()).rejects.toThrow(/not allowed/);
  });

  it("assumes the IRSA role through STS web identity", async () => {
    const xml =
      "<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>" +
      "<AccessKeyId>ASIAW</AccessKeyId><SecretAccessKey>sw</SecretAccessKey><SessionToken>tw</SessionToken>" +
      "<Expiration>2026-11-03T01:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>";
    const stub = stubFetch(() => new Response(xml));
    const provider = defaultCredentialProvider({
      env: { AWS_WEB_IDENTITY_TOKEN_FILE: "/token", AWS_ROLE_ARN: "arn:aws:iam::123456789012:role/deployer", AWS_REGION: "us-east-1" },
      fetch: stub.fetch,
      readFile: async () => "jwt",
      now,
    });
    expect((await provider()).accessKeyId).toBe("ASIAW");
    const call = stub.calls[0]!;
    expect(call.url).toBe("https://sts.us-east-1.amazonaws.com/");
    expect(call.method).toBe("POST");
    expect(call.redirect).toBe("error");
    expect(call.headers["content-type"]).toBe("application/x-www-form-urlencoded");
    // The bearer token travels in the form body, not the URL.
    expect(call.form?.get("Action")).toBe("AssumeRoleWithWebIdentity");
    expect(call.form?.get("WebIdentityToken")).toBe("jwt");
  });

  it("fails clearly when no source is available", async () => {
    const provider = defaultCredentialProvider({ env: { AWS_EC2_METADATA_DISABLED: "true" }, fetch: stubFetch().fetch });
    await expect(provider()).rejects.toThrow(/no AWS credentials found/);
  });
});

describe("SigV4 extra headers", () => {
  // https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
  const example = {
    region: "us-east-1",
    service: "s3",
    accessKeyId: "AKIAIOSFODNN7EXAMPLE",
    secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    now: new Date("2013-05-24T00:00:00Z"),
  };
  const prefix = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, ";

  it("matches the AWS documented GET Object (Range) and PUT Object signatures", () => {
    const get = signV4({ ...example, method: "GET", url: new URL("https://examplebucket.s3.amazonaws.com/test.txt"), body: new Uint8Array(), headers: { Range: "bytes=0-9" } });
    expect(get.authorization).toBe(
      `${prefix}SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41`,
    );
    expect(get.range).toBe("bytes=0-9");
    const put = signV4({
      ...example,
      method: "PUT",
      url: new URL("https://examplebucket.s3.amazonaws.com/test%24file.text"),
      body: new TextEncoder().encode("Welcome to Amazon S3."),
      headers: { Date: "Fri, 24 May 2013 00:00:00 GMT", "x-amz-storage-class": "REDUCED_REDUNDANCY" },
    });
    expect(put.authorization).toBe(
      `${prefix}SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd`,
    );
  });

  it("collapses runs of whitespace in header values (TrimAll)", () => {
    const input = { ...example, method: "GET", url: new URL("https://examplebucket.s3.amazonaws.com/test.txt"), body: new Uint8Array() };
    expect(signV4({ ...input, headers: { Range: "  bytes=0-9  " } }).authorization).toBe(signV4({ ...input, headers: { Range: "bytes=0-9" } }).authorization);
    expect(signV4({ ...input, headers: { "x-amz-meta-a": "a   b" } }).authorization).toBe(signV4({ ...input, headers: { "x-amz-meta-a": "a b" } }).authorization);
  });
});

describe("gateway S3 blob store credentials", () => {
  const podIdentityEnv = {
    AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://169.254.170.23/v1/credentials",
    AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token",
  };
  const store = (fetchImpl: typeof fetch, env: NodeJS.ProcessEnv) =>
    new S3BlobStore({
      endpoint: "https://s3.us-east-1.amazonaws.com",
      region: "us-east-1",
      bucket: "blobs",
      forcePathStyle: false,
      fetch: fetchImpl,
      now,
      credentials: defaultCredentialProvider({ env, fetch: fetchImpl, readFile: async () => "pod-token", now }),
    });

  it("loads an s3 gateway config without static keys (ambient identity)", () => {
    const config = loadGatewayConfig({
      DURABLE_ACTORS_CONTROL_PLANE_URL: "http://cp:7100",
      RELAYFILE_JWT_SECRET: "s",
      RELAYFILE_ACTORS_BROKER_URL: "http://b",
      RELAYFILE_ACTORS_BROKER_TOKEN: "t",
      RELAYFILE_BLOB_STORE: "s3",
      RELAYFILE_BLOB_BUCKET: "blobs",
      RELAYFILE_S3_REGION: "us-east-1",
    });
    expect(config.blobStore?.kind).toBe("s3");
  });

  it("signs with static env keys first, without calling the credential endpoint", async () => {
    const stub = stubFetch(() => new Response(null, { status: 200 }));
    await store(stub.fetch, { AWS_ACCESS_KEY_ID: "AKIDSTATIC", AWS_SECRET_ACCESS_KEY: "s", ...podIdentityEnv }).put("k", bytes);
    expect(stub.calls).toHaveLength(1);
    expect(stub.calls[0]!.headers.authorization).toContain("Credential=AKIDSTATIC/");
  });

  it("signs with EKS Pod Identity credentials and shares one refresh across concurrent requests", async () => {
    const stub = stubFetch(
      () => Response.json({ AccessKeyId: "ASIAPOD", SecretAccessKey: "s", Token: "pod-session", Expiration: "2026-11-03T06:00:00Z" }),
      () => new Response(null, { status: 200 }),
      () => new Response(null, { status: 200 }),
    );
    const blobs = store(stub.fetch, podIdentityEnv);
    await Promise.all([blobs.put("a", bytes), blobs.put("b", bytes)]);
    expect(stub.calls.map((c) => c.url)).toEqual([
      "http://169.254.170.23/v1/credentials",
      "https://blobs.s3.us-east-1.amazonaws.com/a",
      "https://blobs.s3.us-east-1.amazonaws.com/b",
    ]);
    expect(stub.calls[0]!.headers.authorization).toBe("pod-token");
    for (const call of stub.calls.slice(1)) {
      expect(call.headers.authorization).toContain("Credential=ASIAPOD/");
      expect(call.headers["x-amz-security-token"]).toBe("pod-session");
    }
  });
});
