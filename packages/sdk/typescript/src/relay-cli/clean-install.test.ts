import { cpSync, existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
import path from "node:path"

import { beforeAll, describe, expect, it } from "vitest"

import { RELAY_CLI_EXIT_BINARY_NOT_FOUND } from "./index.js"
import {
  buildRelayfileBinary,
  buildSdkDist,
  checkoutRoot,
  temporaryDirectory
} from "./testing/build-binary.js"

/**
 * `agent-relay file <cmd>` shipped once resolving no binary at all on a clean
 * machine: `agent-relay` depends on `@relayfile/sdk`, never on `relayfile`, so
 * the `relayfile` package's postinstall download never ran for it. The tests
 * that were supposed to catch that passed anyway, because they ran inside this
 * checkout where a built binary and a Go toolchain are both sitting right
 * there.
 *
 * These tests remove that blind spot. Each one builds a directory shaped like
 * a real `npm install` result — the SDK and the platform package under
 * `node_modules`, nothing else — under the OS temp dir, where there is no
 * `go.mod` above it and no `relayfile` package anywhere. A probe script is
 * then run with plain `node`, so the SDK is loaded by package name through its
 * real `exports` map, the platform package is found by the real
 * `require.resolve`, and the real Go binary is spawned. Nothing is stubbed, and
 * nothing about the host checkout can make them pass.
 */

const root = checkoutRoot()
const sdkRoot = path.join(root, "packages", "sdk", "typescript")

interface CleanInstall {
  /** Root of the fake install; the probe runs with this as cwd. */
  directory: string
  /** The `@relayfile/cli-<platform>-<arch>` package directory inside it. */
  platformPackageDir: string
}

let relayfileBinary: string

beforeAll(() => {
  buildSdkDist()
  const built = buildRelayfileBinary()
  relayfileBinary = path.join(
    built.binDir,
    process.platform === "win32" ? "relayfile.exe" : "relayfile"
  )
  // 4 minutes: a cold `tsc` plus a cold `go build` on a clean module cache.
}, 240_000)

/**
 * Assemble a directory that looks like a consumer's `node_modules` after
 * `npm install @relayfile/sdk`.
 *
 * @param label - Included in the temp directory name.
 * @param options - `withPlatformPackage: false` simulates `--omit=optional`.
 * @returns Paths inside the assembled tree.
 */
function cleanInstall(
  label: string,
  options: { withPlatformPackage?: boolean } = {}
): CleanInstall {
  const directory = temporaryDirectory(`clean-install-${label}`)
  const modules = path.join(directory, "node_modules", "@relayfile")

  // The SDK, exactly as published: manifest plus dist. Its `exports` map is
  // what makes `import("@relayfile/sdk/relay-cli")` resolve.
  const sdkTarget = path.join(modules, "sdk")
  mkdirSync(sdkTarget, { recursive: true })
  cpSync(path.join(sdkRoot, "package.json"), path.join(sdkTarget, "package.json"))
  cpSync(path.join(sdkRoot, "dist"), path.join(sdkTarget, "dist"), { recursive: true })

  const platformPackageDir = path.join(
    modules,
    `cli-${process.platform}-${process.arch}`
  )
  mkdirSync(path.join(platformPackageDir, "bin"), { recursive: true })
  writeFileSync(
    path.join(platformPackageDir, "package.json"),
    JSON.stringify(
      {
        name: `@relayfile/cli-${process.platform}-${process.arch}`,
        version: "0.0.0-clean-install",
        files: ["bin"],
        os: [process.platform],
        cpu: [process.arch]
      },
      null,
      2
    )
  )
  if (options.withPlatformPackage !== false) {
    cpSync(
      relayfileBinary,
      path.join(
        platformPackageDir,
        "bin",
        process.platform === "win32" ? "relayfile-cli.exe" : "relayfile-cli"
      )
    )
  }

  writeFileSync(
    path.join(directory, "package.json"),
    JSON.stringify({ name: "clean-install-consumer", private: true }, null, 2)
  )

  // No go.mod and no cmd/relayfile-cli above the temp dir, so neither the
  // source-checkout step nor the `go run` fallback can rescue a failed lookup.
  expect(existsSync(path.join(directory, "go.mod"))).toBe(false)
  expect(existsSync(path.join(directory, "node_modules", "relayfile"))).toBe(false)

  return { directory, platformPackageDir }
}

interface ProbeResult {
  code: number
  stdout: string
  stderr: string
  binaryPath?: string
  failed?: string
}

/**
 * Run the surface inside a clean install and report what it did.
 *
 * `PATH` is emptied for the probe so the last-resort PATH scan cannot find a
 * binary this machine happens to have, and `RELAYFILE_CLI_BIN` is cleared so
 * an ambient override cannot mask a resolution failure.
 *
 * @param install - The tree to run in.
 * @param argv - Arguments for the surface.
 * @returns The surface's exit code and captured output.
 */
function probe(install: CleanInstall, argv: readonly string[]): ProbeResult {
  const script = path.join(install.directory, "probe.mjs")
  writeFileSync(
    script,
    `import { createRelayCliSurface, resolveRelayfileBinary } from "@relayfile/sdk/relay-cli"

const argv = JSON.parse(process.argv[2])
let stdout = ""
let stderr = ""
const surface = createRelayCliSurface({ skipCloudPreflight: true })
const code = await surface.run(argv, {
  stdout: (chunk) => {
    stdout += chunk
  },
  stderr: (chunk) => {
    stderr += chunk
  }
})

let binaryPath
let failed
try {
  const resolution = resolveRelayfileBinary()
  binaryPath = resolution.kind === "binary" ? resolution.binaryPath : resolution.command
} catch (error) {
  failed = error.message
}

process.stderr.write("<<PROBE>>" + JSON.stringify({ code, stdout, stderr, binaryPath, failed }))
`
  )

  const result = spawnSync(process.execPath, [script, JSON.stringify(argv)], {
    cwd: install.directory,
    encoding: "utf8",
    // A bare env: no PATH for the fallback scan, no RELAYFILE_CLI_BIN override.
    env: { PATH: "", HOME: install.directory }
  })
  if (result.error) {
    throw result.error
  }
  const marker = (result.stderr ?? "").indexOf("<<PROBE>>")
  if (marker === -1) {
    throw new Error(
      `probe produced no result (exit ${result.status})\n${result.stdout}\n${result.stderr}`
    )
  }
  return JSON.parse(result.stderr.slice(marker + "<<PROBE>>".length)) as ProbeResult
}

describe("clean install of @relayfile/sdk", () => {
  it("runs the real binary out of the platform package", () => {
    const install = cleanInstall("resolves")
    const result = probe(install, ["--version"])

    expect(result.failed).toBeUndefined()
    expect(result.binaryPath).toBe(
      path.join(
        install.platformPackageDir,
        "bin",
        process.platform === "win32" ? "relayfile-cli.exe" : "relayfile-cli"
      )
    )
    // The real Go binary printed its real version and returned its real code.
    expect(result.code).toBe(0)
    expect(result.stdout.trim()).toMatch(/^\d+\.\d+\.\d+/)
    expect(result.stderr).toBe("")
  }, 60_000)

  it("dispatches a real command through the platform package binary", () => {
    const install = cleanInstall("dispatch")
    const result = probe(install, ["export", "--help"])

    expect(result.code).toBe(0)
    expect(result.stdout).toContain("--format")
  }, 60_000)

  it("tells the user what to install when the platform package is absent", () => {
    // What `npm install --omit=optional` leaves behind, and what shipped
    // before these packages existed.
    const install = cleanInstall("omit-optional", { withPlatformPackage: false })
    rmSync(install.platformPackageDir, { recursive: true, force: true })

    const result = probe(install, ["--version"])

    expect(result.code).toBe(RELAY_CLI_EXIT_BINARY_NOT_FOUND)
    expect(result.stdout).toBe("")
    expect(result.stderr).toContain(
      `@relayfile/cli-${process.platform}-${process.arch}`
    )
    expect(result.stderr).toContain("--include=optional")
    expect(result.stderr).toContain("RELAYFILE_CLI_BIN")
    // Not a bare ENOENT, and not a stack trace at the host.
    expect(result.stderr).not.toContain("ENOENT")
    expect(result.failed).toContain(
      `@relayfile/cli-${process.platform}-${process.arch}`
    )
  }, 60_000)

  it("still resolves when only RELAYFILE_CLI_BIN is set", () => {
    const install = cleanInstall("env-override", { withPlatformPackage: false })
    rmSync(install.platformPackageDir, { recursive: true, force: true })

    const script = path.join(install.directory, "override.mjs")
    writeFileSync(
      script,
      `import { resolveRelayfileBinary } from "@relayfile/sdk/relay-cli"
const resolution = resolveRelayfileBinary()
process.stdout.write(resolution.binaryPath)
`
    )
    const result = spawnSync(process.execPath, [script], {
      cwd: install.directory,
      encoding: "utf8",
      env: { PATH: "", HOME: install.directory, RELAYFILE_CLI_BIN: relayfileBinary }
    })
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toBe(relayfileBinary)
  }, 60_000)
})
