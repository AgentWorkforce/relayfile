import { afterEach, describe, expect, it } from "vitest"
import { ensureRelayfileMount } from "./workspace-mount.js"

describe("workspace mount entry point", () => {
  const previousLayout = process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT

  afterEach(() => {
    if (previousLayout === undefined) {
      delete process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
    } else {
      process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = previousLayout
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
})
