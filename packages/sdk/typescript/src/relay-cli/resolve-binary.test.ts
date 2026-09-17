import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"

import {
  RELAYFILE_CLI_BIN_ENV,
  RelayfileBinaryNotFoundError,
  findSourceCheckoutRoot,
  formatBinaryNotFoundMessage,
  platformBinaryName,
  platformPackageBinaryName,
  platformPackageName,
  platformPackageNames,
  resolveRelayfileBinary,
  type ResolveRelayfileBinaryOptions
} from "./resolve-binary.js"
import { checkoutRoot, temporaryDirectory } from "./testing/build-binary.js"

/**
 * Binary resolution used to live in packages/cli/scripts/run.js. These tests
 * pin the behavior that moved here, including the fallbacks the bin shim
 * depends on, and the `@relayfile/cli-<platform>-<arch>` platform packages
 * that are the only path a consumer of @relayfile/sdk has when it does not
 * also depend on `relayfile`.
 */

function fakeFs(present: readonly string[]): (candidate: string) => boolean {
  const set = new Set(present)
  return (candidate) => set.has(candidate)
}

/**
 * Resolve with every ambient input sealed off.
 *
 * The real resolver reads `RELAYFILE_CLI_BIN`, `PATH`, and the installed
 * `@relayfile/cli-*` package. A test that leaves any of those live passes or
 * fails based on the host it runs on — which is exactly the blind spot that
 * let `agent-relay file` ship with no binary. Every knob is closed here and
 * opened one at a time.
 */
