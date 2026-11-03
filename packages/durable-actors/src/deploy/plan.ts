import { createHash } from "node:crypto";

/**
 * Pure pieces of the relayfile actor deployer (actor/deploy.ts): artifact
 * naming and the registerDeployment request body for
 * PUT /v1/projects/{project}/deployment (durable-actors 0.14.6 OpenAPI).
 */

export const ARTIFACT_PREFIX = "durable-actors/v3/artifacts";

/**
 * A deterministic RFC 4122 v4-shaped UUID derived from the artifact bytes, so
 * re-running the deployer for the same build reuses the same immutable
 * objects (idempotent) and a different build never overwrites them.
 */
export function artifactUuid(bytes: Uint8Array): string {
  const hex = createHash("sha256").update(bytes).digest("hex").slice(0, 32).split("");
  hex[12] = "4";
  hex[16] = ((parseInt(hex[16]!, 16) & 0x3) | 0x8).toString(16);
  const h = hex.join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`;
}

export function artifactObject(uuid: string, path = "actors.mjs"): string {
  return `${ARTIFACT_PREFIX}/${uuid}/${path}`;
}

/** Base64url SHA-256 without padding (ActorBundle.files[].sha256). */
export function bundleSha256(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("base64url");
}

export interface DeploymentBodyInput {
  organizationId: string;
  bucket: string;
  object: string;
  generation: number;
  bytes: Uint8Array;
  contract: unknown;
  secretRefs: string[];
}

export function deploymentBody(input: DeploymentBodyInput): Record<string, unknown> {
  if (!/^[A-Za-z0-9._-]{1,64}$/.test(input.organizationId)) throw new Error("invalid organization id");
  if (!Number.isSafeInteger(input.generation) || input.generation < 1) throw new Error("invalid object generation");
  for (const ref of input.secretRefs) {
    if (!/^[A-Za-z0-9._-]{1,255}$/.test(ref)) throw new Error(`invalid secret ref ${ref}`);
  }
  return {
    organizationId: input.organizationId,
    bundle: {
      bucket: input.bucket,
      files: [{ path: "actors.mjs", object: input.object, generation: input.generation, sha256: bundleSha256(input.bytes) }],
    },
    contract: input.contract,
    secretRefs: input.secretRefs,
  };
}

export function parseSecretRefs(raw: string | undefined): string[] {
  return (raw ?? "")
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
}
