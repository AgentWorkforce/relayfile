/**
 * Register the relayfile Workspace actor with a self-hosted durable-actors
 * control plane (the GKE provider requires a compiled GCS bundle; it refuses
 * localSource). Runs under Bun inside the relayfile-actors-workspace image:
 *
 *   bun /app/actor/deploy.ts
 *
 * 1. compile src/actors.ts with the durable-actors SDK compiler -> actors.mjs + contract
 * 2. upload actors.mjs to gs://$DURABLE_ACTORS_ARTIFACT_BUCKET/durable-actors/v3/artifacts/<uuid>/
 *    (uuid derived from the bytes; create-only, so re-runs are idempotent)
 * 3. PUT /v1/projects/$DURABLE_ACTORS_PROJECT_ID/deployment with the bundle and contract
 *
 * Env: DURABLE_ACTORS_CONTROL_PLANE_URL, DURABLE_ACTORS_SECRET (admin api-key),
 * DURABLE_ACTORS_PROJECT_ID, DURABLE_ACTORS_ORGANIZATION_ID,
 * DURABLE_ACTORS_ARTIFACT_BUCKET, DURABLE_ACTORS_SECRET_REFS (optional, comma separated),
 * STORAGE_EMULATOR_HOST (optional, tests). GCS credentials come from the
 * metadata server (Workload Identity).
 */
import { execFile } from "node:child_process"
import { mkdtemp, readFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import path from "node:path"
import { promisify } from "node:util"
import { artifactObject, artifactUuid, deploymentBody, parseSecretRefs } from "../src/deploy/plan.js"

const env = (name: string, fallback?: string): string => {
    const value = process.env[name]?.trim() || fallback
    if (!value) throw new Error(`${name} is required`)
    return value
}

const log = (event: string, fields: Record<string, unknown> = {}) =>
    console.log(JSON.stringify({ ts: new Date().toISOString(), event, ...fields }))

async function gcsToken(): Promise<Record<string, string>> {
    if (process.env.STORAGE_EMULATOR_HOST) return {}
    const response = await fetch(
        "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token",
        { headers: { "Metadata-Flavor": "Google" }, signal: AbortSignal.timeout(5_000) }
    )
    if (!response.ok) throw new Error(`metadata token request failed: HTTP ${response.status}`)
    const body = (await response.json()) as { access_token: string }
    return { authorization: `Bearer ${body.access_token}` }
}

function gcsBase(): string {
    const emulator = process.env.STORAGE_EMULATOR_HOST?.trim()
    if (!emulator) return "https://storage.googleapis.com"
    return (/^https?:\/\//.test(emulator) ? emulator : `http://${emulator}`).replace(/\/$/, "")
}

/** Create-only upload; returns the object's generation (existing or new). */
async function uploadImmutable(bucket: string, object: string, bytes: Uint8Array): Promise<number> {
    const base = gcsBase()
    const headers = await gcsToken()
    const url = new URL(`${base}/upload/storage/v1/b/${encodeURIComponent(bucket)}/o`)
    url.searchParams.set("uploadType", "media")
    url.searchParams.set("name", object)
    url.searchParams.set("ifGenerationMatch", "0")
    const upload = await fetch(url, {
        method: "POST",
        headers: { ...headers, "content-type": "text/javascript" },
        body: bytes,
        signal: AbortSignal.timeout(120_000)
    })
    if (upload.ok) return Number(((await upload.json()) as { generation: string }).generation)
    if (upload.status !== 412) throw new Error(`artifact upload failed: HTTP ${upload.status} ${await upload.text()}`)
    const meta = await fetch(`${base}/storage/v1/b/${encodeURIComponent(bucket)}/o/${encodeURIComponent(object)}`, {
        headers,
        signal: AbortSignal.timeout(30_000)
    })
    if (!meta.ok) throw new Error(`artifact metadata read failed: HTTP ${meta.status}`)
    return Number(((await meta.json()) as { generation: string }).generation)
}

async function main(): Promise<void> {
    const controlPlane = env("DURABLE_ACTORS_CONTROL_PLANE_URL").replace(/\/$/, "")
    const apiKey = env("DURABLE_ACTORS_SECRET")
    const projectId = env("DURABLE_ACTORS_PROJECT_ID", "relayfile")
    const organizationId = env("DURABLE_ACTORS_ORGANIZATION_ID", projectId)
    const bucket = env("DURABLE_ACTORS_ARTIFACT_BUCKET")
    const secretRefs = parseSecretRefs(process.env.DURABLE_ACTORS_SECRET_REFS)

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

    const generation = await uploadImmutable(bucket, object, bytes)
    log("deploy.uploaded", { bucket, object, generation })

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
