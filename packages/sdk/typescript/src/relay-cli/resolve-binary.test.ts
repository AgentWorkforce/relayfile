import { existsSync, readFileSync } from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"

import {
  RelayfileBinaryNotFoundError,
  findSourceCheckoutRoot,
  platformBinaryName,
  resolveRelayfileBinary
} from "./resolve-binary.js"
import { checkoutRoot } from "./testing/build-binary.js"

/**
 * Binary resolution used to live in packages/cli/scripts/run.js. These tests
 * pin the behavior that moved here, including the fallbacks the bin shim
 * depends on.
 */

function fakeFs(present: readonly string[]): (candidate: string) => boolean {
  const set = new Set(present)
  return (candidate) => set.has(candidate)
}

describe("platformBinaryName", () => {
  it("maps node platform/arch onto the packaged Go binary names", () => {
    expect(platformBinaryName("linux", "x64")).toBe("relayfile-cli-linux-amd64")
    expect(platformBinaryName("linux", "arm64")).toBe("relayfile-cli-linux-arm64")
    expect(platformBinaryName("darwin", "arm64")).toBe("relayfile-cli-darwin-arm64")
    expect(platformBinaryName("win32", "x64")).toBe("relayfile-cli-windows-amd64.exe")
  })

  it("returns null for an unsupported target", () => {
    expect(platformBinaryName("aix", "x64")).toBeNull()
    expect(platformBinaryName("linux", "ppc64")).toBeNull()
  })
})

describe("resolveRelayfileBinary", () => {
  it("prefers a locally built generic binary over the packaged one", () => {
    const binDir = path.join("/pkg", "bin")
    const generic = path.join(binDir, "relayfile")
    const packaged = path.join(binDir, "relayfile-cli-linux-amd64")
    const resolution = resolveRelayfileBinary({
      binDirs: [binDir],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([generic, packaged])
    })
    expect(resolution).toEqual({
      kind: "binary",
      command: generic,
      args: [],
      binaryPath: generic
    })
  })

  it("falls back to the packaged per-platform binary", () => {
    const binDir = path.join("/pkg", "bin")
    const packaged = path.join(binDir, "relayfile-cli-darwin-arm64")
    const resolution = resolveRelayfileBinary({
      binDirs: [binDir],
      platform: "darwin",
      arch: "arm64",
      fileExists: fakeFs([packaged])
    })
    expect(resolution).toMatchObject({ kind: "binary", command: packaged })
  })

  it("uses the .exe name on Windows", () => {
    const binDir = path.join("/pkg", "bin")
    const generic = path.join(binDir, "relayfile.exe")
    const resolution = resolveRelayfileBinary({
      binDirs: [binDir],
      platform: "win32",
      arch: "x64",
      fileExists: fakeFs([generic])
    })
    expect(resolution).toMatchObject({ kind: "binary", command: generic })
  })

  it("searches bin directories in the order given", () => {
    const first = path.join("/first", "bin")
    const second = path.join("/second", "bin")
    const secondBinary = path.join(second, "relayfile")
    const resolution = resolveRelayfileBinary({
      binDirs: [first, second],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([secondBinary])
    })
    expect(resolution).toMatchObject({ command: secondBinary })
  })

  it("runs from Go source in a checkout when no binary is built", () => {
    // postinstall intentionally skips building the binary in a source
    // checkout; without this fallback the installed command is unusable there.
    const repoRoot = path.join("/work", "relayfile")
    const resolution = resolveRelayfileBinary({
      binDirs: [],
      searchFrom: [path.join(repoRoot, "packages", "cli", "scripts")],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([
        path.join(repoRoot, "go.mod"),
        path.join(repoRoot, "cmd", "relayfile-cli")
      ])
    })
    expect(resolution).toEqual({
      kind: "go-run",
      command: "go",
      args: ["run", "./cmd/relayfile-cli"],
      cwd: repoRoot
    })
  })

  it("throws a reinstall hint when nothing is usable", () => {
    expect(() =>
      resolveRelayfileBinary({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([])
      })
    ).toThrowError(RelayfileBinaryNotFoundError)

    try {
      resolveRelayfileBinary({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        platform: "aix",
        arch: "ppc64",
        fileExists: fakeFs([])
      })
    } catch (error) {
      expect((error as Error).message).toBe(
        "relayfile binary not found for aix ppc64. Reinstall the package or run postinstall again."
      )
    }
  })
})

describe("findSourceCheckoutRoot", () => {
  it("finds this repo from inside the SDK", () => {
    const root = checkoutRoot()
    expect(existsSync(path.join(root, "go.mod"))).toBe(true)
    expect(existsSync(path.join(root, "cmd", "relayfile-cli"))).toBe(true)
  })

  it("requires both go.mod and cmd/relayfile-cli", () => {
    const partial = path.join("/work", "other-go-project")
    expect(
      findSourceCheckoutRoot(partial, fakeFs([path.join(partial, "go.mod")]))
    ).toBeNull()
  })
})

describe("single implementation", () => {
  it("is the only place the repo looks for the relayfile binary", () => {
    // The bin shim must delegate here rather than keep its own copy of the
    // platform mapping.
    const shim = path.join(checkoutRoot(), "packages", "cli", "scripts", "run.js")
    const source = readFileSync(shim, "utf8")
    expect(source).toContain("resolveRelayfileBinary")
    expect(source).not.toContain("relayfile-cli-")
    expect(source).not.toMatch(/PLATFORM_MAP|ARCH_MAP/)
  })
})
