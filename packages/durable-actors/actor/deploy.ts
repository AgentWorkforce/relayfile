/**
 * Register the relayfile Workspace actor with a self-hosted durable-actors
 * control plane (the GKE and AWS providers require a compiled bundle in the
 * artifact bucket; they refuse localSource). Runs under Bun inside the
 * relayfile-actors-workspace image:
 *
 *   bun /app/actor/deploy.ts
 *
 * 1. compile src/actors.ts with the durable-actors SDK compiler -> actors.mjs + contract
 * 2. upload actors.mjs to $DURABLE_ACTORS_ARTIFACT_BUCKET/durable-actors/v3/artifacts/<uuid>/
 *    on GCS or S3 (uuid derived from the bytes; create-only, so re-runs are idempotent)
 * 3. PUT /v1/projects/$DURABLE_ACTORS_PROJECT_ID/deployment with the bundle and contract
 *
 * Env: DURABLE_ACTORS_CONTROL_PLANE_URL, DURABLE_ACTORS_SECRET (admin api-key),
 * DURABLE_ACTORS_PROJECT_ID, DURABLE_ACTORS_ORGANIZATION_ID,
 * DURABLE_ACTORS_ARTIFACT_BUCKET, DURABLE_ACTORS_SECRET_REFS (optional, comma separated),
 * DURABLE_ACTORS_ARTIFACT_STORE (gcs | s3, default gcs),
 * STORAGE_EMULATOR_HOST (optional, gcs tests). GCS credentials come from the
 * metadata server (Workload Identity).
 *
 * s3: DURABLE_ACTORS_S3_REGION (falls back to AWS_REGION),
 * DURABLE_ACTORS_S3_ENDPOINT (optional, e.g. MinIO), DURABLE_ACTORS_S3_FORCE_PATH_STYLE
 * (optional boolean). Credentials come from the AWS default chain (EKS Pod
 * Identity, IRSA, instance metadata); never static keys in config.
 */
import { execFile } from "node:child_process"
import { mkdtemp, readFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import path from "node:path"
import { promisify } from "node:util"
import { loadArtifactStoreConfig, uploadImmutable } from "../src/deploy/artifacts.js"
import { artifactObject, artifactUuid, deploymentBody, parseSecretRefs } from "../src/deploy/plan.js"

const env = (name: string, fallback?: string): string => {
    const value = process.env[name]?.trim() || fallback
    if (!value) throw new Error(`${name} is required`)
    return value
}

const log = (event: string, fields: Record<string, unknown> = {}) =>
    console.log(JSON.stringify({ ts: new Date().toISOString(), event, ...fields }))

async function main(): Promise<void> {
    const controlPlane = env("DURABLE_ACTORS_CONTROL_PLANE_URL").replace(/\/$/, "")
    const apiKey = env("DURABLE_ACTORS_SECRET")
    const projectId = env("DURABLE_ACTORS_PROJECT_ID", "relayfile")
    const organizationId = env("DURABLE_ACTORS_ORGANIZATION_ID", projectId)
    const bucket = env("DURABLE_ACTORS_ARTIFACT_BUCKET")
    const secretRefs = parseSecretRefs(process.env.DURABLE_ACTORS_SECRET_REFS)
    const store = loadArtifactStoreConfig()

    for (let attempt = 1; ; attempt++) {
        const health = await fetch(`${controlPlane}/healthz`, { signal: AbortSignal.timeout(5_000) }).catch(() => null)
        if (health?.ok) break
        if (attempt >= 60) throw new Error("durable-actors control plane did not become healthy")
        await new Promise(resolve => setTimeout(resolve, 5_000))
    }

    const projectDir = path.resolve(import.meta.dir)
    const out = await mkdtemp(path.join(tmpdir(), "relayfile-actors-"))
    const builder = path.join(projectDir, "..", "node_modules", "durable-actors", "dist", "compiler", "deployment-build.js")
    const { stdout } = await promisify(execFile)(process.execPath, [builder, projectDir, "src/actors.ts", out], {
        maxBuffer: 64 * 1024 * 1024
    })
    const contract = JSON.parse(stdout)
    const bytes = new Uint8Array(await readFile(path.join(out, "actors.mjs")))
    const object = artifactObject(artifactUuid(bytes))
    log("deploy.compiled", { bytes: bytes.byteLength, object })

    // Validate the registration body before the (immutable, never collected) upload.
    deploymentBody({ organizationId, bucket, object, generation: 1, bytes, contract, secretRefs })
    const generation = await uploadImmutable(store, bucket, object, bytes)
    log("deploy.uploaded", { store: store.kind, bucket, object, generation })

    const body = deploymentBody({ organizationId, bucket, object, generation, bytes, contract, secretRefs })
    const response = await fetch(`${controlPlane}/v1/projects/${encodeURIComponent(projectId)}/deployment`, {
        method: "PUT",
        headers: { authorization: `Bearer ${apiKey}`, "content-type": "application/json" },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(120_000)
    })
    if (!response.ok) throw new Error(`registerDeployment failed: HTTP ${response.status} ${await response.text()}`)
    log("deploy.registered", { projectId, organizationId, object, generation, secretRefs })
}

await main()
