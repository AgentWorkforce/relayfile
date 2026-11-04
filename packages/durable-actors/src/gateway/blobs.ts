import { createHash } from "node:crypto";
import { defaultCredentialProvider, type CredentialProvider } from "../aws/credentials.js";
import { s3ObjectUrl, signV4 } from "../aws/sigv4.js";

export { signV4 } from "../aws/sigv4.js";

/**
 * Content-addressed blob storage for file bytes that are too large to keep
 * inline in a workspace actor's SQLite.
 *
 * Keys are `<prefix>blobs/sha256/<aa>/<sha256>`: immutable, so concurrent
 * gateway replicas writing the same content converge on the same object and
 * no coordination is needed (multi-replica safe). Objects are never
 * overwritten with different bytes.
 */
export interface BlobStore {
  readonly kind: string;
  put(key: string, bytes: Uint8Array): Promise<void>;
  get(key: string): Promise<Uint8Array | null>;
  /** Reachability check used by /health?deep=1. */
  probe(): Promise<void>;
}

export function blobKey(prefix: string, sha256: string): string {
  if (!/^[a-f0-9]{64}$/.test(sha256)) throw new Error("invalid blob digest");
  return `${prefix}blobs/sha256/${sha256.slice(0, 2)}/${sha256}`;
}

export function sha256Hex(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

// ---------------------------------------------------------------------------
// In-memory (tests)
// ---------------------------------------------------------------------------

export class MemoryBlobStore implements BlobStore {
  readonly kind = "memory";
  readonly objects = new Map<string, Uint8Array>();
  async put(key: string, bytes: Uint8Array): Promise<void> {
    if (!this.objects.has(key)) this.objects.set(key, new Uint8Array(bytes));
  }
  async get(key: string): Promise<Uint8Array | null> {
    const bytes = this.objects.get(key);
    return bytes ? new Uint8Array(bytes) : null;
  }
  async probe(): Promise<void> {}
}

// ---------------------------------------------------------------------------
// S3-compatible (AWS S3, MinIO, Cloudflare R2, GCS XML API with HMAC keys)
// ---------------------------------------------------------------------------

export interface S3Options {
  endpoint: string;
  region: string;
  bucket: string;
  /**
   * Credential source; defaults to the AWS default chain: AWS_ACCESS_KEY_ID /
   * AWS_SECRET_ACCESS_KEY first, then container credentials (EKS Pod Identity,
   * ECS), IRSA, instance metadata.
   */
  credentials?: CredentialProvider;
  forcePathStyle: boolean;
  fetch?: typeof fetch;
  now?: () => Date;
}

export class S3BlobStore implements BlobStore {
  readonly kind = "s3";
  private readonly fetchImpl: typeof fetch;
  private readonly now: () => Date;
  private readonly credentials: CredentialProvider;
  constructor(private readonly options: S3Options) {
    if (!options.bucket) throw new Error("RELAYFILE_BLOB_BUCKET is required for the s3 blob store");
    this.fetchImpl = options.fetch ?? fetch;
    this.now = options.now ?? (() => new Date());
    this.credentials = options.credentials ?? defaultCredentialProvider({ fetch: this.fetchImpl });
  }

  private objectUrl(key: string): URL {
    return s3ObjectUrl(this.options.endpoint, this.options.bucket, key, this.options.forcePathStyle);
  }

  async put(key: string, bytes: Uint8Array): Promise<void> {
    const response = await this.send("PUT", this.objectUrl(key), bytes);
    await response.body?.cancel();
    if (!response.ok) throw new Error(`s3 put failed: HTTP ${response.status}`);
  }

  async get(key: string): Promise<Uint8Array | null> {
    const response = await this.send("GET", this.objectUrl(key));
    if (response.status === 404) {
      await response.body?.cancel();
      return null;
    }
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error(`s3 get failed: HTTP ${response.status}`);
    }
    return new Uint8Array(await response.arrayBuffer());
  }

  async probe(): Promise<void> {
    const url = this.objectUrl("");
    url.search = "list-type=2&max-keys=1";
    const response = await this.send("GET", url);
    await response.body?.cancel();
    if (!response.ok) throw new Error(`s3 bucket probe failed: HTTP ${response.status}`);
  }

  private async send(method: string, url: URL, body?: Uint8Array): Promise<Response> {
    const creds = await this.credentials();
    const headers = signV4({
      method,
      url,
      body: body ?? new Uint8Array(),
      region: this.options.region,
      service: "s3",
      accessKeyId: creds.accessKeyId,
      secretAccessKey: creds.secretAccessKey,
      sessionToken: creds.sessionToken,
      now: this.now(),
    });
    return this.fetchImpl(url, {
      method,
      headers,
      body: body as BodyInit | undefined,
      signal: AbortSignal.timeout(60_000),
    });
  }
}

