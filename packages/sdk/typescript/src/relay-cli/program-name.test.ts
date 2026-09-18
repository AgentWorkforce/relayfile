import { chmodSync, mkdirSync, writeFileSync } from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"

import { createRelayCliSurface } from "./index.js"
import { temporaryDirectory } from "./testing/build-binary.js"

/**
 * The binary writes messages that tell users to run something:
 *
 *   credentials not found at …; run relayfile login --api-key …
 *
 * Reached through `agent-relay file`, that names a binary the user does not
 * have — they installed `agent-relay`. The binary cannot know it was mounted,
 * so the surface tells it, and the Go side formats those messages with the
 * name it is given (relayfile#509).
 *
 * Asserted against a real spawn rather than a stubbed one, because the value
 * has to survive the env the surface actually builds.
 */
function echoEnvBinDir(): string {
  const binDir = path.join(temporaryDirectory("program-name"), "bin")
  mkdirSync(binDir, { recursive: true })
  const script = path.join(binDir, "echo-env.mjs")
  writeFileSync(
    script,
    `process.stdout.write(process.env.RELAYFILE_PROGRAM_NAME ?? "<unset>")\n`
  )
  const shim = path.join(binDir, "relayfile")
  writeFileSync(
    shim,
    `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} ${JSON.stringify(script)}\n`
  )
  chmodSync(shim, 0o755)
  return binDir
}

async function programNameSeenByBinary(
  env?: NodeJS.ProcessEnv
): Promise<string> {
  const chunks: Buffer[] = []
  await createRelayCliSurface({
    resolve: { binDirs: [echoEnvBinDir()] },
    skipCloudPreflight: true,
    ...(env ? { env } : {})
  }).run(["status"], {
    stdout: (chunk) =>
      chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : Buffer.from(chunk)),
    stderr: () => {}
  })
  return Buffer.concat(chunks).toString("utf8")
}

describe("mounted program name", () => {
  it("tells the binary it was reached through agent-relay file", async () => {
    expect(await programNameSeenByBinary()).toBe("agent-relay file")
  })

  it("sets it even when the caller supplies its own env", async () => {
    // A host passing `env` must not accidentally drop the name and send users
    // back to a binary they do not have.
    const seen = await programNameSeenByBinary({ PATH: process.env["PATH"] ?? "" })
    expect(seen).toBe("agent-relay file")
  })
})
