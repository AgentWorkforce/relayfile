import { defaultCredentialProvider } from "../aws/credentials.js";
import { GcsBlobStore, S3BlobStore, type BlobStore } from "./blobs.js";

/**
 * Gateway configuration from the environment. The env contract is documented
 * in packages/durable-actors/README.md; keep the two in sync.
 */
export interface GatewayConfig {
  port: number;
  controlPlaneUrl: string;
  projectId: string;
  /** Admin key: only with allowAdminKey (dev sidecar). */
  adminKey: string | null;
  brokerUrl: string | null;
  brokerToken: string | null;
  jwksUrl: string | null;
  jwtSecret: string | null;
  internalHmacSecret: string | null;
  externalWritebackProviders: string[];
  blobStore: BlobStore | null;
  blobPrefix: string;
  inlineMaxBytes: number;
  maxBodyBytes: number;
}

type Env = Record<string, string | undefined>;

function get(env: Env, name: string): string | null {
  const value = env[name]?.trim();
  return value ? value : null;
}

function bool(env: Env, name: string): boolean {
  return /^(1|true|yes)$/i.test(env[name]?.trim() ?? "");
}

function int(env: Env, name: string, fallback: number, min: number, max: number): number {
  const raw = get(env, name);
  if (raw === null) return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < min || value > max) throw new Error(`${name} must be an integer in [${min}, ${max}]`);
  return value;
}

export function loadGatewayConfig(env: Env = process.env): GatewayConfig {
  const controlPlaneUrl = get(env, "DURABLE_ACTORS_CONTROL_PLANE_URL");
  if (!controlPlaneUrl) throw new Error("DURABLE_ACTORS_CONTROL_PLANE_URL is required");
  const projectId = get(env, "DURABLE_ACTORS_PROJECT_ID") ?? "relayfile";

  const brokerUrl = get(env, "RELAYFILE_ACTORS_BROKER_URL");
  const brokerToken = get(env, "RELAYFILE_ACTORS_BROKER_TOKEN");
  const adminKey = get(env, "DURABLE_ACTORS_SECRET") ?? get(env, "DURABLE_ACTORS_API_KEY");
  const allowAdminKey = bool(env, "RELAYFILE_ACTORS_ALLOW_ADMIN_KEY");
  if (adminKey && !allowAdminKey) {
    throw new Error(
      "DURABLE_ACTORS_SECRET is set on the gateway: the internet-facing gateway must not hold the durable-actors admin key. " +
        "Use RELAYFILE_ACTORS_BROKER_URL/RELAYFILE_ACTORS_BROKER_TOKEN, or set RELAYFILE_ACTORS_ALLOW_ADMIN_KEY=true for a dev sidecar.",
    );
  }
  if (brokerUrl && !brokerToken) throw new Error("RELAYFILE_ACTORS_BROKER_TOKEN is required with RELAYFILE_ACTORS_BROKER_URL");
  if (!brokerUrl && !adminKey) {
    throw new Error("configure RELAYFILE_ACTORS_BROKER_URL (production) or DURABLE_ACTORS_SECRET with RELAYFILE_ACTORS_ALLOW_ADMIN_KEY=true (dev)");
  }

  const jwksUrl = get(env, "RELAYAUTH_JWKS_URL");
  const jwtSecret = get(env, "RELAYFILE_JWT_SECRET");
  if (!jwksUrl && !jwtSecret) throw new Error("RELAYAUTH_JWKS_URL or RELAYFILE_JWT_SECRET is required");

  const driver = (get(env, "RELAYFILE_BLOB_STORE") ?? "inline").toLowerCase();
  const bucket = get(env, "RELAYFILE_BLOB_BUCKET") ?? "";
  let prefix = get(env, "RELAYFILE_BLOB_PREFIX") ?? "";
  if (prefix && !prefix.endsWith("/")) prefix += "/";
  let blobStore: BlobStore | null;
  switch (driver) {
    case "inline":
      blobStore = null;
      break;
    case "gcs":
      blobStore = new GcsBlobStore({ bucket, emulatorHost: get(env, "STORAGE_EMULATOR_HOST") ?? undefined });
      break;
    case "s3":
      blobStore = new S3BlobStore({
        bucket,
        endpoint: get(env, "RELAYFILE_S3_ENDPOINT") ?? `https://s3.${get(env, "RELAYFILE_S3_REGION") ?? "us-east-1"}.amazonaws.com`,
        region: get(env, "RELAYFILE_S3_REGION") ?? "us-east-1",
        // Static AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY first, else the pod's
        // ambient identity (EKS Pod Identity, IRSA, instance metadata).
        credentials: defaultCredentialProvider({ env }),
        forcePathStyle: bool(env, "RELAYFILE_S3_FORCE_PATH_STYLE"),
      });
      break;
    default:
      throw new Error("RELAYFILE_BLOB_STORE must be gcs, s3, or inline");
  }

  return {
    port: int(env, "PORT", 8080, 1, 65535),
    controlPlaneUrl,
    projectId,
    adminKey: allowAdminKey ? adminKey : null,
    brokerUrl,
    brokerToken,
    jwksUrl,
    jwtSecret,
    internalHmacSecret: get(env, "RELAYFILE_INTERNAL_HMAC_SECRET"),
    externalWritebackProviders: (get(env, "RELAYFILE_EXTERNAL_WRITEBACK_PROVIDERS") ?? "")
      .split(",")
      .map((value) => value.trim().toLowerCase())
      .filter(Boolean),
    blobStore,
    blobPrefix: prefix,
    // Actor calls carry the whole request; keep inline content well under the runtime payload limits.
    inlineMaxBytes: int(env, "RELAYFILE_BLOB_INLINE_MAX_BYTES", 64 * 1024, 0, 4 * 1024 * 1024),
    maxBodyBytes: int(env, "RELAYFILE_MAX_BODY_BYTES", 64 * 1024 * 1024, 1024, 512 * 1024 * 1024),
  };
}
