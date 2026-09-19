import { beforeAll, describe, expect, it } from "vitest"
import {
  assertSurfaceConforms,
  findSurfaceViolations,
  walkCommands,
  RELAY_CLI_EXIT_UNKNOWN_COMMAND,
  type RelayCliSurface
} from "@agent-relay/cli-surface"

import {
  createRelayCliSurface,
  relayfileCommands,
  routableTopLevelNames
} from "./index.js"
import { buildRelayfileBinary } from "./testing/build-binary.js"

/**
 * These tests run the real relayfile Go binary. Nothing here is stubbed: the
 * surface resolves a binary built from `cmd/relayfile-cli` and spawns it, so a
 * passing run proves `agent-relay file <cmd>` reaches the actual
 * implementation and returns its actual exit code.
 */

let binDir: string

beforeAll(() => {
  binDir = buildRelayfileBinary().binDir
  // 3 minutes: a cold `go build` of cmd/relayfile-cli on a clean module cache.
}, 180_000)

function surface(): RelayCliSurface {
  return createRelayCliSurface({
    resolve: { binDirs: [binDir] },
    // Cloud sign-in is exercised in cloud-preflight.test.ts; these tests must
    // never open a browser or touch the caller's Cloud session.
    skipCloudPreflight: true
  })
}

interface Capture {
  stdout: string
  stderr: string
}

async function invoke(argv: readonly string[]): Promise<Capture & { code: number }> {
  const captured: Capture = { stdout: "", stderr: "" }
  const code = await surface().run(argv, {
    stdout: (chunk) => {
      captured.stdout += chunk
    },
    stderr: (chunk) => {
      captured.stderr += chunk
    }
  })
  return { ...captured, code }
}

describe("createRelayCliSurface", () => {
  it("satisfies the RelayCliSurface contract", () => {
    const created = surface()
    expect(created.id).toBe("relayfile")
    expect(created.contract).toBe(1)
    expect(created.version).toMatch(/^\d+\.\d+\.\d+/)
    expect(created.commands.length).toBeGreaterThan(0)
    expect(findSurfaceViolations(created)).toEqual([])
    expect(() => assertSurfaceConforms(created)).not.toThrow()
  })

  it("is structurally assignable to the contract type", () => {
    // Type-level assertion: the surface must satisfy RelayCliSurface without
    // importing anything from @agent-relay/cli-surface at runtime.
    const assignable: RelayCliSurface = surface()
    expect(assignable.id).toBe("relayfile")
  })
})

describe("command tree drift", () => {
  it("routes every command it declares", async () => {
    // Every spec'd command must be reachable. `--help` short-circuits inside
    // the binary before any network or credential work, so this sweeps the
    // whole declared tree against the real dispatcher.
    const failures: string[] = []
    for (const { path } of walkCommands(relayfileCommands())) {
      const result = await invoke([...path, "--help"])
      if (result.code !== 0) {
        failures.push(
          `${path.join(" ")} --help exited ${result.code}: ${result.stderr.trim()}`
        )
        continue
      }
      if (!result.stdout.trim()) {
        failures.push(`${path.join(" ")} --help printed nothing`)
      }
    }
    expect(failures).toEqual([])
  }, 120_000)

  it("declares every command the binary routes", async () => {
    // The reverse direction. The binary rejects an unknown top-level command,
    // so anything it accepts but we do not declare would be invisible to
    // `agent-relay file --help`. command-spec.test.ts pins the tree itself to
    // the binary's own table; this checks the routing edge.
    const unknown = await invoke(["definitely-not-a-relayfile-command"])
    expect(unknown.code).toBe(RELAY_CLI_EXIT_UNKNOWN_COMMAND)

    const declared = new Set<string>()
    for (const command of relayfileCommands()) {
      declared.add(command.name)
      for (const alias of command.aliases ?? []) {
        declared.add(alias)
      }
    }
    // `help`, `__command-spec`, and `version` are the documented exceptions:
    // the binary routes all three outside its command table, so they are
    // routable without being declared. The host renders help itself,
    // `__command-spec` is the introspection hook that produces the snapshot,
    // and `version` is `wantsVersion`'s alias for `--version`.
    for (const routable of ["help", "__command-spec", "version"]) {
      expect(declared.has(routable)).toBe(false)
      expect(routableTopLevelNames()).toContain(routable)
    }
  }, 60_000)

  it("declares aliases the binary actually accepts", async () => {
    const aliased = relayfileCommands().filter((command) => command.aliases?.length)
    expect(aliased.length).toBeGreaterThan(0)
    for (const command of aliased) {
      for (const alias of command.aliases ?? []) {
        const result = await invoke([alias, "--help"])
        expect(result.code, `${alias} --help`).toBe(0)
      }
    }
  }, 60_000)
})

