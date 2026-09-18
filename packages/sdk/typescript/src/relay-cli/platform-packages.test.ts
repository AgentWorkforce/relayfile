import { existsSync, readFileSync, readdirSync } from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"

import { platformPackageBinaryName, platformPackageNames } from "./resolve-binary.js"
import { checkoutRoot } from "./testing/build-binary.js"

/**
 * The `@relayfile/cli-<platform>-<arch>` packages are the only way the
 * relayfile binary reaches a consumer that depends on `@relayfile/sdk` without
 * also depending on `relayfile` — `agent-relay file` is that consumer, and it
 * shipped once with no binary at all because nothing checked.
 *
 * Every list that has to agree is checked here: the package directories, the
 * SDK's optionalDependencies, the resolver's target table, the two build
 * scripts that produce the binaries, and the publish workflow that releases
 * them. A package that exists but is never published, or is published but
 * cannot be built, fails this file rather than a user's install.
 */

const root = checkoutRoot()
const sdkPackagePath = path.join(root, "packages", "sdk", "typescript", "package.json")

interface PlatformPackage {
  /** Directory name under `packages/`, e.g. `cli-linux-x64`. */
  directory: string
  /** npm package name. */
  name: string
  /** Node platform id. */
  platform: string
  /** Node arch id. */
  arch: string
  manifest: Record<string, unknown>
}

function readJson(filePath: string): Record<string, unknown> {
  return JSON.parse(readFileSync(filePath, "utf8")) as Record<string, unknown>
}

function packageDirectories(prefix: string): readonly string[] {
  return readdirSync(path.join(root, "packages"))
    .filter((entry) => entry.startsWith(`${prefix}-`))
    .sort()
}

const platformPackages: readonly PlatformPackage[] = packageDirectories("cli").map(
  (directory) => {
    const [, platform, arch] = directory.split("-")
    return {
      directory,
      name: `@relayfile/${directory}`,
      platform,
      arch,
      manifest: readJson(path.join(root, "packages", directory, "package.json"))
    }
  }
)

const sdkManifest = readJson(sdkPackagePath)
const sdkVersion = sdkManifest.version as string
const optionalDependencies = (sdkManifest.optionalDependencies ?? {}) as Record<
  string,
  string
>
const workflow = readFileSync(
  path.join(root, ".github", "workflows", "publish.yml"),
  "utf8"
)

describe("cli platform packages", () => {
  it("has a package directory for every target the resolver looks for", () => {
    // Both directions: a resolver target with no package would throw
    // "install @relayfile/cli-..." naming something that does not exist, and a
    // package the resolver never looks for is dead weight on every install.
    expect(platformPackages.map((entry) => entry.name)).toEqual([
      ...platformPackageNames()
    ])
  })

  it("declares the os/cpu that npm filters the install on", () => {
    for (const entry of platformPackages) {
      expect(entry.manifest.name, entry.directory).toBe(entry.name)
      expect(entry.manifest.os, entry.directory).toEqual([entry.platform])
      expect(entry.manifest.cpu, entry.directory).toEqual([entry.arch])
      expect(entry.manifest.files, entry.directory).toEqual(["bin"])
      expect(entry.manifest.version, entry.directory).toBe(sdkVersion)
      expect(entry.manifest.publishConfig, entry.directory).toEqual({
        access: "public"
      })
      expect(
        (entry.manifest.repository as { directory?: string } | undefined)?.directory,
        entry.directory
      ).toBe(`packages/${entry.directory}`)
    }
  })

  it("runs nothing at install time", () => {
    // The whole point of the platform-package pattern over the `relayfile`
    // package's postinstall download: no scripts, no network, works offline
    // and behind a firewall, integrity from the registry.
    for (const entry of platformPackages) {
      expect(entry.manifest.scripts, entry.directory).toBeUndefined()
      expect(entry.manifest.dependencies, entry.directory).toBeUndefined()
      expect(entry.manifest.optionalDependencies, entry.directory).toBeUndefined()
    }
  })

  it("keeps the same manifest shape as the mount platform packages", () => {
    // The instruction was to mirror @relayfile/mount-*, not to invent a second
    // pattern. Compare key sets rather than values.
    const mountManifest = readJson(
      path.join(root, "packages", "mount-linux-x64", "package.json")
    )
    const expected = Object.keys(mountManifest).sort()
    for (const entry of platformPackages) {
      expect(Object.keys(entry.manifest).sort(), entry.directory).toEqual(expected)
    }
  })

  it("keeps the bin skeleton tracked and the binary untracked", () => {
    const gitignore = readFileSync(path.join(root, ".gitignore"), "utf8")
    expect(gitignore).toContain("!packages/cli-*/bin/")
    expect(gitignore).toContain("!packages/cli-*/bin/.gitkeep")
    expect(gitignore).toContain("packages/cli-*/bin/relayfile-cli")
    expect(gitignore).toContain("packages/cli-*/bin/relayfile-cli.exe")
    for (const entry of platformPackages) {
      expect(
        existsSync(path.join(root, "packages", entry.directory, "bin", ".gitkeep")),
        entry.directory
      ).toBe(true)
    }
  })

  it("is an exactly pinned optional dependency of @relayfile/sdk", () => {
    // Optional so a platform with no package (or an --omit=optional install)
    // still installs the SDK; exact so a consumer can never end up with a
    // binary from a different release than the surface that spawns it.
    for (const entry of platformPackages) {
      expect(optionalDependencies[entry.name], entry.name).toBe(sdkVersion)
    }
  })

  it("has no cli platform package the SDK does not install", () => {
    const declared = Object.keys(optionalDependencies)
      .filter((name) => name.startsWith("@relayfile/cli-"))
      .sort()
    expect(declared).toEqual(platformPackages.map((entry) => entry.name))
  })
})

