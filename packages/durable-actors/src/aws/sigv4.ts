import { createHash, createHmac } from "node:crypto";

/**
 * Dependency-free AWS Signature Version 4 and S3 URL helpers, shared by the
 * gateway blob store (src/gateway/blobs.ts) and the actor deployer's S3
 * artifact upload (src/deploy/artifacts.ts). Imports only node:crypto so it
 * also runs under Bun in the relayfile-actors-workspace image.
 */

export function encodeRfc3986(value: string): string {
  return encodeURIComponent(value).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
}

/** Virtual-hosted (default) or path-style URL of one S3 object. */
export function s3ObjectUrl(endpoint: string, bucket: string, key: string, forcePathStyle: boolean): URL {
  const url = new URL(endpoint);
  const encodedKey = key.split("/").map(encodeRfc3986).join("/");
  if (forcePathStyle) {
    url.pathname = `${url.pathname.replace(/\/$/, "")}/${bucket}/${encodedKey}`;
  } else {
    url.hostname = `${bucket}.${url.hostname}`;
    url.pathname = `${url.pathname.replace(/\/$/, "")}/${encodedKey}`;
  }
  return url;
}

export interface SignInput {
  method: string;
  url: URL;
  body: Uint8Array;
  region: string;
  service: string;
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
  /** Extra request headers to sign and send (S3 requires every x-amz-* header to be signed). */
  headers?: Record<string, string>;
  now: Date;
}

/** AWS Signature Version 4 headers for one request. Exported for tests. */
export function signV4(input: SignInput): Record<string, string> {
  const amzDate = input.now.toISOString().replace(/[:-]|\.\d{3}/g, "");
  const dateStamp = amzDate.slice(0, 8);
  const payloadHash = createHash("sha256").update(input.body).digest("hex");
  const headers: Record<string, string> = {};
  for (const [name, value] of Object.entries(input.headers ?? {})) headers[name.toLowerCase()] = value;
  headers.host = input.url.host;
  headers["x-amz-content-sha256"] = payloadHash;
  headers["x-amz-date"] = amzDate;
  if (input.sessionToken) headers["x-amz-security-token"] = input.sessionToken;
  const signedHeaderNames = Object.keys(headers).sort();
  const canonicalHeaders = signedHeaderNames.map((name) => `${name}:${headers[name]!.trim()}\n`).join("");
  const signedHeaders = signedHeaderNames.join(";");
  const query = [...input.url.searchParams.entries()]
    .map(([k, v]) => [encodeRfc3986(k), encodeRfc3986(v)] as const)
    .sort(([a, av], [b, bv]) => (a === b ? (av < bv ? -1 : 1) : a < b ? -1 : 1))
    .map(([k, v]) => `${k}=${v}`)
    .join("&");
  const canonicalRequest = [
    input.method,
    input.url.pathname || "/",
    query,
    canonicalHeaders,
    signedHeaders,
    payloadHash,
  ].join("\n");
  const scope = `${dateStamp}/${input.region}/${input.service}/aws4_request`;
  const stringToSign = [
    "AWS4-HMAC-SHA256",
    amzDate,
    scope,
    createHash("sha256").update(canonicalRequest).digest("hex"),
  ].join("\n");
  const kDate = createHmac("sha256", `AWS4${input.secretAccessKey}`).update(dateStamp).digest();
  const kRegion = createHmac("sha256", kDate).update(input.region).digest();
  const kService = createHmac("sha256", kRegion).update(input.service).digest();
  const kSigning = createHmac("sha256", kService).update("aws4_request").digest();
  const signature = createHmac("sha256", kSigning).update(stringToSign).digest("hex");
  const { host: _host, ...rest } = headers;
  return {
    ...rest,
    authorization: `AWS4-HMAC-SHA256 Credential=${input.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
  };
}
