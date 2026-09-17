/**
 * Test support: build the real relayfile Go binary once per test run.
 *
 * The CLI-surface tests execute the actual binary — no stubs — so they need a
 * built one. `go run` would recompile on every invocation; building once and
 * pointing the resolver at the output keeps a per-command sweep fast.
 *
 * Excluded from the published build by tsconfig.
 */

import { spawnSync } from "node:child_process"
import { existsSync, mkdirSync, mkdtempSync } from "node:fs"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

import { findSourceCheckoutRoot } from "../resolve-binary.js"

let cached: { binDir: string; checkoutRoot: string } | undefined

/**
 * Create a throwaway directory for a test that needs a real filesystem —
 * resolution through `require.resolve` cannot be faked.
 *
 * @param prefix - Label included in the directory name.
 * @returns The directory path.
 */
export function temporaryDirectory(prefix: string): string {
  return mkdtempSync(path.join(os.tmpdir(), `relayfile-${prefix}-`))
}

/**
 * Locate the relayfile checkout these tests run inside.
 *
 * @returns The checkout root.
 * @throws When the tests are not running inside a checkout.
 */
export function checkoutRoot(): string {
  const root = findSourceCheckoutRoot(path.dirname(fileURLToPath(import.meta.url)))
  if (!root) {
    throw new Error(
      "relay-cli tests must run inside a relayfile checkout (no go.mod + cmd/relayfile-cli found)"
    )
  }
  return root
}

/**
 * Build `cmd/relayfile-cli` into a temp directory shaped like the `relayfile`
 * package's `bin/`, so it can be handed to `resolveRelayfileBinary` as a
 * `binDirs` entry.
 *
 * @returns The directory holding the built binary, and the checkout root.
 * @throws When the Go toolchain is missing or the build fails.
 */
export function buildRelayfileBinary(): { binDir: string; checkoutRoot: string } {
  if (cached) {
    return cached
  }

  const root = checkoutRoot()
  const binDir = path.join(mkdtempSync(path.join(os.tmpdir(), "relayfile-surface-")), "bin")
  mkdirSync(binDir, { recursive: true })
  const output = path.join(binDir, os.platform() === "win32" ? "relayfile.exe" : "relayfile")

  const result = spawnSync("go", ["build", "-o", output, "./cmd/relayfile-cli"], {
    cwd: root,
    encoding: "utf8"
  })
  if (result.error) {
    const code = (result.error as NodeJS.ErrnoException).code
    if (code === "ENOENT") {
      throw new Error(
        "the relay-cli surface tests execute the real relayfile binary and need a Go toolchain on PATH"
      )
    }
    throw result.error
  }
  if (result.status !== 0 || !existsSync(output)) {
    throw new Error(`go build ./cmd/relayfile-cli exited ${result.status}\n${result.stderr}`)
  }

  cached = { binDir, checkoutRoot: root }
  return cached
}

let sdkDist: string | undefined

/**
 * Ensure this package's `dist/` exists, for tests that need the SDK as it is
 * published rather than as vitest transforms it — resolution through the
 * `exports` map only works against the built files.
 *
 * Builds only when `dist/` is missing, so a normal `npm run build && npm test`
 * pays nothing.
 *
 * @returns The `dist` directory.
 * @throws When the build fails.
 */
export function buildSdkDist(): string {
  if (sdkDist) {
    return sdkDist
  }

  // src/relay-cli/testing -> packages/sdk/typescript
  const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..", "..")
  const dist = path.join(packageRoot, "dist")

  if (!existsSync(path.join(dist, "relay-cli", "index.js"))) {
    const result = spawnSync("npm", ["run", "build"], {
      cwd: packageRoot,
      encoding: "utf8"
    })
    if (result.status !== 0) {
      throw new Error(
        `npm run build (packages/sdk/typescript) exited ${result.status}\n${result.stdout}\n${result.stderr}`
      )
    }
  }
  if (!existsSync(path.join(dist, "relay-cli", "command-spec.json"))) {
    throw new Error(
      "dist/relay-cli/command-spec.json is missing; run `npm run build --workspace=packages/sdk/typescript`"
    )
  }

  sdkDist = dist
  return dist
}
