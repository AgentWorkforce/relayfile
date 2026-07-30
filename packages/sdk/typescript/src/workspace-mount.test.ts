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

  it("refuses inherited scoped layout before resolving or starting the mount binary", async () => {
    process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = "scoped"

    await expect(
      ensureRelayfileMount({
        relayfileUrl: "https://relayfile.mount.test",
        workspace: "ws_123",
        token: "rf_mount_token",
        binaryPath: "/does/not/exist"
      })
    ).rejects.toMatchObject({
      name: "MountSessionInputError",
      code: "mount_session_input_error"
    })
  })
})
