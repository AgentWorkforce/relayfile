import { mkdirSync, rmSync, writeFileSync } from "node:fs"
import { createServer } from "node:http"
import path from "node:path"
import { beforeAll, expect, it } from "vitest"
import { createRelayCliSurface } from "./index.js"
import { buildRelayfileBinary, temporaryDirectory } from "./testing/build-binary.js"

let binDir: string
beforeAll(() => {
  binDir = buildRelayfileBinary().binDir
}, 180_000)

it.skipIf(process.platform === "win32")("sends typed metadata through the mounted surface and authorized workspace resolver", async () => {
  const home = temporaryDirectory("metadata-home")
  const calls: string[] = []
  let received: unknown
  const server = createServer(async (request, response) => {
    calls.push(`${request.method} ${request.url}`)
    expect(request.headers.authorization).toBe("Bearer cld_fixture")
    response.setHeader("Content-Type", "application/json")
    if (request.method === "GET" && request.url === "/api/v1/workspaces/rw_fixture/resolve") {
      response.end(JSON.stringify({ cloudWorkspaceId: "cloud_fixture", relayfileWorkspaceId: "rw_fixture" }))
    } else if (request.method === "PUT" && request.url === "/api/v1/workspaces/cloud_fixture/integrations/gitlab/metadata") {
      let body = ""
      for await (const chunk of request) body += chunk
      received = JSON.parse(body)
      response.end('{"ok":true}')
    } else {
      response.statusCode = 500
      response.end('{"error":"unexpected local request"}')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve))
  try {
    const address = server.address()
    if (!address || typeof address === "string") throw new Error("missing local test address")
    const apiUrl = `http://127.0.0.1:${address.port}`
    const store = path.join(home, ".agentworkforce", "relay")
    mkdirSync(store, { recursive: true })
    writeFileSync(path.join(store, "cloud-auth.json"), JSON.stringify({
      apiUrl,
      accessToken: "cld_fixture",
      refreshToken: "cld_rt_fixture",
      accessTokenExpiresAt: new Date(Date.now() + 86_400_000).toISOString()
    }), { mode: 0o600 })
    mkdirSync(path.join(home, ".relayfile"))
    writeFileSync(path.join(home, ".relayfile", "workspaces.json"), JSON.stringify({
      workspaces: [{ name: "fixture", id: "rw_fixture" }]
    }))
    const metadata = { projectIds: ["12345"], manageWebhooks: false, webhookSecret: "private-test-fixture" }
    const input = path.join(home, "metadata.json")
    writeFileSync(input, JSON.stringify(metadata), { mode: 0o600 })
    const output = { stdout: "", stderr: "" }
    const surface = createRelayCliSurface({
      resolve: { binDirs: [binDir] },
      // A bare environment and temporary HOME exclude all operator credentials.
      env: { HOME: home, USERPROFILE: home, PATH: "", AGENT_RELAY_HOME: store },
      skipCloudPreflight: true
    })
    const code = await surface.run([
      "integration", "set-metadata", "gitlab", "--workspace", "fixture",
      "--metadata-file", input, "--cloud-api-url", apiUrl, "--yes"
    ], {
      stdout: (chunk) => { output.stdout += chunk },
      stderr: (chunk) => { output.stderr += chunk }
    })
    expect({ code, stderr: output.stderr }).toEqual({ code: 0, stderr: "" })
    expect(received).toEqual({ metadata })
    expect(calls).toEqual([
      "GET /api/v1/workspaces/rw_fixture/resolve",
      "PUT /api/v1/workspaces/cloud_fixture/integrations/gitlab/metadata"
    ])
    expect(output.stdout).toContain("gitlab metadata updated in workspace fixture")
    expect(output.stdout + output.stderr).not.toContain(metadata.webhookSecret)
  } finally {
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
    rmSync(home, { recursive: true, force: true })
  }
})
