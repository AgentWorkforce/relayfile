import { defaultCredentialProvider, type CredentialProvider } from "../aws/credentials.js";
import { s3ObjectUrl, signV4 } from "../aws/sigv4.js";

/**
 * Create-only upload of the compiled actor bundle to the durable-actors
 * artifact bucket, on GCS (default) or S3. Object names come from the bytes,
 * so a re-run finds the object already present and reuses its generation.
 *
 * The S3 contract matches the durable-actors S3 storage backend: PutObject
 * with `If-None-Match: *`, user metadata `da-generation: 1`, content-type
 * text/javascript; on 412/409 the existing object's `da-generation` is read
 * back with HeadObject.
 */

export const S3_ARTIFACT_GENERATION = 1;

export type ArtifactStoreConfig =
  | { kind: "gcs"; emulatorHost?: string }
  | { kind: "s3"; region: string; endpoint: string; forcePathStyle: boolean };

export function loadArtifactStoreConfig(env: NodeJS.ProcessEnv = process.env): ArtifactStoreConfig {
  const get = (name: string) => env[name]?.trim() || undefined;
  const kind = (get("DURABLE_ACTORS_ARTIFACT_STORE") ?? "gcs").toLowerCase();
  if (kind === "gcs") return { kind, emulatorHost: get("STORAGE_EMULATOR_HOST") };
  if (kind !== "s3") throw new Error("DURABLE_ACTORS_ARTIFACT_STORE must be gcs or s3");
  const region = get("DURABLE_ACTORS_S3_REGION") ?? get("AWS_REGION");
  if (!region) throw new Error("DURABLE_ACTORS_S3_REGION (or AWS_REGION) is required for the s3 artifact store");
  const endpoint = get("DURABLE_ACTORS_S3_ENDPOINT") ?? `https://s3.${region}.amazonaws.com`;
  const url = new URL(endpoint);
  if (url.protocol !== "https:" && url.protocol !== "http:") throw new Error("DURABLE_ACTORS_S3_ENDPOINT must be an http(s) URL");
  const rawPathStyle = (get("DURABLE_ACTORS_S3_FORCE_PATH_STYLE") ?? "false").toLowerCase();
  if (!["true", "1", "yes", "false", "0", "no"].includes(rawPathStyle)) {
    throw new Error("DURABLE_ACTORS_S3_FORCE_PATH_STYLE must be true or false");
  }
  return { kind, region, endpoint, forcePathStyle: ["true", "1", "yes"].includes(rawPathStyle) };
}

export interface UploadDeps {
  fetch?: typeof fetch;
  /** S3: credential source; defaults to the AWS default chain (EKS Pod Identity, IRSA, ...). */
  credentials?: CredentialProvider;
  /** GCS: bearer token source; defaults to the metadata server (Workload Identity). */
  gcsToken?: () => Promise<string>;
  now?: () => Date;
}

/** Create-only upload; returns the object's generation (existing or new). */
export function uploadImmutable(
  config: ArtifactStoreConfig,
  bucket: string,
  object: string,
  bytes: Uint8Array,
  deps: UploadDeps = {},
): Promise<number> {
  return config.kind === "s3" ? uploadS3(config, bucket, object, bytes, deps) : uploadGcs(config, bucket, object, bytes, deps);
}

// ---------------------------------------------------------------------------
// GCS JSON API
// ---------------------------------------------------------------------------

const METADATA_TOKEN_URL =
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token";

