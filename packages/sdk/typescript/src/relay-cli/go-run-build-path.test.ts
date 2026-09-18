import {
  chmodSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  utimesSync,
  writeFileSync
} from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"

import {
  buildGoRunBinary,
  GoBuildFailedError,
  type RelayfileGoRunResolution
} from "./index.js"
import { temporaryDirectory } from "./testing/build-binary.js"

/**
 * The source fallback must not fight over one file.
 *
 * `goRunBinaryPath` is keyed by the checkout alone, so every process that
 * falls back to source builds to the same path — while a `listen` or `mount`
 * started from that path is still executing it. Writing it in place cannot
 * work on Windows (a running `.exe` is locked) and races on Unix, where a
 * second builder can truncate the file a third process is about to exec.
 *
 * These tests pin the write itself: where `go build` is pointed, and what the
 * caller gets back. `go` is a shim on PATH rather than the real toolchain, so
 * the assertions are about this module's file handling and nothing else.
 */

/** A fake `go` that records its `-o` target and writes a runnable file there. */
function installGoShim(options: { exitCode?: number } = {}): {
  pathEntry: string
  outputTargets: () => string[]
} {
  const binDir = temporaryDirectory("go-shim")
  const log = path.join(temporaryDirectory("go-shim-log"), "targets.txt")
  const script = `#!/bin/sh
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  prev="$arg"
done
printf '%s\\n' "$out" >> ${JSON.stringify(log)}
if [ ${options.exitCode ?? 0} -ne 0 ]; then
  echo "shim: build refused" >&2
  exit ${options.exitCode ?? 0}
fi
if [ -d "$out" ]; then
  echo "shim: $out is a directory" >&2
  exit 1
fi
printf '#!/bin/sh\\necho relayfile-shim-build\\n' > "$out" || exit 1
`
  writeFileSync(path.join(binDir, "go"), script)
  chmodSync(path.join(binDir, "go"), 0o755)
  return {
    pathEntry: binDir,
    outputTargets: () =>
      existsSync(log)
        ? readFileSync(log, "utf8")
            .split("\n")
            .filter((line) => line !== "")
        : []
  }
}

function goRunResolution(): RelayfileGoRunResolution {
  return {
    kind: "go-run",
    command: "go",
    args: ["run", "./cmd/relayfile-cli"],
    cwd: temporaryDirectory("go-run-build-checkout")
  } as RelayfileGoRunResolution
}

/** A fresh, empty directory to publish into, plus the shared path inside it. */
function buildDirectory(): { directory: string; output: string } {
  const directory = temporaryDirectory("go-run-build-out")
  return { directory, output: path.join(directory, "relayfile-cli") }
}

describe.skipIf(process.platform === "win32")("source-fallback build output", () => {
  it("builds to a private sibling and publishes it under the shared path", () => {
    const go = installGoShim()
    const { directory, output } = buildDirectory()

    const built = buildGoRunBinary(goRunResolution(), {
      env: { PATH: go.pathEntry },
      outputPath: output
    })

    expect(built).toBe(output)
    expect(readFileSync(output, "utf8")).toContain("relayfile-shim-build")

    // The bug: `go build -o` was pointed straight at the shared path, so a
    // build overwrote whatever long-running process was executing it.
    const targets = go.outputTargets()
    expect(targets).toHaveLength(1)
    expect(targets[0]).not.toBe(output)
    expect(path.dirname(targets[0])).toBe(directory)
    expect(
      path.basename(targets[0]).startsWith(`${path.basename(output)}.build-`)
    ).toBe(true)

    // Publishing is a rename, so nothing is left next to the result.
    expect(readdirSync(directory)).toEqual([path.basename(output)])
  })

  it("keeps the .exe extension on the staged build", () => {
    // Windows is where the fallback below actually fires, and the path it
    // returns is handed straight to the host to execute — so the staging
    // suffix goes before the extension, not after it.
    const go = installGoShim()
    const { output } = buildDirectory()
    const windowsOutput = `${output}.exe`

    buildGoRunBinary(goRunResolution(), {
      env: { PATH: go.pathEntry },
      outputPath: windowsOutput
    })

    const staged = path.basename(go.outputTargets()[0])
    expect(staged).toMatch(/^relayfile-cli\.build-[0-9]+-[0-9a-f]+\.exe$/)
  })

  it("gives concurrent builds private paths, never a shared one", () => {
    const go = installGoShim()
    const { output } = buildDirectory()
    const resolution = goRunResolution()
    const env = { PATH: go.pathEntry }

    buildGoRunBinary(resolution, { env, outputPath: output })
    buildGoRunBinary(resolution, { env, outputPath: output })

    const targets = go.outputTargets()
    expect(targets).toHaveLength(2)
    expect(new Set(targets).size).toBe(2)
    expect(targets).not.toContain(output)
  })

  it("falls back to the private path when the shared name cannot be replaced", () => {
    // Windows refuses to rename over a running `.exe`, and there is no way to
    // provoke that on the platforms this suite runs on. An occupied
    // destination stands in for it: what is under test is that a failed
    // publish still yields a usable binary instead of an error.
    const go = installGoShim()
    const { directory, output } = buildDirectory()
    mkdirSync(output, { recursive: true })
    writeFileSync(path.join(output, "occupant"), "held")

    const built = buildGoRunBinary(goRunResolution(), {
      env: { PATH: go.pathEntry },
      outputPath: output
    })

    expect(built).not.toBe(output)
    expect(path.dirname(built)).toBe(directory)
    expect(readFileSync(built, "utf8")).toContain("relayfile-shim-build")
  })

  it("leaves nothing behind when the build fails", () => {
    const go = installGoShim({ exitCode: 2 })
    const { directory, output } = buildDirectory()

    expect(() =>
      buildGoRunBinary(goRunResolution(), {
        env: { PATH: go.pathEntry },
        outputPath: output
      })
    ).toThrow(GoBuildFailedError)

    expect(readdirSync(directory)).toEqual([])
  })

  it("sweeps stale artifacts a crash or a failed publish left behind", () => {
    const go = installGoShim()
    const { output } = buildDirectory()

    const stale = `${output}.build-999999-deadbeef`
    writeFileSync(stale, "stale")
    const old = new Date(Date.now() - 48 * 60 * 60 * 1000)
    utimesSync(stale, old, old)

    const recent = `${output}.build-999998-feedface`
    writeFileSync(recent, "recent")

    buildGoRunBinary(goRunResolution(), {
      env: { PATH: go.pathEntry },
      outputPath: output
    })

    expect(existsSync(stale)).toBe(false)
    // A fallback binary handed to a caller minutes ago may still be running.
    expect(existsSync(recent)).toBe(true)
  })
})
