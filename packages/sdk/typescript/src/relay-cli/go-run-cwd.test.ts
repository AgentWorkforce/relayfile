import { existsSync, mkdirSync, realpathSync, writeFileSync } from "node:fs"
import path from "node:path"

import { beforeAll, describe, expect, it } from "vitest"

import {
  buildGoRunBinary,
  createRelayCliSurface,
  GoToolchainMissingError,
  goRunBinaryPath,
  resolveRelayfileBinary,
  type RelayfileGoRunResolution
} from "./index.js"
import { checkoutRoot, temporaryDirectory } from "./testing/build-binary.js"

/**
 * The source fallback must not move the caller.
 *
 * When no binary is installed the resolver falls back to running relayfile
 * from a Go source checkout. `go run` cannot express that: the program it
 * launches inherits the `go` command's working directory, and `go` only finds
 * the module from the checkout — so `relayfile read x --output out.json` run
 * from anywhere else wrote `out.json` into the repository. (`go -C <checkout>
 * run` behaves the same, and an absolute package path fails outside a module,
 * so building first is the only way to separate the two directories.)
 *
 * These tests pin the caller's directory, not the mechanism: a fixture
 * checkout that prints its own working directory, launched through the real
 * surface with a real spawn.
 */

let fixtureRoot: string

/**
 * A minimal source checkout: `go.mod` plus a `cmd/relayfile-cli` that reports
 * the working directory it was launched in.
 *
 * @returns The checkout root.
 */
function createFixtureCheckout(): string {
  const root = realpathSync(temporaryDirectory("go-run-checkout"))
  mkdirSync(path.join(root, "cmd", "relayfile-cli"), { recursive: true })
  writeFileSync(
    path.join(root, "go.mod"),
    "module example.com/relayfile-go-run-fixture\n\ngo 1.21\n"
  )
  writeFileSync(
    path.join(root, "cmd", "relayfile-cli", "main.go"),
    `package main

import (
	"fmt"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cwd=%s\\n", dir)
	fmt.Printf("args=%v\\n", os.Args[1:])
}
`
  )
  return root
}

/**
 * Resolution options that see the fixture checkout and nothing else.
 *
 * `resolveRelayfileBinary` always searches from this module's directory and
 * the process cwd, both inside the real relayfile checkout, which would
 * shadow the fixture with whatever the developer has built. Hiding the real
 * checkout from `fileExists` leaves exactly one candidate.
 *
 * @returns Options for `resolveRelayfileBinary`.
 */
function fixtureResolveOptions(): {
  binDirs: string[]
  resolveFrom: string[]
  pathEntries: string[]
  searchFrom: string[]
  env: NodeJS.ProcessEnv
  fileExists: (candidate: string) => boolean
} {
  const realRoot = realpathSync(checkoutRoot())
  return {
    binDirs: [],
    resolveFrom: [],
    pathEntries: [],
    searchFrom: [fixtureRoot],
    env: {},
    fileExists: (candidate: string) => {
      const resolved = path.resolve(candidate)
      if (resolved === realRoot || resolved.startsWith(realRoot + path.sep)) {
        return false
      }
      return existsSync(resolved)
    }
  }
}

async function runFromSource(
  argv: readonly string[],
  cwd?: string
): Promise<{ code: number; stdout: string; stderr: string }> {
  let stdout = ""
  let stderr = ""
  const code = await createRelayCliSurface({
    resolve: fixtureResolveOptions(),
    env: process.env,
    cwd,
    skipCloudPreflight: true
  }).run(argv, {
    stdout: (chunk) => {
      stdout += Buffer.from(chunk as Uint8Array).toString("utf8")
    },
    stderr: (chunk) => {
      stderr += Buffer.from(chunk as Uint8Array).toString("utf8")
    }
  })
  return { code, stdout, stderr }
}

beforeAll(() => {
  fixtureRoot = createFixtureCheckout()
}, 180_000)

describe.skipIf(process.platform === "win32")("source fallback honours the caller's cwd", () => {
  it("resolves the fixture checkout as a go-run fallback", () => {
    const resolution = resolveRelayfileBinary(fixtureResolveOptions())
    expect(resolution.kind).toBe("go-run")
    expect((resolution as RelayfileGoRunResolution).cwd).toBe(fixtureRoot)
  })

  it("runs in the requested directory, not the checkout", async () => {
    const callerDir = realpathSync(temporaryDirectory("go-run-caller"))
    const result = await runFromSource(["read", "/x", "--output", "out.json"], callerDir)

    expect(result.stderr).toBe("")
    expect(result.code).toBe(0)
    // The bug: this was the checkout root, so `--output out.json` landed there.
    expect(result.stdout).toContain(`cwd=${callerDir}\n`)
    expect(result.stdout).not.toContain(`cwd=${fixtureRoot}\n`)
  }, 180_000)

  it("forwards argv unchanged, with no `go run` prefix left in it", async () => {
    const callerDir = realpathSync(temporaryDirectory("go-run-argv"))
    const result = await runFromSource(["tree", "/", "--depth", "2"], callerDir)

    expect(result.code).toBe(0)
    expect(result.stdout).toContain("args=[tree / --depth 2]\n")
  }, 180_000)

  it("falls back to the process cwd when the host names none", async () => {
    const result = await runFromSource(["status"])

    expect(result.code).toBe(0)
    expect(result.stdout).toContain(`cwd=${realpathSync(process.cwd())}\n`)
  }, 180_000)

  it("builds outside the checkout so a working tree is never written to", () => {
    const resolution = resolveRelayfileBinary(
      fixtureResolveOptions()
    ) as RelayfileGoRunResolution
    const built = buildGoRunBinary(resolution, { env: process.env })

    expect(built).toBe(goRunBinaryPath(fixtureRoot))
    expect(existsSync(built)).toBe(true)
    expect(path.resolve(built).startsWith(fixtureRoot + path.sep)).toBe(false)
  }, 180_000)

  it("reports a missing Go toolchain instead of throwing at the host", async () => {
    const resolution = resolveRelayfileBinary(
      fixtureResolveOptions()
    ) as RelayfileGoRunResolution

    expect(() =>
      buildGoRunBinary(resolution, {
        env: { PATH: temporaryDirectory("go-run-empty-path") },
        outputPath: path.join(temporaryDirectory("go-run-missing"), "relayfile-cli")
      })
    ).toThrow(GoToolchainMissingError)
  }, 60_000)
})
