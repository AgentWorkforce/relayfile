/**
 * The one implementation of "find the relayfile binary".
 *
 * The real relayfile CLI is the Go binary (`cmd/relayfile-cli`). Both entry
 * points into it — the `relayfile` npm package's bin shim
 * (`packages/cli/scripts/run.js`) and the `agent-relay file` CLI surface in
 * this directory — resolve it through this module. Nothing else in the repo
 * may reimplement the lookup.
 *
 * The binary reaches a machine two different ways, and the resolver has to
 * cope with both:
 *
 *  - As `@relayfile/cli-<platform>-<arch>`, an optional dependency of this
 *    package. npm installs only the one matching the host's `os`/`cpu`, so
 *    nothing is downloaded at install time, the install works offline and in
 *    CI, and integrity comes from the registry. This is the only path that
 *    exists for a consumer that depends on `@relayfile/sdk` without depending
 *    on `relayfile` — `agent-relay` is exactly that consumer.
 *  - Inside the `relayfile` package's own `bin/`, put there by that package's
 *    `postinstall` (`packages/cli/scripts/install.js`), which downloads the
 *    per-platform build from GitHub Releases.
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

/**
 * Node platform/arch pairs that have a published `@relayfile/cli-*` package.
 *
 * Kept in lockstep with this package's `optionalDependencies`, the target list
 * in `packages/cli/scripts/build-binaries.js`, and the package directories
 * under `packages/cli-*`. `platform-packages.test.ts` fails when they diverge.
 */
const PLATFORM_PACKAGE_TARGETS: readonly string[] = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64"
]

/** Environment variable that pins the binary, bypassing every other step. */
export const RELAYFILE_CLI_BIN_ENV = "RELAYFILE_CLI_BIN"

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
  /**
   * Anchors for the `require.resolve` that finds
   * `@relayfile/cli-<platform>-<arch>`. Defaults to this module, the entry
   * script, and the cwd — the three places a consumer's copy of the package
   * can be reached from.
   */
  resolveFrom?: readonly string[]
  /** `PATH` entries for the last-resort lookup. Defaults to `env.PATH`. */
  pathEntries?: readonly string[]
  /** Environment read for `RELAYFILE_CLI_BIN` and `PATH`. */
  env?: NodeJS.ProcessEnv
  platform?: string
  arch?: string
  /** Injected for tests. */
  fileExists?: (candidate: string) => boolean
}

/**
 * Name of the npm package carrying the prebuilt binary for a target.
 *
 * @param platform - Node platform id; defaults to this host's.
 * @param arch - Node arch id; defaults to this host's.
 * @returns The package name, or null when no package is published for the
 *   target (nothing to suggest installing, so callers say "build from source").
 */
export function platformPackageName(
  platform: string = os.platform(),
  arch: string = os.arch()
): string | null {
  const target = `${platform}-${arch}`
  return PLATFORM_PACKAGE_TARGETS.includes(target)
    ? `@relayfile/cli-${target}`
    : null
}

/** Every `@relayfile/cli-*` package name, for tests and release tooling. */
export function platformPackageNames(): readonly string[] {
  return PLATFORM_PACKAGE_TARGETS.map((target) => `@relayfile/cli-${target}`)
}

/**
 * Name of the binary inside a `@relayfile/cli-*` package.
 *
 * It keeps the Go binary's own name (matching `relayfile-mount` inside the
 * `@relayfile/mount-*` packages) rather than the `relayfile` name the CLI
 * package installs, so the two never collide on a machine that has both.
 *
 * @param platform - Node platform id; defaults to this host's.
 * @returns The file name, with `.exe` on Windows.
 */
export function platformPackageBinaryName(
  platform: string = os.platform()
): string {
  return platform === "win32" ? "relayfile-cli.exe" : "relayfile-cli"
}

/** Thrown when neither a binary nor a usable source checkout was found. */
export class RelayfileBinaryNotFoundError extends Error {
  readonly platform: string
  readonly arch: string
  /** The `@relayfile/cli-*` package for this target, when one is published. */
  readonly platformPackage: string | null