// ---------------------------------------------------------------------------
// Google Cloud Storage JSON API (Workload Identity / metadata server)
// ---------------------------------------------------------------------------

export interface GcsOptions {
  bucket: string;
  /** e.g. http://fake-gcs:4443 for fake-gcs-server; requests then carry no token. */
  emulatorHost?: string;
  fetch?: typeof fetch;
  /** Override token acquisition (tests). */
  token?: () => Promise<string>;
}

const METADATA_TOKEN_URL =
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token";

export class GcsBlobStore implements BlobStore {
  readonly kind = "gcs";
  private readonly fetchImpl: typeof fetch;
  private readonly base: string;
  private cachedToken: { value: string; expiresAt: number } | undefined;
  constructor(private readonly options: GcsOptions) {
    if (!options.bucket) throw new Error("RELAYFILE_BLOB_BUCKET is required for the gcs blob store");
    this.fetchImpl = options.fetch ?? fetch;
    const emulator = options.emulatorHost?.trim();
    this.base = emulator
      ? (/^https?:\/\//.test(emulator) ? emulator : `http://${emulator}`).replace(/\/$/, "")
      : "https://storage.googleapis.com";
  }

  async put(key: string, bytes: Uint8Array): Promise<void> {
    const url = new URL(`${this.base}/upload/storage/v1/b/${encodeURIComponent(this.options.bucket)}/o`);
    url.searchParams.set("uploadType", "media");
    url.searchParams.set("name", key);
    // Content-addressed: create only if absent. 412 means another replica already wrote it.
    url.searchParams.set("ifGenerationMatch", "0");
    const response = await this.fetchImpl(url, {
      method: "POST",
      headers: { ...(await this.authHeaders()), "content-type": "application/octet-stream" },
      body: bytes as BodyInit,
      signal: AbortSignal.timeout(60_000),
    });
    await response.body?.cancel();
    if (response.ok || response.status === 412) return;
    throw new Error(`gcs put failed: HTTP ${response.status}`);
  }

  async get(key: string): Promise<Uint8Array | null> {
    const url = `${this.base}/storage/v1/b/${encodeURIComponent(this.options.bucket)}/o/${encodeURIComponent(key)}?alt=media`;
    const response = await this.fetchImpl(url, {
      headers: await this.authHeaders(),
      signal: AbortSignal.timeout(60_000),
    });
    if (response.status === 404) {
      await response.body?.cancel();
      return null;
    }
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error(`gcs get failed: HTTP ${response.status}`);
    }
    return new Uint8Array(await response.arrayBuffer());
  }

  async probe(): Promise<void> {
    const url = `${this.base}/storage/v1/b/${encodeURIComponent(this.options.bucket)}/o?maxResults=1&fields=kind`;
    const response = await this.fetchImpl(url, { headers: await this.authHeaders(), signal: AbortSignal.timeout(10_000) });
    await response.body?.cancel();
    if (!response.ok) throw new Error(`gcs bucket probe failed: HTTP ${response.status}`);
  }

  private async authHeaders(): Promise<Record<string, string>> {
    if (this.options.emulatorHost) return {};
    return { authorization: `Bearer ${await this.accessToken()}` };
  }

  private async accessToken(): Promise<string> {
    if (this.options.token) return this.options.token();
    if (this.cachedToken && this.cachedToken.expiresAt > Date.now() + 60_000) return this.cachedToken.value;
    const response = await this.fetchImpl(METADATA_TOKEN_URL, {
      headers: { "Metadata-Flavor": "Google" },
      signal: AbortSignal.timeout(5_000),
    });
    if (!response.ok) throw new Error(`metadata token request failed: HTTP ${response.status}`);
    const body = (await response.json()) as { access_token?: string; expires_in?: number };
    if (!body.access_token) throw new Error("metadata server returned no access token");
    this.cachedToken = { value: body.access_token, expiresAt: Date.now() + (body.expires_in ?? 300) * 1000 };
    return body.access_token;
  }
}
