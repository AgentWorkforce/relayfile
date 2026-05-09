import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { PassThrough } from "node:stream"
import { EventEmitter } from "node:events"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  createDefaultMountLauncher,
  readMountedWorkspaceStatus
} from "./mount-launcher.js"
import {
  MountModeUnavailableError,
  MountReadyTimeoutError
} from "./setup-errors.js"

class FakeChildProcess extends EventEmitter {
  readonly stdout = new PassThrough()
  readonly stderr = new PassThrough()
  readonly pid = 4242

  exitCode: number | null = null
  killed = false
  killSignals: string[] = []

  kill(signal: NodeJS.Signals | number = "SIGTERM"): boolean {
    const normalized = typeof signal === "number" ? String(signal) : signal
    this.killSignals.push(normalized)
    this.killed = true
    queueMicrotask(() => {
      this.exitCode = normalized === "SIGKILL" ? 137 : 0
      this.emit("exit", this.exitCode, normalized)
    })
    return true
  }
}

function createMountEnv(localDir: string, mode: "poll" | "fuse" = "poll") {
  return {
    RELAYFILE_BASE_URL: "https://relayfile.mount.test",
    RELAYFILE_TOKEN: "rf_mount_token",
    RELAYFILE_WORKSPACE: "ws_123",
    RELAYFILE_REMOTE_PATH: "/notion",
    RELAYFILE_LOCAL_DIR: localDir,
    RELAYFILE_MOUNT_MODE: mode
  }
}

describe("default mount launcher", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  afterEach(async () => {
    vi.useRealTimers()
  })

  it("times out readiness, then stops the child process", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-timeout-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const child = new FakeChildProcess()
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("boom", { status: 500 }))
    )
    const launcher = createDefaultMountLauncher({
      spawnImpl: vi.fn().mockReturnValue(child as never),
      readyPollIntervalMs: 1
    })

    try {
      const instance = await launcher.start({
        env: createMountEnv(localDir),
        readyTimeoutMs: 5
      })

      await expect(instance.ready).rejects.toBeInstanceOf(MountReadyTimeoutError)
      expect(child.killSignals).toContain("SIGTERM")
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("writes pid/log files and stop stays idempotent", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-stop-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const child = new FakeChildProcess()
    const launcher = createDefaultMountLauncher({
      spawnImpl: vi.fn().mockReturnValue(child as never),
      readyPollIntervalMs: 1
    })

    try {
      const lastReconcileAt = new Date(Date.now() - 1_000).toISOString()
      await mkdir(path.join(localDir, ".relay"), { recursive: true })
      await writeFile(
        path.join(localDir, ".relay", "state.json"),
        JSON.stringify({
          mode: "poll",
          intervalMs: 30_000,
          lastReconcileAt,
          providers: [{ status: "ready" }]
        }),
        "utf8"
      )

      const instance = await launcher.start({
        env: createMountEnv(localDir),
        readyTimeoutMs: 50
      })

      await instance.ready
      child.stdout.write("mount ready\n")
      await instance.stop()
      await instance.stop()

      expect(child.killSignals.filter((signal) => signal === "SIGTERM")).toHaveLength(1)
      expect(
        await readFile(path.join(localDir, ".relay", "mount.log"), "utf8")
      ).toContain("mount ready")
      expect((await stat(path.join(localDir, ".relay"))).isDirectory()).toBe(true)
      await expect(stat(path.join(localDir, ".relay", "mount.pid"))).rejects.toBeTruthy()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("translates fuse-mode startup failures to MountModeUnavailableError", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-fuse-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const child = new FakeChildProcess()
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("boom", { status: 500 }))
    )
    const launcher = createDefaultMountLauncher({
      spawnImpl: vi.fn().mockImplementation(() => {
        queueMicrotask(() => {
          child.stderr.write("failed to start fuse mount: fuse mode is not available in this build\n")
          child.exitCode = 1
          child.emit("exit", 1, null)
        })
        return child as never
      }),
      readyPollIntervalMs: 1
    })

    try {
      const instance = await launcher.start({
        env: createMountEnv(localDir, "fuse"),
        readyTimeoutMs: 25
      })

      await expect(instance.ready).rejects.toBeInstanceOf(MountModeUnavailableError)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("reads fresh state before probing the HTTP API", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-status-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)

    try {
      const lastHeartbeatAt = new Date(Date.now() - 250).toISOString()
      const lastReconcileAt = new Date(Date.now() - 1_000).toISOString()
      const lastEventAt = new Date(Date.now() - 500).toISOString()
      await mkdir(path.join(localDir, ".relay"), { recursive: true })
      await writeFile(
        path.join(localDir, ".relay", "state.json"),
        JSON.stringify({
          mode: "poll",
          intervalMs: 30_000,
          lastHeartbeatAt,
          lastReconcileAt,
          lastEventAt,
          pendingWriteback: 1,
          pendingConflicts: 0,
          daemon: { pid: 7777 },
          providers: [{ status: "ready" }]
        }),
        "utf8"
      )

      const status = await readMountedWorkspaceStatus({
        localDir,
        workspaceId: "ws_123",
        remotePath: "/notion",
        mode: "poll",
        relayfileBaseUrl: "https://relayfile.mount.test",
        relayfileToken: "rf_mount_token",
        expiresAt: "2026-05-09T11:00:00.000Z",
        suggestedRefreshAt: "2026-05-09T10:55:00.000Z"
      })

      expect(status).toMatchObject({
        ready: true,
        mode: "poll",
        pid: 7777,
        lastHeartbeatAt,
        lastReconcileAt,
        lastEventAt,
        pendingWriteback: 1,
        pendingConflicts: 0
      })
      expect(fetchMock).not.toHaveBeenCalled()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })
})
