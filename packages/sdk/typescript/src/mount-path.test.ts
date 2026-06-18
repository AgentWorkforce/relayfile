import { chmod, mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { afterEach, beforeEach, describe, expect, it } from "vitest"
import {
  formatMountNotFoundError,
  getOptionalDepPackageName,
  getRelayfileMountBinaryPath
} from "./mount-path.js"

describe("getOptionalDepPackageName", () => {
  it("follows the @relayfile/mount-<platform>-<arch> convention", () => {
    expect(getOptionalDepPackageName("darwin", "arm64")).toBe(
      "@relayfile/mount-darwin-arm64"
    )
    expect(getOptionalDepPackageName("linux", "x64")).toBe(
      "@relayfile/mount-linux-x64"
    )
  })

  it("defaults to the current process platform/arch", () => {
    expect(getOptionalDepPackageName()).toBe(
      `@relayfile/mount-${process.platform}-${process.arch}`
    )
  })
})

describe("getRelayfileMountBinaryPath", () => {
  let tmpDir: string
  const savedBin = process.env.RELAYFILE_MOUNT_BIN

  beforeEach(async () => {
    tmpDir = await mkdtemp(path.join(os.tmpdir(), "mount-path-"))
    delete process.env.RELAYFILE_MOUNT_BIN
  })

  afterEach(async () => {
    if (savedBin === undefined) {
      delete process.env.RELAYFILE_MOUNT_BIN
    } else {
      process.env.RELAYFILE_MOUNT_BIN = savedBin
    }
    await rm(tmpDir, { recursive: true, force: true })
  })

  it("honours the RELAYFILE_MOUNT_BIN override when it exists", async () => {
    const binary = path.join(tmpDir, "relayfile-mount")
    await writeFile(binary, "#!/bin/sh\n")
    await chmod(binary, 0o755)

    process.env.RELAYFILE_MOUNT_BIN = binary
    expect(getRelayfileMountBinaryPath()).toBe(binary)
  })

  it("ignores a RELAYFILE_MOUNT_BIN override that does not exist", () => {
    process.env.RELAYFILE_MOUNT_BIN = path.join(tmpDir, "does-not-exist")
    // Falls through to other resolution strategies; in CI without the optional
    // dep or a checkout it may be null, but it must never be the bogus path.
    expect(getRelayfileMountBinaryPath()).not.toBe(
      process.env.RELAYFILE_MOUNT_BIN
    )
  })

  it("resolves a source-checkout dist binary when loaded from a checkout", async () => {
    // Synthesize a minimal relayfile source checkout: go.mod +
    // cmd/relayfile-mount/main.go + dist/relayfile-mount-<platform>-<goArch>.
    const goArch = process.arch === "x64" ? "amd64" : process.arch
    await writeFile(path.join(tmpDir, "go.mod"), "module example\n")
    await mkdir(path.join(tmpDir, "cmd", "relayfile-mount"), { recursive: true })
    await writeFile(
      path.join(tmpDir, "cmd", "relayfile-mount", "main.go"),
      "package main\n"
    )
    await mkdir(path.join(tmpDir, "dist"), { recursive: true })
    const distBinary = path.join(
      tmpDir,
      "dist",
      `relayfile-mount-${process.platform}-${goArch}`
    )
    await writeFile(distBinary, "#!/bin/sh\n")
    await chmod(distBinary, 0o755)

    const savedCwd = process.cwd()
    try {
      process.chdir(tmpDir)
      const resolved = getRelayfileMountBinaryPath()
      expect(resolved).not.toBeNull()
      // Compare canonical paths: process.cwd() returns the realpath, which on
      // macOS prefixes the symlinked tmpdir with /private.
      expect(await realpath(resolved as string)).toBe(await realpath(distBinary))
    } finally {
      process.chdir(savedCwd)
    }
  })
})

describe("formatMountNotFoundError", () => {
  it("names the optional-dep package for the current platform", () => {
    expect(formatMountNotFoundError()).toContain(getOptionalDepPackageName())
  })
})
