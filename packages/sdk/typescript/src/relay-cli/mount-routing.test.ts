import { spawnSync } from "node:child_process"
import path from "node:path"

import { beforeAll, describe, expect, it } from "vitest"

import { createRelayCliSurface, relayfileCommands } from "./index.js"
import { buildRelayfileBinary, temporaryDirectory } from "./testing/build-binary.js"

/**
 * `mount` is the awkward shape in relayfile's command tree: it takes two
 * optional positionals *and* has four hidden subcommands, so a generic mounter
 * could plausibly mistake `relayfile mount my-workspace ./dir` for a
 * subcommand lookup, or hide `mount checkpoint-seal` behind the positionals.
 *
 * Rather than assert what each flag does, these tests prove equivalence: for a
 * set of argv shapes, `surface.run(argv)` must produce the same exit code and
 * the same bytes as spawning the binary with that argv directly. That covers
 * positionals, hidden subcommands, aliases, and flags in one assertion, and it
 * cannot pass by accident.
 *
 * Every shape here fails inside the binary's own argument validation or
 * credential lookup, so nothing touches the network. `HOME` points at an empty
 * directory so the binary never reads the caller's real relayfile state.
 */

let binDir: string
let binaryPath: string
let home: string

beforeAll(() => {
  binDir = buildRelayfileBinary().binDir
  binaryPath = path.join(
    binDir,
    process.platform === "win32" ? "relayfile.exe" : "relayfile"
  )
  home = temporaryDirectory("mount-routing-home")
  // 3 minutes: a cold `go build` of cmd/relayfile-cli.
}, 180_000)

interface Capture {
  code: number
  stdout: string
  stderr: string
}

/**
 * Env for a direct binary spawn used as the comparison baseline.
 *
 * RELAYFILE_PROGRAM_NAME matches what the surface sets, because these tests
 * assert that argv *routes* identically — not that the binary names itself
 * identically. Mounted, it deliberately says `agent-relay file` so its advice
 * points at a binary the user actually has (relayfile#509). Without this the
 * comparison fails on the one difference the mount is supposed to make.
 */
function childEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: home,
    USERPROFILE: home,
    RELAYFILE_PROGRAM_NAME: "agent-relay file"
  }
}

async function throughSurface(argv: readonly string[]): Promise<Capture> {
  const captured = { stdout: "", stderr: "" }
  const surface = createRelayCliSurface({
    resolve: { binDirs: [binDir] },
    env: childEnv(),
    // Cloud sign-in is exercised in cloud-preflight.test.ts; these tests must
    // never open a browser or touch the caller's Cloud session.
    skipCloudPreflight: true
  })
  const code = await surface.run(argv, {
    stdout: (chunk) => {
      captured.stdout += chunk
    },
    stderr: (chunk) => {
      captured.stderr += chunk
    }
  })
  return { ...captured, code }
}

function directly(argv: readonly string[]): Capture {
  const result = spawnSync(binaryPath, [...argv], {
    encoding: "utf8",
    env: childEnv()
  })
  if (result.error) {
    throw result.error
  }
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? ""
  }
}

/** argv shapes that exercise every way `mount` can be invoked. */
const MOUNT_INVOCATIONS: ReadonlyArray<{ label: string; argv: readonly string[] }> = [
  { label: "no positionals", argv: ["mount", "--help"] },
  {
    label: "both positionals",
    argv: ["mount", "surface-test-missing-workspace", "/tmp/relayfile-surface-test"]
  },
  {
    label: "positionals plus flags",
    argv: [
      "mount",
      "surface-test-missing-workspace",
      "/tmp/relayfile-surface-test",
      "--mode",
      "poll",
      "--once"
    ]
  },
  {
    label: "workspace positional only",
    argv: ["mount", "surface-test-missing-workspace", "--once"]
  },
  { label: "hidden subcommand, no flags", argv: ["mount", "checkpoint-seal"] },
  {
    label: "hidden subcommand with flags",
    argv: ["mount", "checkpoint-seal", "--root", "/tmp/relayfile-surface-test", "--json"]
  },
  {
    label: "hidden subcommand resume-seal",
    argv: ["mount", "resume-seal", "--root", "/tmp/relayfile-surface-test", "--json"]
  },
  { label: "hidden subcommand verify-seal", argv: ["mount", "verify-seal", "--help"] },
  { label: "hidden subcommand handback-seal", argv: ["mount", "handback-seal", "--help"] },
  { label: "alias start", argv: ["start", "--help"] },
  { label: "alias on", argv: ["on", "--help"] },
  {
    label: "alias with positionals",
    argv: ["start", "surface-test-missing-workspace", "/tmp/relayfile-surface-test"]
  }
]

describe("mount routes through the surface exactly as the binary does", () => {
  it.each(MOUNT_INVOCATIONS)("$label", async ({ argv }) => {
    const mounted = await throughSurface(argv)
    const native = directly(argv)

    expect(mounted.code, `exit code for \`${argv.join(" ")}\``).toBe(native.code)
    expect(mounted.stdout).toBe(native.stdout)
    expect(mounted.stderr).toBe(native.stderr)
    // The surface must never have short-circuited: its own unknown-command
    // path is the one failure mode that would look like a routing success.
    expect(mounted.stderr).not.toContain("unknown command")
  }, 60_000)

  it("reaches the mount implementation, not a subcommand lookup", async () => {
    // A positional that is not one of the hidden subcommand names must be
    // handled as WORKSPACE. The binary echoes it back in its own error, which
    // is proof the argument arrived where mount expected it.
    const result = await throughSurface([
      "mount",
      "surface-test-missing-workspace",
      "/tmp/relayfile-surface-test"
    ])
    expect(result.code).not.toBe(0)
    expect(result.stderr).toContain("surface-test-missing-workspace")
  }, 60_000)

  it("returns the binary's exit 2, not its own unknown-command exit 2", async () => {
    // `mount checkpoint-seal` with no flags exits 2 from inside the binary,
    // colliding with RELAY_CLI_EXIT_UNKNOWN_COMMAND. The two must stay
    // distinguishable by what actually ran.
    const result = await throughSurface(["mount", "checkpoint-seal"])
    expect(result.code).toBe(2)
    expect(result.stderr).toContain("checkpoint_invalid_input")
    expect(result.stderr).not.toContain("unknown command")
  }, 60_000)

  it("declares every hidden mount subcommand the binary routes", () => {
    const mount = relayfileCommands().find((command) => command.name === "mount")
    expect(mount).toBeDefined()
    const hidden = (mount?.subcommands ?? []).filter((child) => child.hidden)
    expect(hidden.map((child) => child.name).sort()).toEqual([
      "checkpoint-seal",
      "handback-seal",
      "resume-seal",
      "verify-seal"
    ])
    // Positionals stay optional, so `mount` alone and `mount <subcommand>`
    // both remain expressible in the declared tree.
    expect(mount?.args?.every((arg) => !arg.required)).toBe(true)
  })
})