async function uploadGcs(
  config: { emulatorHost?: string },
  bucket: string,
  object: string,
  bytes: Uint8Array,
  deps: UploadDeps,
): Promise<number> {
  const fetchImpl = deps.fetch ?? fetch;
  const emulator = config.emulatorHost?.trim();
  const base = emulator
    ? (/^https?:\/\//.test(emulator) ? emulator : `http://${emulator}`).replace(/\/$/, "")
    : "https://storage.googleapis.com";
  const headers: Record<string, string> = emulator ? {} : { authorization: `Bearer ${await gcsToken(fetchImpl, deps)}` };
  const url = new URL(`${base}/upload/storage/v1/b/${encodeURIComponent(bucket)}/o`);
  url.searchParams.set("uploadType", "media");
  url.searchParams.set("name", object);
  url.searchParams.set("ifGenerationMatch", "0");
  const upload = await fetchImpl(url, {
    method: "POST",
    headers: { ...headers, "content-type": "text/javascript" },
    body: bytes as BodyInit,
    signal: AbortSignal.timeout(120_000),
  });
  if (upload.ok) return Number(((await upload.json()) as { generation: string }).generation);
  if (upload.status !== 412) throw new Error(`artifact upload failed: HTTP ${upload.status} ${await upload.text()}`);
  const meta = await fetchImpl(`${base}/storage/v1/b/${encodeURIComponent(bucket)}/o/${encodeURIComponent(object)}`, {
    headers,
    signal: AbortSignal.timeout(30_000),
  });
  if (!meta.ok) throw new Error(`artifact metadata read failed: HTTP ${meta.status}`);
  return Number(((await meta.json()) as { generation: string }).generation);
}

async function gcsToken(fetchImpl: typeof fetch, deps: UploadDeps): Promise<string> {
  if (deps.gcsToken) return deps.gcsToken();
  const response = await fetchImpl(METADATA_TOKEN_URL, {
    headers: { "Metadata-Flavor": "Google" },
    signal: AbortSignal.timeout(5_000),
  });
  if (!response.ok) throw new Error(`metadata token request failed: HTTP ${response.status}`);
  return ((await response.json()) as { access_token: string }).access_token;
}

// ---------------------------------------------------------------------------
// S3 (AWS, MinIO)
// ---------------------------------------------------------------------------

async function uploadS3(
  config: { region: string; endpoint: string; forcePathStyle: boolean },
  bucket: string,
  object: string,
  bytes: Uint8Array,
  deps: UploadDeps,
): Promise<number> {
  const fetchImpl = deps.fetch ?? fetch;
  const credentials = deps.credentials ?? defaultCredentialProvider({ fetch: fetchImpl });
  const now = deps.now ?? (() => new Date());
  const url = s3ObjectUrl(config.endpoint, bucket, object, config.forcePathStyle);
  const send = async (method: string, extra: Record<string, string>, body?: Uint8Array, timeoutMs = 30_000) => {
    const creds = await credentials();
    const headers = signV4({
      method,
      url,
      body: body ?? new Uint8Array(),
      region: config.region,
      service: "s3",
      accessKeyId: creds.accessKeyId,
      secretAccessKey: creds.secretAccessKey,
      sessionToken: creds.sessionToken,
      headers: extra,
      now: now(),
    });
    return fetchImpl(url, { method, headers, body: body as BodyInit | undefined, signal: AbortSignal.timeout(timeoutMs) });
  };

  const put = await send(
    "PUT",
    {
      "content-type": "text/javascript",
      "if-none-match": "*",
      "x-amz-meta-da-generation": String(S3_ARTIFACT_GENERATION),
    },
    bytes,
    120_000,
  );
  if (put.ok) {
    await put.body?.cancel();
    return S3_ARTIFACT_GENERATION;
  }
  if (put.status !== 412 && put.status !== 409) {
    throw new Error(`artifact upload failed: HTTP ${put.status} ${await put.text()}`);
  }
  await put.body?.cancel();

  const head = await send("HEAD", {});
  await head.body?.cancel();
  if (!head.ok) throw new Error(`artifact metadata read failed: HTTP ${head.status}`);
  const raw = head.headers.get("x-amz-meta-da-generation")?.trim();
  if (!raw) {
    throw new Error(
      `artifact s3://${bucket}/${object} already exists without da-generation metadata; ` +
        "it was not written by this deployer, so delete it or fix its metadata and re-run",
    );
  }
  const generation = Number(raw);
  if (!/^\d+$/.test(raw) || !Number.isSafeInteger(generation) || generation < 1) {
    throw new Error(`artifact s3://${bucket}/${object} has invalid da-generation metadata ${JSON.stringify(raw)}`);
  }
  return generation;
}
