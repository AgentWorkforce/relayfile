/**
 * Resolves the relayfile-mount binary path at runtime.
 *
 * Mirrors the agent-relay broker resolver (`@agent-relay/harness-driver`'s
 * broker-path): the binary ships as platform-specific optional-dependency
 * packages (`@relayfile/mount-<platform>-<arch>`) that npm installs only for
 * the matching os/cpu, and the SDK locates the right one via `require.resolve`.
 * No postinstall download is involved.
 *
 * Usage:
 *   import { getRelayfileMountBinaryPath } from "@relayfile/sdk/mount-path"
 *   const binPath = getRelayfileMountBinaryPath()
 */
import { existsSync } from "node:fs"
import { createRequire } from "node:module"
import { delimiter, dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const MOUNT_BIN = "relayfile-mount"

/**
 * Node's `process.arch` ("x64"/"arm64") differs from Go's GOARCH
 * ("amd64"/"arm64"). The optional-dep package name follows Node's convention
 * (matching the broker packages); the source-checkout `dist/` file names follow
 * Go's. This maps Node arch -> Go arch for dev resolution.
 */
function goArch(arch: string = process.arch): string {
  return arch === "x64" ? "amd64" : arch
}

export function getOptionalDepPackageName(
  platform: string = process.platform,
  arch: string = process.arch
): string {
  return `@relayfile/mount-${platform}-${arch}`
}

function getCurrentModuleDir(): string | null {
  try {
    return dirname(fileURLToPath(import.meta.url))
  } catch {
    return null
  }
}

function addUnique(paths: string[], candidate: string | null | undefined): void {
  if (candidate && !paths.includes(candidate)) {
    paths.push(candidate)
  }
}

/**
 * Anchors from which to run `require.resolve` for the optional-dep package.
 * Covers the SDK's own location, the entry script (CLI consumers / bundled
 * installs where the SDK lives under the consumer's node_modules), and the
 * consumer's cwd.
 */
function getResolutionReferences(): string[] {
  const refs: string[] = []
  const moduleDir = getCurrentModuleDir()
  addUnique(refs, moduleDir ? join(moduleDir, "mount-path.js") : null)
  if (process.argv[1]) {
    addUnique(refs, process.argv[1])
  }
  addUnique(refs, join(process.cwd(), "package.json"))
  return refs
}

/**
 * Resolve the binary via the platform-specific optional-dependency package.
 * Returns null when the optional dep is not installed (expected under
 * --omit=optional, or before the package has been published for a platform).
 */
function getOptionalDepBinaryPath(): string | null {
  const pkgName = getOptionalDepPackageName()
  for (const ref of getResolutionReferences()) {
    try {
      const pkgJsonPath = createRequire(ref).resolve(`${pkgName}/package.json`)
      const binPath = join(dirname(pkgJsonPath), "bin", MOUNT_BIN)
      if (existsSync(binPath)) {
        return binPath
      }
    } catch {
      // Try the next reference.
    }
  }
  return null
}

function isSourceCheckoutRoot(candidate: string): boolean {
  const root = resolve(candidate)
  return (
    existsSync(join(root, "go.mod")) &&
    existsSync(join(root, "cmd", "relayfile-mount", "main.go"))
  )
}

function findAncestorSourceCheckoutRoot(start: string): string | null {
  let current = resolve(start)
  for (let i = 0; i < 8; i += 1) {
    if (isSourceCheckoutRoot(current)) {
      return current
    }
    const parent = resolve(current, "..")
    if (parent === current) {
      break
    }
    current = parent
  }
  return null
}

/**
 * Local `make build`/`make build-all` outputs, used when the SDK is loaded
 * from a relayfile source checkout.
 */
function getSourceCheckoutBinaryPaths(): string[] {
  const paths: string[] = []
  const roots = new Set<string>()
  const addRoot = (candidate: string | null): void => {
    if (!candidate) {
      return
    }
    const root = resolve(candidate)
    if (!isSourceCheckoutRoot(root) || roots.has(root)) {
      return
    }
    roots.add(root)
    addUnique(paths, join(root, "bin", MOUNT_BIN))
    addUnique(
      paths,
      join(root, "dist", `relayfile-mount-${process.platform}-${goArch()}`)
    )
  }

  addRoot(findAncestorSourceCheckoutRoot(process.cwd()))
  const moduleDir = getCurrentModuleDir()
  if (moduleDir) {
    // packages/sdk/typescript/dist -> repo root is four levels up.
    addRoot(findAncestorSourceCheckoutRoot(moduleDir))
  }
  return paths
}

function findExecutableInPath(): string | null {
  const ext = process.platform === "win32" ? ".exe" : ""
  const entries = (process.env.PATH ?? "").split(delimiter).filter(Boolean)
  for (const entry of entries) {
    const candidate = join(entry, `${MOUNT_BIN}${ext}`)
    if (existsSync(candidate)) {
      return candidate
    }
  }
  return null
}

/**
 * Resolve the relayfile-mount binary path.
 *
 * Search order:
 *   1. Explicit env override (RELAYFILE_MOUNT_BIN)
 *   2. Local source-checkout build (bin/ or dist/), when loaded from a checkout
 *   3. Platform-specific optional-dep package — primary production path
 *   4. PATH lookup
 *
 * @returns Absolute path to the binary, or null if none is found.
 */
export function getRelayfileMountBinaryPath(): string | null {
  const override = process.env.RELAYFILE_MOUNT_BIN
  if (override) {
    const resolved = resolve(override)
    if (existsSync(resolved)) {
      return resolved
    }
  }

  for (const sourcePath of getSourceCheckoutBinaryPaths()) {
    if (existsSync(sourcePath)) {
      return sourcePath
    }
  }

  const optionalDepBinary = getOptionalDepBinaryPath()
  if (optionalDepBinary) {
    return optionalDepBinary
  }

  return findExecutableInPath()
}

/**
 * Human-readable error explaining that the optional-dep package for the
 * current platform/arch isn't installed.
 */
export function formatMountNotFoundError(): string {
  const pkgName = getOptionalDepPackageName()
  return (
    `relayfile couldn't find a relayfile-mount binary for ` +
    `${process.platform}-${process.arch}. The optional dependency ${pkgName} ` +
    `is expected to be installed alongside @relayfile/sdk. Try reinstalling ` +
    `with --include=optional, or set RELAYFILE_MOUNT_BIN to a binary you've ` +
    `built or downloaded manually.`
  )
}