describe("run", () => {
  it("returns the binary's real exit code and output", async () => {
    const version = await invoke(["--version"])
    expect(version.code).toBe(0)
    expect(version.stdout.trim()).toMatch(/^\d+\.\d+\.\d+/)
    expect(version.stderr).toBe("")
  })

  it("routes a bare `version` token the way the binary does", async () => {
    // The binary's `wantsVersion` accepts `version` as well as `--version`, so
    // `agent-relay file version` must reach it rather than trip the surface's
    // own unknown-command guard.
    const spelled = await invoke(["version"])
    const flagged = await invoke(["--version"])
    expect(spelled.code).toBe(0)
    expect(spelled.stdout).toBe(flagged.stdout)
    expect(spelled.stdout.trim()).toMatch(/^\d+\.\d+\.\d+/)
    expect(spelled.stderr).toBe("")
  })

  it("lets the binary reject `version` with arguments", async () => {
    // `wantsVersion` only matches a lone `version`, so `version --json` falls
    // through to the binary's dispatcher. The surface must not pre-empt that
    // with its own exit 2: the error has to come from the binary.
    const result = await invoke(["version", "--json"])
    expect(result.code).not.toBe(0)
    expect(result.stderr).toContain("unknown subcommand")
    expect(result.stderr).not.toContain('unknown command "version"')
  }, 30_000)

  it("returns a non-zero code from a real failure", async () => {
    // `workspace use` with a workspace that cannot exist fails inside the
    // binary; the surface must surface its code, not swallow it.
    const result = await invoke(["workspace", "use", "surface-test-missing-workspace"])
    expect(result.code).not.toBe(0)
    expect(result.stderr).not.toBe("")
  }, 30_000)

  it("reports an unknown command as exit 2 without spawning", async () => {
    const result = await invoke(["nope"])
    expect(result.code).toBe(RELAY_CLI_EXIT_UNKNOWN_COMMAND)
    expect(result.stderr).toContain('unknown command "nope"')
    expect(result.stdout).toBe("")
  })

  it("passes flag-leading argv through to the binary", async () => {
    // A leading flag is not a command name, so it must reach the binary
    // rather than trip the unknown-command guard.
    const result = await invoke(["--help"])
    expect(result.code).toBe(0)
    // Mounted, the binary names the host rather than itself (relayfile#509),
    // so this also proves the program name reached the child.
    expect(result.stdout).toContain("agent-relay file is the RelayFile CLI")
  })

  it("writes only through the injected io", async () => {
    const stdoutWrite = process.stdout.write
    const stderrWrite = process.stderr.write
    const direct: string[] = []
    process.stdout.write = ((chunk: string) => {
      direct.push(String(chunk))
      return true
    }) as typeof process.stdout.write
    process.stderr.write = ((chunk: string) => {
      direct.push(String(chunk))
      return true
    }) as typeof process.stderr.write
    try {
      const result = await invoke(["--version"])
      expect(result.stdout.trim()).toMatch(/^\d+\.\d+\.\d+/)
    } finally {
      process.stdout.write = stdoutWrite
      process.stderr.write = stderrWrite
    }
    expect(direct).toEqual([])
  })

  it("installs no signal handlers", async () => {
    const before = {
      SIGINT: process.listenerCount("SIGINT"),
      SIGTERM: process.listenerCount("SIGTERM"),
      SIGHUP: process.listenerCount("SIGHUP")
    }
    await invoke(["--version"])
    expect({
      SIGINT: process.listenerCount("SIGINT"),
      SIGTERM: process.listenerCount("SIGTERM"),
      SIGHUP: process.listenerCount("SIGHUP")
    }).toEqual(before)
  })

  it("never calls process.exit", async () => {
    const realExit = process.exit
    let exitCalls = 0
    process.exit = ((code?: number) => {
      exitCalls += 1
      throw new Error(`process.exit(${code}) called`)
    }) as typeof process.exit
    try {
      const result = await invoke(["--version"])
      expect(result.code).toBe(0)
    } finally {
      process.exit = realExit
    }
    expect(exitCalls).toBe(0)
  })
})