function resolve(
  options: ResolveRelayfileBinaryOptions = {}
): ReturnType<typeof resolveRelayfileBinary> {
  return resolveRelayfileBinary({
    env: {},
    resolveFrom: [],
    pathEntries: [],
    ...options
  })
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

describe("platformPackageName", () => {
  it("names the optional dependency for every published target", () => {
    expect(platformPackageName("linux", "x64")).toBe("@relayfile/cli-linux-x64")
    expect(platformPackageName("darwin", "arm64")).toBe("@relayfile/cli-darwin-arm64")
    expect(platformPackageName("win32", "x64")).toBe("@relayfile/cli-win32-x64")
  })

  it("returns null where no package is published", () => {
    expect(platformPackageName("aix", "ppc64")).toBeNull()
    expect(platformPackageName("linux", "ppc64")).toBeNull()
  })

  it("uses the Go binary's own name inside the package, .exe on Windows", () => {
    // Not `relayfile`: the `relayfile` package installs that name, and a
    // machine can have both packages.
    expect(platformPackageBinaryName("linux")).toBe("relayfile-cli")
    expect(platformPackageBinaryName("darwin")).toBe("relayfile-cli")
    expect(platformPackageBinaryName("win32")).toBe("relayfile-cli.exe")
  })
})

describe("resolveRelayfileBinary", () => {
  it("prefers the platform package over every other candidate", () => {
    // The production path for `agent-relay file`: agent-relay depends on
    // @relayfile/sdk, never on `relayfile`, so this is the only binary that
    // exists in its tree.
    const pkgRoot = temporaryDirectory("cli-platform-pkg")
    const pkgDir = path.join(pkgRoot, "node_modules", "@relayfile", "cli-linux-x64")
    mkdirSync(path.join(pkgDir, "bin"), { recursive: true })
    writeFileSync(
      path.join(pkgDir, "package.json"),
      JSON.stringify({ name: "@relayfile/cli-linux-x64", version: "0.0.0-test" })
    )
    const packaged = path.join(pkgDir, "bin", "relayfile-cli")
    writeFileSync(packaged, "#!/bin/sh\nexit 0\n")
    writeFileSync(path.join(pkgRoot, "package.json"), JSON.stringify({ name: "consumer" }))

    const otherBinDir = path.join("/pkg", "bin")
    const resolution = resolve({
      resolveFrom: [path.join(pkgRoot, "package.json")],
      binDirs: [otherBinDir],
      platform: "linux",
      arch: "x64",
      // The `relayfile` package's own binary is present too and must lose.
      fileExists: (candidate) =>
        candidate === packaged || candidate === path.join(otherBinDir, "relayfile")
    })

    expect(resolution).toEqual({
      kind: "binary",
      command: packaged,
      args: [],
      binaryPath: packaged
    })
  })

  it("looks for the .exe inside the win32 platform package", () => {
    const pkgRoot = temporaryDirectory("cli-platform-pkg-win32")
    const pkgDir = path.join(pkgRoot, "node_modules", "@relayfile", "cli-win32-x64")
    mkdirSync(path.join(pkgDir, "bin"), { recursive: true })
    writeFileSync(
      path.join(pkgDir, "package.json"),
      JSON.stringify({ name: "@relayfile/cli-win32-x64", version: "0.0.0-test" })
    )
    const packaged = path.join(pkgDir, "bin", "relayfile-cli.exe")
    writeFileSync(path.join(pkgRoot, "package.json"), JSON.stringify({ name: "consumer" }))

    const resolution = resolve({
      resolveFrom: [path.join(pkgRoot, "package.json")],
      binDirs: [],
      platform: "win32",
      arch: "x64",
      fileExists: fakeFs([packaged])
    })
    expect(resolution).toMatchObject({ kind: "binary", command: packaged })
  })

  it("ignores a platform package that has no binary in it", () => {
    // The checked-in skeleton has bin/.gitkeep and nothing else; it must not
    // short-circuit the chain in a source checkout.
    const pkgRoot = temporaryDirectory("cli-platform-pkg-empty")
    const pkgDir = path.join(pkgRoot, "node_modules", "@relayfile", "cli-linux-x64")
    mkdirSync(path.join(pkgDir, "bin"), { recursive: true })
    writeFileSync(
      path.join(pkgDir, "package.json"),
      JSON.stringify({ name: "@relayfile/cli-linux-x64", version: "0.0.0-test" })
    )
    writeFileSync(path.join(pkgRoot, "package.json"), JSON.stringify({ name: "consumer" }))

    const binDir = path.join("/pkg", "bin")
    const fallback = path.join(binDir, "relayfile")
    const resolution = resolve({
      resolveFrom: [path.join(pkgRoot, "package.json")],
      binDirs: [binDir],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([fallback])
    })
    expect(resolution).toMatchObject({ command: fallback })
  })

  it("prefers a locally built generic binary over the packaged one", () => {
    const binDir = path.join("/pkg", "bin")
    const generic = path.join(binDir, "relayfile")
    const packaged = path.join(binDir, "relayfile-cli-linux-amd64")
    const resolution = resolve({
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
    const resolution = resolve({
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
    const resolution = resolve({
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
    const resolution = resolve({
      binDirs: [first, second],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([secondBinary])
    })
    expect(resolution).toMatchObject({ command: secondBinary })
  })

  it("finds make build and make release outputs in a checkout", () => {
    const repoRoot = path.join("/work", "relayfile")
    const checkout = [
      path.join(repoRoot, "go.mod"),
      path.join(repoRoot, "cmd", "relayfile-cli")
    ]
    const madeBinary = path.join(repoRoot, "bin", "relayfile-cli")
    expect(
      resolve({
        binDirs: [],
        searchFrom: [repoRoot],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([...checkout, madeBinary])
      })
    ).toMatchObject({ kind: "binary", command: madeBinary })

    // `make release` writes Go-named files into dist/.
    const released = path.join(repoRoot, "dist", "relayfile-cli-linux-amd64")
    expect(
      resolve({
        binDirs: [],
        searchFrom: [repoRoot],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([...checkout, released])
      })
    ).toMatchObject({ kind: "binary", command: released })
  })

  it("finds the .exe those builds write in a Windows checkout", () => {
    // `go build -o bin/relayfile-cli` appends `.exe` for GOOS=windows, and the
    // dist name carries it too. Without the suffix a successful `make build`
    // is invisible and resolution falls through to `go run`.
    const repoRoot = path.join("/work", "relayfile")
    const checkout = [
      path.join(repoRoot, "go.mod"),
      path.join(repoRoot, "cmd", "relayfile-cli")
    ]
    const madeBinary = path.join(repoRoot, "bin", "relayfile-cli.exe")
    expect(
      resolve({
        binDirs: [],
        searchFrom: [repoRoot],
        platform: "win32",
        arch: "x64",
        fileExists: fakeFs([...checkout, madeBinary])
      })
    ).toMatchObject({ kind: "binary", command: madeBinary })

    // The dist name is the one build-cli-npm-packages.mjs looks for, which is
    // exactly `platformBinaryName`.
    const released = path.join(
      repoRoot,
      "dist",
      platformBinaryName("win32", "x64") ?? ""
    )
    expect(path.basename(released)).toBe("relayfile-cli-windows-amd64.exe")
    expect(
      resolve({
        binDirs: [],
        searchFrom: [repoRoot],
        platform: "win32",
        arch: "x64",
        fileExists: fakeFs([...checkout, released])
      })
    ).toMatchObject({ kind: "binary", command: released })
  })

  it("runs from Go source in a checkout when no binary is built", () => {
    // postinstall intentionally skips building the binary in a source
    // checkout; without this fallback the installed command is unusable there.
    const repoRoot = path.join("/work", "relayfile")
    const resolution = resolve({
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

  it("honors RELAYFILE_CLI_BIN ahead of everything else", () => {
    const pinned = path.join("/opt", "relayfile", "relayfile-cli")
    const binDir = path.join("/pkg", "bin")
    const resolution = resolve({
      env: { [RELAYFILE_CLI_BIN_ENV]: pinned },
      binDirs: [binDir],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([pinned, path.join(binDir, "relayfile")])
    })
    expect(resolution).toMatchObject({ kind: "binary", command: pinned })
  })

  it("ignores RELAYFILE_CLI_BIN when it points at nothing", () => {
    const binDir = path.join("/pkg", "bin")
    const fallback = path.join(binDir, "relayfile")
    const resolution = resolve({
      env: { [RELAYFILE_CLI_BIN_ENV]: path.join("/gone", "relayfile-cli") },
      binDirs: [binDir],
      platform: "linux",
      arch: "x64",
      fileExists: fakeFs([fallback])
    })
    expect(resolution).toMatchObject({ command: fallback })
  })

  it("scans PATH last, and only for relayfile-cli", () => {
    // Never the generic `relayfile`: on PATH that is the npm bin shim, which
    // resolves through this module, so spawning it would recurse forever.
    const pathDir = path.join("/usr", "local", "bin")
    const onPath = path.join(pathDir, "relayfile-cli")
    expect(
      resolve({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        pathEntries: [pathDir],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([onPath])
      })
    ).toMatchObject({ kind: "binary", command: onPath })

    expect(() =>
      resolve({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        pathEntries: [pathDir],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([path.join(pathDir, "relayfile")])
      })
    ).toThrowError(RelayfileBinaryNotFoundError)
  })

  it("names the package to install when nothing is usable", () => {
    // Not a bare ENOENT: the prebuilt binary is an optional dependency, so the
    // message has to say which one and how it goes missing.
    let thrown: RelayfileBinaryNotFoundError | undefined
    try {
      resolve({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        platform: "linux",
        arch: "x64",
        fileExists: fakeFs([])
      })
    } catch (error) {
      thrown = error as RelayfileBinaryNotFoundError
    }

    expect(thrown).toBeInstanceOf(RelayfileBinaryNotFoundError)
    expect(thrown?.platformPackage).toBe("@relayfile/cli-linux-x64")
    const message = thrown?.message ?? ""
    expect(message).toContain("linux x64")
    expect(message).toContain("npm install @relayfile/cli-linux-x64")
    expect(message).toContain("--include=optional")
    expect(message).toContain("--omit=optional")
    expect(message).toContain(RELAYFILE_CLI_BIN_ENV)
  })

  it("says to build from source on a target with no published package", () => {
    let thrown: RelayfileBinaryNotFoundError | undefined
    try {
      resolve({
        binDirs: [],
        searchFrom: [path.join("/nowhere", "deep")],
        platform: "aix",
        arch: "ppc64",
        fileExists: fakeFs([])
      })
    } catch (error) {
      thrown = error as RelayfileBinaryNotFoundError
    }

    expect(thrown?.platformPackage).toBeNull()
    expect(thrown?.message).toContain("no prebuilt CLI binary for aix ppc64")
    expect(thrown?.message).toContain("go build ./cmd/relayfile-cli")
    // Nothing to npm-install, so it must not suggest a package that does not exist.
    expect(thrown?.message).not.toContain("@relayfile/cli-aix-ppc64")
  })

  it("formats the not-found message without throwing", () => {
    expect(formatBinaryNotFoundMessage("darwin", "arm64")).toContain(
      "@relayfile/cli-darwin-arm64"
    )
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

  it("exposes every published platform package name", () => {
    expect([...platformPackageNames()]).toEqual([
      "@relayfile/cli-darwin-arm64",
      "@relayfile/cli-darwin-x64",
      "@relayfile/cli-linux-arm64",
      "@relayfile/cli-linux-x64",
      "@relayfile/cli-win32-arm64",
      "@relayfile/cli-win32-x64"
    ])
  })
})
