/**
 * The one implementation of "find the relayfile binary".
 *
 * The real relayfile CLI is the Go binary (`cmd/relayfile-cli`). Both entry
 * points into it — the `relayfile` npm package's bin shim
 * (`packages/cli/scripts/run.js`) and the `agent-relay file` CLI surface in
 * this directory — resolve it through this module. Nothing else in the repo
 * may reimplement the lookup.
 */

import { createRequire } from "node:module"
import { existsSync } from "node:fs"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

const PLATFORM_MAP: Record<string, string> = {
  darwin: "darwin",
  linux: "linux",
  win32: "windows"
}

const ARCH_MAP: Record<string, string> = {
  x64: "amd64",
  arm64: "arm64"
}

/** How the resolver decided to launch relayfile. */
export type RelayfileBinaryResolution =
  | {
      /** A packaged or locally built binary was found. */
      kind: "binary"
      command: string
      args: readonly string[]
      /** Absolute path of the binary that matched. */
      binaryPath: string
    }
  | {
      /**
       * No binary was found, but we are inside a source checkout, so relayfile
       * runs straight from Go source. `postinstall` intentionally skips
       * building the binary in a checkout; without this fallback the installed
       * `relayfile` command would be unusable there.
       */
      kind: "go-run"
      command: "go"
      args: readonly string[]
      cwd: string
    }

export interface ResolveRelayfileBinaryOptions {
  /**
   * Directories to search for a packaged binary, highest priority first.
   * Defaults to the `bin` directory of the installed `relayfile` package plus
   * the workspace copy when running inside this repo.
   */
  binDirs?: readonly string[]
  /** Extra directories to start the source-checkout search from. */
  searchFrom?: readonly string[]
  platform?: string
  arch?: string
  /** Injected for tests. */
  fileExists?: (candidate: string) => boolean
}

/** Thrown when neither a binary nor a usable source checkout was found. */
export class RelayfileBinaryNotFoundError extends Error {
  readonly platform: string
  readonly arch: string

  constructor(platform: string, arch: string) {
    super(
      `relayfile binary not found for ${platform} ${arch}. Reinstall the package or run postinstall again.`
    )
    this.name = "RelayfileBinaryNotFoundError"
    this.platform = platform
    this.arch = arch
  }
}

/**
 * Name of the per-platform binary shipped inside the `relayfile` package.
 *
 * @returns The packaged binary's file name, or null on an unsupported target.
 */
export function platformBinaryName(
  platform: string = os.platform(),
  arch: string = os.arch()
): string | null {
  const goPlatform = PLATFORM_MAP[platform]
  const goArch = ARCH_MAP[arch]
  if (!goPlatform || !goArch) {
    return null
  }
  const extension = goPlatform === "windows" ? ".exe" : ""
  return `relayfile-cli-${goPlatform}-${goArch}${extension}`
}

/**
 * Name of the binary the CLI package installs and runs, `bin/relayfile`.
 *
 * @param platform - Node platform id; defaults to this host's.
 * @returns The file name, with `.exe` on Windows.
 */
export function genericBinaryName(platform: string = os.platform()): string {
  return platform === "win32" ? "relayfile.exe" : "relayfile"
}

function moduleDirectory(): string {
  return path.dirname(fileURLToPath(import.meta.url))
}

/**
 * Walk up from `start` looking for a directory that satisfies `matches`.
 *
 * @returns The matching directory, or null when the filesystem root is reached.
 */
function findUpward(
  start: string,
  matches: (directory: string) => boolean
): string | null {
  let current = path.resolve(start)
  for (;;) {
    if (matches(current)) {
      return current
    }
    const parent = path.dirname(current)
    if (parent === current) {
      return null
    }
    current = parent
  }
}

/**
 * Locate the `bin` directory of the installed `relayfile` CLI package.
 *
 * @returns The directory, or null when the package is not installed here.
 */
function installedCliBinDir(): string | null {
  try {
    const require = createRequire(import.meta.url)
    const manifest = require.resolve("relayfile/package.json")
    return path.join(path.dirname(manifest), "bin")
  } catch {
    return null
  }
}

/**
 * Locate `packages/cli/bin` when running from inside this repo, where the SDK
 * and the CLI package are siblings rather than dependencies.
 *
 * @returns The directory, or null outside a checkout.
 */
function workspaceCliBinDir(exists: (candidate: string) => boolean): string | null {
  const repoRoot = findUpward(moduleDirectory(), (directory) =>
    exists(path.join(directory, "packages", "cli", "package.json"))
  )
  return repoRoot ? path.join(repoRoot, "packages", "cli", "bin") : null
}

/**
 * Locate a relayfile source checkout: a directory with both `go.mod` and
 * `cmd/relayfile-cli`.
 *
 * @returns The checkout root, or null when there is none above `start`.
 */
export function findSourceCheckoutRoot(
  start: string,
  exists: (candidate: string) => boolean = existsSync
): string | null {
  return findUpward(
    start,
    (directory) =>
      exists(path.join(directory, "go.mod")) &&
      exists(path.join(directory, "cmd", "relayfile-cli"))
  )
}

/**
 * Resolve how to launch the relayfile CLI on this machine.
 *
 * Search order, preserving the behavior of the `relayfile` bin shim:
 * 1. a generic `bin/relayfile` (a locally built binary), then the
 *    per-platform `bin/relayfile-cli-<os>-<arch>` the package ships;
 * 2. `go run ./cmd/relayfile-cli` from an enclosing source checkout.
 *
 * @param options - Search overrides; all are optional.
 * @returns The command, argv prefix, and cwd to spawn.
 * @throws {RelayfileBinaryNotFoundError} When nothing usable was found.
 */
export function resolveRelayfileBinary(
  options: ResolveRelayfileBinaryOptions = {}
): RelayfileBinaryResolution {
  const exists = options.fileExists ?? existsSync
  const platform = options.platform ?? os.platform()
  const arch = options.arch ?? os.arch()

  const binDirs =
    options.binDirs ??
    [installedCliBinDir(), workspaceCliBinDir(exists)].filter(
      (directory): directory is string => Boolean(directory)
    )

  const packagedName = platformBinaryName(platform, arch)
  for (const binDir of binDirs) {
    const candidates = [
      path.join(binDir, genericBinaryName(platform)),
      packagedName ? path.join(binDir, packagedName) : null
    ].filter((candidate): candidate is string => Boolean(candidate))

    for (const candidate of candidates) {
      if (exists(candidate)) {
        return { kind: "binary", command: candidate, args: [], binaryPath: candidate }
      }
    }
  }

  const searchRoots = [...(options.searchFrom ?? []), moduleDirectory(), process.cwd()]
  for (const root of searchRoots) {
    const checkout = findSourceCheckoutRoot(root, exists)
    if (checkout) {
      return {
        kind: "go-run",
        command: "go",
        args: ["run", "./cmd/relayfile-cli"],
        cwd: checkout
      }
    }
  }

  throw new RelayfileBinaryNotFoundError(platform, arch)
}

/** Message shown when a source checkout was found but Go is not installed. */
export const GO_TOOLCHAIN_MISSING_MESSAGE =
  "relayfile binary not found and Go is not installed to run from source. " +
  "Install Go or run `npm run build --workspace=packages/cli`."
