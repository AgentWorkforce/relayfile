import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, expect, it } from "vitest"
import { walkCommands } from "@agent-relay/cli-surface"

import { COMMAND_SPEC_PATH, relayfileCommands } from "./index.js"
import { emitCommandSpec } from "../../scripts/gen-command-spec.mjs"

/**
 * The snapshot in command-spec.json is generated from the Go CLI's own command
 * table (`relayfile __command-spec --json`). It exists so `commands` is
 * available without the binary at import time — which means it can rot. This
 * test regenerates it from the real binary and diffs, so it cannot.
 */

describe("command-spec.json", () => {
  it("matches what the Go command tree emits", () => {
    const emitted = emitCommandSpec()
    const snapshot = readFileSync(COMMAND_SPEC_PATH, "utf8")
    if (emitted !== snapshot) {
      // Point at the fix rather than dumping 1.6k lines of JSON diff.
      const emittedTree = JSON.parse(emitted)
      const snapshotTree = JSON.parse(snapshot)
      expect(snapshotTree, "run: npm run gen:command-spec").toEqual(emittedTree)
      // Identical trees but different bytes: formatting drift.
      expect(snapshot, "run: npm run gen:command-spec").toBe(emitted)
    }
  }, 180_000)

  it("is loaded from the path the build copies into dist", () => {
    expect(path.basename(COMMAND_SPEC_PATH)).toBe("command-spec.json")
    expect(() => readFileSync(COMMAND_SPEC_PATH, "utf8")).not.toThrow()
  })

  it("describes a non-trivial tree with nested groups", () => {
    const commands = relayfileCommands()
    expect(commands.length).toBeGreaterThan(10)

    const names = commands.map((command) => command.name)
    for (const expected of ["setup", "mount", "status", "workspace", "integration"]) {
      expect(names).toContain(expected)
    }

    const workspace = commands.find((command) => command.name === "workspace")
    expect(workspace?.subcommands?.map((sub) => sub.name)).toEqual(
      expect.arrayContaining(["create", "join", "use", "list", "current", "view", "status", "delete"])
    )

    // `workspace view` proves three-level nesting survives the round trip.
    const view = workspace?.subcommands?.find((sub) => sub.name === "view")
    expect(view?.subcommands?.map((sub) => sub.name)).toEqual(["add", "list", "remove"])
  })

  it("carries no machine-specific default values", () => {
    // The snapshot is checked in, so a default derived from the generating
    // machine's environment (home directory, socket path, server override)
    // would both leak a local path and make the snapshot unreproducible.
    const offenders: string[] = []
    for (const { path: commandPath, command } of walkCommands(relayfileCommands())) {
      for (const option of command.options ?? []) {
        if (option.defaultValue === undefined) continue
        const value = String(option.defaultValue)
        if (value.includes("/home/") || value.includes("/Users/") || value.includes(path.sep + "tmp")) {
          offenders.push(`${commandPath.join(" ")} ${option.flags} = ${value}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })

  it("keeps the introspection hook out of the published tree", () => {
    const names = new Set<string>()
    for (const { command } of walkCommands(relayfileCommands())) {
      names.add(command.name)
    }
    expect(names.has("__command-spec")).toBe(false)
    expect(names.has("help")).toBe(false)
  })
})
