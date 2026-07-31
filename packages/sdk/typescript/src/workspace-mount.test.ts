import { afterEach, describe, expect, it } from "vitest"
import { chmod, mkdtemp, rm, stat, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { ensureRelayfileMount } from "./workspace-mount.js"

describe("workspace mount entry point", () => {
  const previousLayout = process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
  const previousRemotePath = process.env.RELAYFILE_REMOTE_PATH

  afterEach(() => {
    if (previousLayout === undefined) {
      delete process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
    } else {
      process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = previousLayout
    }
    if (previousRemotePath === undefined) {
      delete process.env.RELAYFILE_REMOTE_PATH
    } else {
      process.env.RELAYFILE_REMOTE_PATH = previousRemotePath
    }
  })

  it("allows inherited scoped layout to reach mount binary resolution", async () => {
    process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = "scoped"

    await expect(
      ensureRelayfileMount({
        relayfileUrl: "https://relayfile.mount.test",
        workspace: "ws_123",
        token: "rf_mount_token",
        binaryPath: "/does/not/exist"
      })
    ).rejects.toThrow("missing relayfile mount binary: /does/not/exist")
  })

  it("returns the concrete child for an inherited scoped native mount", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-workspace-mount-")
    )
    const binaryPath = path.join(tempRoot, "relayfile-mount")
    const catalogDir = path.join(tempRoot, "catalog")
    await writeFile(
      binaryPath,
      `#!/bin/sh
case " $* " in
  *" --once "*) exit 0 ;;
esac
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
    )
    await chmod(binaryPath, 0o755)
    process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = "scoped"
    process.env.RELAYFILE_REMOTE_PATH = "/github/repos/acme"

    try {
      const handle = await ensureRelayfileMount({
        relayfileUrl: "https://relayfile.mount.test",
        workspace: "ws_123",
        token: "rf_mount_token",
        binaryPath,
        mountPoint: catalogDir
      })

      expect(handle.mountPoint).toBe(
        path.join(catalogDir, "github", "repos", "acme")
      )
      await handle.stop()
      await expect(stat(catalogDir)).resolves.toBeDefined()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })
})
