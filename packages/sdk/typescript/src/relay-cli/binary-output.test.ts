import { chmodSync, mkdirSync, writeFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
import path from "node:path"

import { beforeAll, describe, expect, it } from "vitest"

import { createRelayCliSurface } from "./index.js"
import { buildRelayfileBinary, temporaryDirectory } from "./testing/build-binary.js"

/**
 * `relayfile export --format tar --output -` streams a tar archive to stdout.
 * Mounted as a CLI surface, that output crosses an `io.stdout(chunk)` boundary,
 * and decoding the chunks as UTF-8 destroys the archive without any error:
 * every byte that is not valid UTF-8 comes out as U+FFFD. The same decode
 * corrupts ordinary text when a multibyte character lands across a chunk
 * boundary.
 *
 * So the surface hands `io` the raw bytes. These tests pin that. The archive
 * itself needs a live workspace and a server, which a unit test cannot have —
 * what is checked here instead is the plumbing that carries it: a real spawn
 * through the real resolver, with a fixture binary as the payload producer,
 * emitting bytes no UTF-8 decode survives. The text half is then checked
 * against the real relayfile binary.
 */

/** Bytes chosen to fail under any UTF-8 decode, plus a split multibyte char. */
const INVALID_UTF8 = Buffer.from([0x1f, 0x8b, 0x08, 0x00, 0xff, 0xfe, 0x00, 0x80])
const MULTIBYTE = Buffer.from("héllo — ✅", "utf8")

let realBinDir: string

beforeAll(() => {
  realBinDir = buildRelayfileBinary().binDir
  // 3 minutes: a cold `go build` of cmd/relayfile-cli.
}, 180_000)

/**
 * A `bin/` directory holding a fake `relayfile` that writes fixed bytes to
 * stdout, so a payload the real binary can only produce against a live server
 * can still be pushed through the surface's stdio path.
 *
 * @param chunks - Byte chunks to write, one write each.
 * @returns The bin directory to hand `resolveRelayfileBinary`.
 */
function fixtureBinDir(chunks: readonly Buffer[]): string {
  const binDir = path.join(temporaryDirectory("binary-output"), "bin")
  mkdirSync(binDir, { recursive: true })
  const script = path.join(binDir, "emit.mjs")
  writeFileSync(
    script,
    `const chunks = ${JSON.stringify(chunks.map((chunk) => chunk.toString("base64")))}
for (const chunk of chunks) {
  process.stdout.write(Buffer.from(chunk, "base64"))
}
process.exitCode = 0
`
  )
  const shim = path.join(binDir, "relayfile")
  writeFileSync(
    shim,
    `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} ${JSON.stringify(script)}\n`
  )
  chmodSync(shim, 0o755)
  return binDir
}

interface Capture {
  code: number
  stdout: Buffer
  stderr: Buffer
}

async function invoke(binDir: string, argv: readonly string[]): Promise<Capture> {
  const stdout: Buffer[] = []
  const stderr: Buffer[] = []
  const code = await createRelayCliSurface({
    resolve: { binDirs: [binDir] },
    skipCloudPreflight: true
  }).run(argv, {
    stdout: (chunk) => {
      stdout.push(Buffer.from(chunk as Uint8Array))
    },
    stderr: (chunk) => {
      stderr.push(Buffer.from(chunk as Uint8Array))
    }
  })
  return { code, stdout: Buffer.concat(stdout), stderr: Buffer.concat(stderr) }
}

describe("stdout is byte-exact", () => {
  it.skipIf(process.platform === "win32")(
    "carries bytes no UTF-8 decode would survive",
    async () => {
      const binDir = fixtureBinDir([INVALID_UTF8])
      const result = await invoke(binDir, ["export", "--format", "tar", "--output", "-"])

      expect(result.code).toBe(0)
      expect(result.stdout.equals(INVALID_UTF8)).toBe(true)
      // What the old string sink produced instead: replacement characters.
      expect(result.stdout.includes(Buffer.from("�", "utf8"))).toBe(false)
    },
    60_000
  )

  it.skipIf(process.platform === "win32")(
    "keeps a multibyte character intact across two writes",
    async () => {
      // Split mid-character: a per-chunk decode turns each half into U+FFFD.
      const split = MULTIBYTE.length - 1
      const binDir = fixtureBinDir([
        MULTIBYTE.subarray(0, split),
        MULTIBYTE.subarray(split)
      ])
      const result = await invoke(binDir, ["read", "/x"])

      expect(result.stdout.equals(MULTIBYTE)).toBe(true)
      expect(result.stdout.toString("utf8")).toBe(MULTIBYTE.toString("utf8"))
    },
    60_000
  )

  it("matches the real binary's own stdout byte for byte", async () => {
    // The real thing, not a fixture: `--help` output through the surface must
    // be identical to spawning the binary directly.
    const argv = ["export", "--help"]
    const throughSurface = await invoke(realBinDir, argv)
    const direct = spawnSync(
      path.join(realBinDir, process.platform === "win32" ? "relayfile.exe" : "relayfile"),
      argv
    )

    expect(throughSurface.code).toBe(direct.status)
    expect(throughSurface.stdout.equals(direct.stdout)).toBe(true)
    expect(throughSurface.stderr.equals(direct.stderr)).toBe(true)
  }, 60_000)
})
