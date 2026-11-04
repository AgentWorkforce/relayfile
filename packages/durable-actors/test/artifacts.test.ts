import { describe, expect, it } from "vitest";
import { defaultCredentialProvider } from "../src/aws/credentials.js";
import { signV4 } from "../src/aws/sigv4.js";
import { loadArtifactStoreConfig, uploadImmutable, type ArtifactStoreConfig } from "../src/deploy/artifacts.js";

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: Uint8Array;
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
    const url = new URL(stub.calls[0]!.url);
    expect(url.host).toBe("sts.us-east-1.amazonaws.com");
    expect(url.searchParams.get("Action")).toBe("AssumeRoleWithWebIdentity");
    expect(url.searchParams.get("WebIdentityToken")).toBe("jwt");
  });

  it("fails clearly when no source is available", async () => {
    const provider = defaultCredentialProvider({ env: { AWS_EC2_METADATA_DISABLED: "true" }, fetch: stubFetch().fetch });
    await expect(provider()).rejects.toThrow(/no AWS credentials found/);
  });
});

describe("SigV4 extra headers", () => {
  it("signs and returns caller headers, lower-cased", () => {
    const headers = signV4({
      method: "PUT",
      url: new URL("https://b.s3.amazonaws.com/k"),
      body: new Uint8Array(),
      region: "us-east-1",
      service: "s3",
      accessKeyId: "AKID",
      secretAccessKey: "s",
      headers: { "If-None-Match": "*", "X-Amz-Meta-Da-Generation": "1" },
      now: now(),
    });
    expect(headers["if-none-match"]).toBe("*");
    expect(headers["x-amz-meta-da-generation"]).toBe("1");
    expect(headers.authorization).toContain("SignedHeaders=host;if-none-match;x-amz-content-sha256;x-amz-date;x-amz-meta-da-generation,");
  });
});