describe("cli platform package build", () => {
  it("cross-compiles a binary for every platform package", () => {
    // "I do not want to publish a package we cannot produce": every package
    // must have a matching target in the CLI package's build script.
    const source = readFileSync(
      path.join(root, "packages", "cli", "scripts", "build-binaries.js"),
      "utf8"
    )
    const goTargets = new Set(
      [...source.matchAll(/goos:\s*"([a-z0-9]+)",\s*goarch:\s*"([a-z0-9]+)"/g)].map(
        (match) => `${match[1]}-${match[2]}`
      )
    )
    expect(goTargets.size).toBeGreaterThan(0)

    const goOs: Record<string, string> = { win32: "windows" }
    const goArch: Record<string, string> = { x64: "amd64" }
    for (const entry of platformPackages) {
      const target = `${goOs[entry.platform] ?? entry.platform}-${
        goArch[entry.arch] ?? entry.arch
      }`
      expect(goTargets.has(target), `${entry.directory} needs ${target}`).toBe(true)
    }
  })

  it("is filled by build-cli-npm-packages.mjs for every platform package", () => {
    const source = readFileSync(
      path.join(root, "scripts", "build-cli-npm-packages.mjs"),
      "utf8"
    )
    for (const entry of platformPackages) {
      expect(
        source.includes(`npmOs: '${entry.platform}', npmArch: '${entry.arch}'`),
        entry.directory
      ).toBe(true)
    }
    expect(source).toContain("relayfile-cli.exe")
  })

  it("names the binary the resolver looks for", () => {
    const source = readFileSync(
      path.join(root, "scripts", "build-cli-npm-packages.mjs"),
      "utf8"
    )
    expect(source).toContain(`'${platformPackageBinaryName("linux")}'`)
    expect(source).toContain(`'${platformPackageBinaryName("win32")}'`)
  })
})

describe("cli platform package release", () => {
  /** The `git add` argument list in the release-commit step. */
  function releaseCommitBlock(): string {
    const start = workflow.indexOf("          git add \\\n")
    expect(start).toBeGreaterThan(-1)
    const end = workflow.indexOf("\n          if !", start)
    expect(end).toBeGreaterThan(start)
    return workflow.slice(start, end)
  }

  it("is published by the publish workflow", () => {
    // Listed everywhere a release needs it: the dispatch choice, the input
    // allowlist, the version-sync path list, the build artifact, both publish
    // matrices, the single-package dispatch, the release commit, and the
    // release notes. A package missing from any one of these silently never
    // ships, which is the exact failure this whole change is fixing.
    for (const entry of platformPackages) {
      const required: Array<[string, string]> = [
        ["dispatch choice", `          - ${entry.directory}\n`],
        ["input allowlist", `|${entry.directory}`],
        ["version sync", `"packages/${entry.directory}/package.json"`],
        ["build artifact", `packages/${entry.directory}/bin/.gitkeep`],
        ["publish matrix", `          - package: ${entry.directory}\n`],
        ["single dispatch", `${entry.directory}) echo "path=packages/${entry.directory}"`],
        ["release notes", `- \`${entry.name}@$`]
      ]
      for (const [label, needle] of required) {
        expect(workflow.includes(needle), `${entry.directory}: ${label}`).toBe(true)
      }
      expect(
        releaseCommitBlock().includes(`packages/${entry.directory}/package.json`),
        `${entry.directory}: release commit`
      ).toBe(true)
    }
  })

  it("appears in both the preflight and publish matrices", () => {
    for (const entry of platformPackages) {
      expect(
        workflow.split(`          - package: ${entry.directory}\n`).length - 1,
        entry.directory
      ).toBe(2)
    }
  })

  it("copies the cross-compiled binary into the package", () => {
    expect(workflow).toContain("Prepare cli platform package")
    expect(workflow).toContain('BIN_NAME=relayfile-cli')
    expect(workflow).toContain('case "${{ matrix.cli_binary }}" in *.exe) BIN_NAME=relayfile-cli.exe ;; esac')
    // Every matrix entry declares cli_binary explicitly: an undefined matrix
    // key is not '' in a GitHub expression, so the `!= ''` guard would fire.
    const matrixPackages = workflow.match(/^          - package: /gm) ?? []
    const matrixCliBinary = workflow.match(/^            cli_binary: /gm) ?? []
    expect(matrixCliBinary.length).toBe(matrixPackages.length)
  })

  it("expects one attestation per published package", () => {
    // The release job counts attestation files; adding packages without
    // raising the count fails the release *after* npm already has them.
    const publishedPackages =
      workflow.match(/^            "packages\/[^"]+\/package\.json"/gm) ?? []
    expect(publishedPackages.length).toBeGreaterThan(platformPackages.length)
    expect(workflow).toContain(
      `-name '*.json' | wc -l | tr -d ' ')" -eq ${publishedPackages.length}`
    )
  })
})