  constructor(platform: string, arch: string) {
    super(formatBinaryNotFoundMessage(platform, arch))
    this.name = "RelayfileBinaryNotFoundError"
    this.platform = platform
    this.arch = arch
    this.platformPackage = platformPackageName(platform, arch)
  }
}

/**
 * Explain what to install, for this exact platform.
 *
 * A bare ENOENT sends people looking for a bug in their PATH. The prebuilt
 * binary is an optional dependency, so the two things that actually cause this
 * — `--omit=optional` and an unsupported target — both need naming.
 *
 * @param platform - Node platform id.
 * @param arch - Node arch id.
 * @returns The message carried by `RelayfileBinaryNotFoundError`.
 */
export function formatBinaryNotFoundMessage(
  platform: string,
  arch: string
): string {
  const packageName = platformPackageName(platform, arch)
  if (!packageName) {
    return (
      `relayfile has no prebuilt CLI binary for ${platform} ${arch}. ` +
      `Prebuilt binaries exist for ${PLATFORM_PACKAGE_TARGETS.join(", ")}. ` +
      `Build one from source with \`go build ./cmd/relayfile-cli\` and point ` +
      `${RELAYFILE_CLI_BIN_ENV} at it.`
    )
  }
  return (
    `relayfile could not find a relayfile-cli binary for ${platform} ${arch}. ` +
    `The prebuilt binary ships as ${packageName}, an optional dependency of ` +
    `@relayfile/sdk, so it is missing when the install ran with ` +
    `--omit=optional or could not fetch optional packages. Fix it with one of:\n` +
    `  npm install ${packageName}\n` +
    `  npm install @relayfile/sdk --include=optional\n` +
    `  npm install -g relayfile   (the standalone CLI)\n` +
    `Or set ${RELAYFILE_CLI_BIN_ENV} to a binary you built or downloaded from ` +
    `https://github.com/AgentWorkforce/relayfile/releases.`
  )
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
 * Go's GOARCH ("amd64") differs from Node's `process.arch` ("x64"). The
 * platform package name follows Node; `make build-all` / `make release` output
 * file names follow Go.
 */
function goArch(arch: string): string {
  return ARCH_MAP[arch] ?? arch
}

/**
 * Locate the binary inside `@relayfile/cli-<platform>-<arch>`.
 *
 * Resolved by `require.resolve` from several anchors rather than a fixed
 * relative path: the package can sit anywhere npm decides to hoist it, and the
 * SDK itself may be nested under a consumer's `node_modules` or bundled.
 *
 * @returns The binary path, or null when the package is not installed here
 *   (expected under `--omit=optional`, or on an unsupported target).
 */
function platformPackageBinary(
  platform: string,
  arch: string,
  exists: (candidate: string) => boolean,
  resolveFrom: readonly string[]
): string | null {
  const packageName = platformPackageName(platform, arch)
  if (!packageName) {
    return null
  }
  const binaryName = platformPackageBinaryName(platform)
  for (const anchor of resolveFrom) {
    let manifest: string
    try {
      manifest = createRequire(anchor).resolve(`${packageName}/package.json`)
    } catch {
      continue
    }
    const candidate = path.join(path.dirname(manifest), "bin", binaryName)
    if (exists(candidate)) {
      return candidate
    }
  }
  return null
}

/**
 * Default anchors for resolving the platform package: this module, the entry
 * script (a consumer's CLI, where the SDK lives under their `node_modules`),
 * and the cwd.
 */
function defaultResolveFrom(): readonly string[] {
  const anchors: string[] = [path.join(moduleDirectory(), "resolve-binary.js")]
  if (process.argv[1]) {
    anchors.push(process.argv[1])
  }
  anchors.push(path.join(process.cwd(), "package.json"))
  return [...new Set(anchors)]
}

/**
 * `make build` and `make release` outputs inside a source checkout.
 *
 * Both names carry `.exe` on Windows: `go build -o bin/relayfile-cli` appends
 * it for GOOS=windows, and `scripts/build-cli-npm-packages.mjs` looks for
 * `dist/relayfile-cli-windows-<arch>.exe`. Without the suffix a successful
 * `make build` is invisible here and resolution falls through to `go run`.
 *
 * @returns Candidate binary paths, highest priority first.
 */
function sourceCheckoutBinaries(
  platform: string,
  arch: string,
  exists: (candidate: string) => boolean,
  searchRoots: readonly string[]
): readonly string[] {
  const candidates: string[] = []
  const seenRoots = new Set<string>()
  for (const start of searchRoots) {
    const root = findSourceCheckoutRoot(start, exists)
    if (!root || seenRoots.has(root)) {
      continue
    }
    seenRoots.add(root)
    const goOs = PLATFORM_MAP[platform] ?? platform
    const extension = goOs === "windows" ? ".exe" : ""
    candidates.push(path.join(root, "bin", `relayfile-cli${extension}`))
    candidates.push(
      path.join(root, "dist", `relayfile-cli-${goOs}-${goArch(arch)}${extension}`)
    )
  }
  return candidates
}

/**
 * Last-resort `PATH` scan, for a Go binary placed outside npm.
 *
 * Deliberately matches `relayfile-cli` only, never the generic `relayfile`:
 * on PATH that name is usually the npm bin shim
 * (`packages/cli/scripts/run.js`), which resolves its binary through this
 * module. Spawning it here would make the resolver call itself forever.
 *
 * @returns The first match, or null.
 */
function findOnPath(
  platform: string,
  exists: (candidate: string) => boolean,
  pathEntries: readonly string[]
): string | null {
  const name = platformPackageBinaryName(platform)
  for (const entry of pathEntries) {
    const candidate = path.join(entry, name)
    if (exists(candidate)) {
      return candidate
    }
  }
  return null
}

function binary(binaryPath: string): RelayfileBinaryResolution {
  return { kind: "binary", command: binaryPath, args: [], binaryPath }
}

/**
 * Resolve how to launch the relayfile CLI on this machine.
 *
 * Search order, and why it is this order:
 *  1. `RELAYFILE_CLI_BIN` — the escape hatch, so a developer or operator can
 *     always pin an exact binary.
 *  2. `@relayfile/cli-<platform>-<arch>` — the prebuilt binary npm installed
 *     for this host. First because it is the only path that exists for a
 *     consumer of `@relayfile/sdk` that does not also depend on `relayfile`
 *     (`agent-relay file` is that consumer), and because it needs no network.
 *  3. The `binDirs` chain: a generic `bin/relayfile` (the `relayfile`
 *     package's postinstall download, or a local build), then the
 *     per-platform `bin/relayfile-cli-<os>-<arch>` that package ships.
 *  4. `make build` / `make release` outputs in an enclosing source checkout.
 *  5. `go run ./cmd/relayfile-cli` from that checkout.
 *  6. `PATH`, for a binary installed outside npm.
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
  const env = options.env ?? process.env

  const override = env[RELAYFILE_CLI_BIN_ENV]
  if (override) {
    const resolved = path.resolve(override)
    if (exists(resolved)) {
      return binary(resolved)
    }
  }

  const fromPlatformPackage = platformPackageBinary(
    platform,
    arch,
    exists,
    options.resolveFrom ?? defaultResolveFrom()
  )
  if (fromPlatformPackage) {
    return binary(fromPlatformPackage)
  }

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
        return binary(candidate)
      }
    }
  }

  const searchRoots = [...(options.searchFrom ?? []), moduleDirectory(), process.cwd()]

  for (const candidate of sourceCheckoutBinaries(platform, arch, exists, searchRoots)) {
    if (exists(candidate)) {
      return binary(candidate)
    }
  }

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

  const pathEntries =
    options.pathEntries ?? (env.PATH ?? "").split(path.delimiter).filter(Boolean)
  const onPath = findOnPath(platform, exists, pathEntries)
  if (onPath) {
    return binary(onPath)
  }

  throw new RelayfileBinaryNotFoundError(platform, arch)
}

/** Message shown when a source checkout was found but Go is not installed. */
export const GO_TOOLCHAIN_MISSING_MESSAGE =
  "relayfile binary not found and Go is not installed to run from source. " +
  "Install Go or run `npm run build --workspace=packages/cli`."
