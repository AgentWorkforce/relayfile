#!/usr/bin/env node
// Fills the @relayfile/cli-<os>-<arch> platform packages with the prebuilt
// relayfile-cli binary so they can be published to npm. Mirrors
// build-mount-npm-packages.mjs and the @relayfile/mount-* layout, which
// @relayfile/sdk resolves at runtime via require.resolve.
//
// These packages are what makes `agent-relay file <cmd>` work on a clean
// install: agent-relay depends on @relayfile/sdk but not on `relayfile`, so
// the `relayfile` package's postinstall download never runs for it. Installing
// the binary as a per-platform optional dependency needs no install-time
// network, works offline and in CI, and gets its integrity from the registry.
//
// Usage:
//   npm run build --workspace=packages/cli   # produce packages/cli/bin/*
//   node scripts/build-cli-npm-packages.mjs
//
// For each target it:
//   1. copies the matching prebuilt binary -> packages/cli-<os>-<arch>/bin/relayfile-cli
//      (relayfile-cli.exe on Windows)
//   2. rewrites that package's version to match @relayfile/sdk
//
// Two source layouts are accepted, because two different commands produce the
// binaries: `npm run build --workspace=packages/cli` writes
// packages/cli/bin/relayfile-cli-<goos>-<goarch>, and `make release` writes
// dist/relayfile-cli-<goos>-<goarch>. The publish workflow builds the CLI
// package, so the first is the one that matters in CI.
//
// Note the arch naming: Go emits `amd64` and `windows`, npm os/cpu uses Node's
// `x64` and `win32`. Source file names follow Go; the package dirs follow Node.
import { chmod, copyFile, mkdir, readFile, writeFile } from 'node:fs/promises'
import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(scriptDir, '..')
const distDir = join(repoRoot, 'dist')
const cliBinDir = join(repoRoot, 'packages', 'cli', 'bin')
const packagesDir = join(repoRoot, 'packages')

// (npmOs/npmArch used in the package dir + os/cpu fields)
//   -> (goOs/goArch used in the prebuilt file name)
const TARGETS = [
  { npmOs: 'darwin', npmArch: 'arm64', goOs: 'darwin', goArch: 'arm64' },
  { npmOs: 'darwin', npmArch: 'x64', goOs: 'darwin', goArch: 'amd64' },
  { npmOs: 'linux', npmArch: 'arm64', goOs: 'linux', goArch: 'arm64' },
  { npmOs: 'linux', npmArch: 'x64', goOs: 'linux', goArch: 'amd64' },
  { npmOs: 'win32', npmArch: 'arm64', goOs: 'windows', goArch: 'arm64' },
  { npmOs: 'win32', npmArch: 'x64', goOs: 'windows', goArch: 'amd64' }
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

/**
 * Prebuilt binary for a target, from whichever build produced it.
 *
 * @param target - One TARGETS entry.
 * @returns The first existing source path, or null when none was built.
 */
async function findSourceBinary(target) {
  const extension = target.goOs === 'windows' ? '.exe' : ''
  const fileName = `relayfile-cli-${target.goOs}-${target.goArch}${extension}`
  for (const candidate of [join(cliBinDir, fileName), join(distDir, fileName)]) {
    if (await exists(candidate)) {
      return candidate
    }
  }
  return null
}

async function main() {
  const version = await sdkVersion()
  const missing = []

  for (const target of TARGETS) {
    const source = await findSourceBinary(target)
    const pkgDir = join(packagesDir, `cli-${target.npmOs}-${target.npmArch}`)
    const pkgJsonPath = join(pkgDir, 'package.json')
    const binaryName = target.npmOs === 'win32' ? 'relayfile-cli.exe' : 'relayfile-cli'
    const binTarget = join(pkgDir, 'bin', binaryName)

    if (!source) {
      missing.push(`relayfile-cli for ${target.npmOs}-${target.npmArch}`)
      continue
    }

    await mkdir(dirname(binTarget), { recursive: true })
    await copyFile(source, binTarget)
    await chmod(binTarget, 0o755)

    const pkg = JSON.parse(await readFile(pkgJsonPath, 'utf8'))
    if (pkg.version !== version) {
      pkg.version = version
      await writeFile(pkgJsonPath, JSON.stringify(pkg, null, 2) + '\n')
    }

    console.log(`[cli-pkg] ${pkg.name}@${version} <- ${source}`)
  }

  if (missing.length > 0) {
    console.error(
      '[cli-pkg] missing prebuilt binaries (run `npm run build --workspace=packages/cli` ' +
        `first):\n  ${missing.join('\n  ')}`
    )
    process.exit(1)
  }

  console.log(`[cli-pkg] ready to publish ${TARGETS.length} packages at v${version}`)
}

main().catch((error) => {
  console.error('[cli-pkg]', error instanceof Error ? error.message : error)
  process.exit(1)
})
