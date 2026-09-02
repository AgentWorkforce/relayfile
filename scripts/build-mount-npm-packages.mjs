#!/usr/bin/env node
// Fills the @relayfile/mount-<os>-<arch> platform packages with the prebuilt
// relayfile-mount binary so they can be published to npm. Mirrors the
// agent-relay broker platform-package layout (@agent-relay/broker-<os>-<arch>),
// which @relayfile/sdk resolves at runtime via require.resolve.
//
// Usage:
//   make build-all            # produce dist/<os>_<arch>/relayfile-mount
//   node scripts/build-mount-npm-packages.mjs
//
// Packed-consumer verification may override RELAYFILE_MOUNT_DIST_DIR and
// RELAYFILE_MOUNT_PACKAGES_DIR so release staging never mutates worktree
// build output or checked-out package directories.
//
// For each target it:
//   1. copies dist/<os>_<goarch>/relayfile-mount (or the legacy flat release
//      artifact dist/relayfile-mount-<os>-<goarch>) into the platform package
//   2. rewrites that package's version to match @relayfile/sdk
//
// Note the arch naming: Go emits `amd64`, npm os/cpu uses Node's `x64`. The
// dist paths use the Go name; the package dirs use the Node name.
import { chmod, copyFile, mkdir, readFile, writeFile } from 'node:fs/promises'
import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(scriptDir, '..')
const distDir = resolve(process.env.RELAYFILE_MOUNT_DIST_DIR || join(repoRoot, 'dist'))
const packagesDir = resolve(process.env.RELAYFILE_MOUNT_PACKAGES_DIR || join(repoRoot, 'packages'))

// (npmArch used in package dir + os/cpu) -> (goArch used in dist file name)
const TARGETS = [
  { os: 'darwin', npmArch: 'arm64', goArch: 'arm64' },
  { os: 'darwin', npmArch: 'x64', goArch: 'amd64' },
  { os: 'linux', npmArch: 'arm64', goArch: 'arm64' },
  { os: 'linux', npmArch: 'x64', goArch: 'amd64' }
]

async function exists(path) {
  try {
    await access(path, constants.R_OK)
    return true
  } catch {
    return false
  }
}

async function sdkVersion() {
  const sdkPkgPath = join(repoRoot, 'packages', 'sdk', 'typescript', 'package.json')
  const raw = await readFile(sdkPkgPath, 'utf8')
  return JSON.parse(raw).version
}

async function main() {
  const version = await sdkVersion()
  const missing = []

  for (const target of TARGETS) {
    // `make build-all` writes one target directory per GOOS/GOARCH. Keep the
    // legacy flat filename as a fallback for older release artifact layouts.
    const builtBinary = join(distDir, `${target.os}_${target.goArch}`, 'relayfile-mount')
    const legacyBinary = join(distDir, `relayfile-mount-${target.os}-${target.goArch}`)
    const distBinary = (await exists(builtBinary)) ? builtBinary : legacyBinary
    const pkgDir = join(packagesDir, `mount-${target.os}-${target.npmArch}`)
    const pkgJsonPath = join(pkgDir, 'package.json')
    const binTarget = join(pkgDir, 'bin', 'relayfile-mount')

    if (!(await exists(distBinary))) {
      missing.push(distBinary)
      continue
    }

    await mkdir(dirname(binTarget), { recursive: true })
    await copyFile(distBinary, binTarget)
    await chmod(binTarget, 0o755)

    const pkg = JSON.parse(await readFile(pkgJsonPath, 'utf8'))
    if (pkg.version !== version) {
      pkg.version = version
      await writeFile(pkgJsonPath, JSON.stringify(pkg, null, 2) + '\n')
    }

    console.log(`[mount-pkg] ${pkg.name}@${version} <- ${distBinary}`)
  }

  if (missing.length > 0) {
    console.error(
      `[mount-pkg] missing dist binaries (run \`make build-all\` first):\n  ${missing.join('\n  ')}`
    )
    process.exit(1)
  }

  console.log(`[mount-pkg] ready to publish ${TARGETS.length} packages at v${version}`)
}

main().catch((error) => {
  console.error('[mount-pkg]', error instanceof Error ? error.message : error)
  process.exit(1)
})
