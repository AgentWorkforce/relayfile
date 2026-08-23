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
  MountMultiPathUnsupportedError,
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

function createCheckpointMountEnv(
  localDir: string,
  mode: "poll" | "fuse" = "poll"
) {
  return {
    ...createMountEnv(localDir, mode),
    RELAYFILE_REMOTE_PATH: "/",
    RELAYFILE_MOUNT_SCOPES:
      "fs:read fs:write sync:trigger sync:read ops:read"
  }
}

async function writeReadyState(localDir: string, mode: "poll" | "fuse" = "poll") {
  await mkdir(path.join(localDir, ".relay"), { recursive: true })
  await writeFile(
    path.join(localDir, ".relay", "state.json"),
    JSON.stringify({
      mode,
      intervalMs: 30_000,
      lastReconcileAt: new Date().toISOString(),
      providers: [{ status: "ready" }]
    }),
    "utf8"
  )
}

function validCheckpointSeal(overrides: Record<string, unknown> = {}) {
  return {
    sealId: "seal_123",
    sealToken: "one-use-secret",
    workspaceId: "ws_123",
    root: "/",
    sessionId: "session-123",
    generation: 7,
    digest: `sha256:${"a".repeat(64)}`,
    workspaceRevision: "rev_123",
    eventCursor: "evt_123",
    issuedAt: "2026-08-23T10:00:00.000Z",
    expiresAt: "2026-08-23T10:01:00.000Z",
    ...overrides
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

  it("refuses multi-path configuration before filesystem or process side effects", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-multipath-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const spawnImpl = vi.fn()
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await expect(
        launcher.start({
          env: {
            ...createMountEnv(localDir),
            RELAYFILE_MOUNT_PATHS_FILE: "/tmp/relayfile-paths.json"
          },
          readyTimeoutMs: 50
        })
      ).rejects.toMatchObject({
        name: "MountMultiPathUnsupportedError",
        code: "mount_multi_path_unsupported"
      } satisfies Partial<MountMultiPathUnsupportedError>)
      expect(spawnImpl).not.toHaveBeenCalled()
      await expect(stat(localDir)).rejects.toMatchObject({ code: "ENOENT" })
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("refuses inherited multi-path configuration before filesystem or process side effects", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-inherited-multipath-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const spawnImpl = vi.fn()
    const launcher = createDefaultMountLauncher({ spawnImpl })
    const previousPathsFile = process.env.RELAYFILE_MOUNT_PATHS_FILE
    process.env.RELAYFILE_MOUNT_PATHS_FILE = "/tmp/relayfile-paths.json"

    try {
      await expect(
        launcher.start({
          env: createMountEnv(localDir),
          readyTimeoutMs: 50
        })
      ).rejects.toMatchObject({
        name: "MountMultiPathUnsupportedError",
        code: "mount_multi_path_unsupported"
      } satisfies Partial<MountMultiPathUnsupportedError>)
      expect(spawnImpl).not.toHaveBeenCalled()
      await expect(stat(localDir)).rejects.toMatchObject({ code: "ENOENT" })
    } finally {
      if (previousPathsFile === undefined) {
        delete process.env.RELAYFILE_MOUNT_PATHS_FILE
      } else {
        process.env.RELAYFILE_MOUNT_PATHS_FILE = previousPathsFile
      }
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("refuses direct scoped-layout configuration before filesystem or process side effects", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-scoped-layout-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const spawnImpl = vi.fn()
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await expect(
        launcher.start({
          env: {
            ...createMountEnv(localDir),
            RELAYFILE_MOUNT_LOCAL_LAYOUT: "scoped"
          },
          readyTimeoutMs: 50
        })
      ).rejects.toMatchObject({
        name: "MountSessionInputError",
        code: "mount_session_input_error"
      })
      expect(spawnImpl).not.toHaveBeenCalled()
      await expect(stat(localDir)).rejects.toMatchObject({ code: "ENOENT" })
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("refuses inherited scoped-layout configuration before filesystem or process side effects", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-inherited-scoped-layout-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const spawnImpl = vi.fn()
    const launcher = createDefaultMountLauncher({ spawnImpl })
    const previousLayout = process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
    process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = "scoped"

    try {
      await expect(
        launcher.start({ env: createMountEnv(localDir), readyTimeoutMs: 50 })
      ).rejects.toMatchObject({
        name: "MountSessionInputError",
        code: "mount_session_input_error"
      })
      expect(spawnImpl).not.toHaveBeenCalled()
      await expect(stat(localDir)).rejects.toMatchObject({ code: "ENOENT" })
    } finally {
      if (previousLayout === undefined) {
        delete process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
      } else {
        process.env.RELAYFILE_MOUNT_LOCAL_LAYOUT = previousLayout
      }
      await rm(tempRoot, { recursive: true, force: true })
    }
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

  it("stops a ready poll daemon before running the one-shot checkpoint command", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const checkpoint = new FakeChildProcess()
    const events: string[] = []
    const spawnImpl = vi.fn().mockImplementation((_command, args: string[]) => {
      if (spawnImpl.mock.calls.length === 1) {
        events.push("daemon-started")
        return daemon as never
      }
      events.push(`checkpoint-started:${daemon.killSignals.join(",")}`)
      queueMicrotask(() => {
        checkpoint.stdout.write(`${JSON.stringify(validCheckpointSeal())}\n`)
        checkpoint.exitCode = 0
        checkpoint.emit("exit", 0, null)
      })
      return checkpoint as never
    })
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: createCheckpointMountEnv(localDir),
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_invalid_input" })
      await expect(instance.checkpointAndSeal?.({
        generation: 7
      } as never)).rejects.toMatchObject({ code: "checkpoint_seal_invalid_input" })
      expect(daemon.killSignals).toEqual([])

      const seal = await instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7,
        timeoutMs: 250,
        ttlSeconds: 45
      })

      expect(seal).toMatchObject(validCheckpointSeal())
      expect(events).toEqual(["daemon-started", "checkpoint-started:SIGTERM"])
      expect(spawnImpl).toHaveBeenNthCalledWith(
        2,
        expect.any(String),
        [
          "--checkpoint-and-seal",
          "--checkpoint-session", "session-123",
          "--checkpoint-generation", "7",
          "--checkpoint-seal-ttl", "45s",
          "--timeout", "250ms"
        ],
        expect.objectContaining({
          cwd: localDir,
          stdio: ["ignore", "pipe", "pipe"]
        })
      )
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("rejects FUSE checkpoints before unmounting the source", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-fuse-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const spawnImpl = vi.fn().mockReturnValue(daemon as never)
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir, "fuse")
      const instance = await launcher.start({
        env: createCheckpointMountEnv(localDir, "fuse"),
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_mode_unavailable" })
      expect(instance.stopped).toBe(false)
      expect(daemon.killSignals).toEqual([])
      expect(spawnImpl).toHaveBeenCalledTimes(1)
      await instance.stop()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("rejects pull-only checkpoints before stopping the source", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-pull-only-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const spawnImpl = vi.fn().mockReturnValue(daemon as never)
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: {
          ...createCheckpointMountEnv(localDir),
          RELAYFILE_MOUNT_SYNC_MODE: "pull-only"
        },
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_mode_unavailable" })
      expect(instance.stopped).toBe(false)
      expect(daemon.killSignals).toEqual([])
      expect(spawnImpl).toHaveBeenCalledTimes(1)
      await instance.stop()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("rejects a non-root checkpoint before stopping the source", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-root-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const spawnImpl = vi.fn().mockReturnValue(daemon as never)
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: {
          ...createMountEnv(localDir),
          RELAYFILE_MOUNT_SCOPES:
            "fs:read fs:write sync:trigger sync:read ops:read"
        },
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_root_unavailable" })
      expect(instance.stopped).toBe(false)
      expect(daemon.killSignals).toEqual([])
      expect(spawnImpl).toHaveBeenCalledTimes(1)
      await instance.stop()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("checks inherited effective mount configuration before stopping the source", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-inherited-root-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const spawnImpl = vi.fn().mockReturnValue(daemon as never)
    const launcher = createDefaultMountLauncher({ spawnImpl })
    const previousRemotePath = process.env.RELAYFILE_REMOTE_PATH
    const { RELAYFILE_REMOTE_PATH: _omittedRemotePath, ...inheritedEnv } =
      createCheckpointMountEnv(localDir)

    try {
      process.env.RELAYFILE_REMOTE_PATH = "/inherited-scope"
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: inheritedEnv,
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_root_unavailable" })
      expect(instance.stopped).toBe(false)
      expect(daemon.killSignals).toEqual([])
      expect(spawnImpl).toHaveBeenCalledTimes(1)
      await instance.stop()
    } finally {
      if (previousRemotePath === undefined) {
        delete process.env.RELAYFILE_REMOTE_PATH
      } else {
        process.env.RELAYFILE_REMOTE_PATH = previousRemotePath
      }
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it.each([
    ["missing ops", "fs:read fs:write sync:trigger"],
    ["missing trigger", "fs:read fs:write ops:read"],
    [
      "narrow filesystem grants",
      "relayfile:fs:read:/notion/** relayfile:fs:write:/notion/** sync:trigger ops:read"
    ]
  ])("rejects %s checkpoint scopes before stopping the source", async (_name, scopes) => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-scopes-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const spawnImpl = vi.fn().mockReturnValue(daemon as never)
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: {
          ...createCheckpointMountEnv(localDir),
          RELAYFILE_MOUNT_SCOPES: scopes
        },
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_scope_unavailable" })
      expect(instance.stopped).toBe(false)
      expect(daemon.killSignals).toEqual([])
      expect(spawnImpl).toHaveBeenCalledTimes(1)
      await instance.stop()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("rejects a server seal bound to a different workspace", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-binding-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const checkpoint = new FakeChildProcess()
    const spawnImpl = vi.fn().mockImplementation(() => {
      if (spawnImpl.mock.calls.length === 1) return daemon as never
      queueMicrotask(() => {
        checkpoint.stdout.write(JSON.stringify(validCheckpointSeal({ workspaceId: "ws_other" })))
        checkpoint.exitCode = 0
        checkpoint.emit("exit", 0, null)
      })
      return checkpoint as never
    })
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: createCheckpointMountEnv(localDir),
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7
      })).rejects.toMatchObject({ code: "checkpoint_seal_invalid_output" })
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("kills a checkpoint command that exceeds its deadline", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-checkpoint-timeout-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const daemon = new FakeChildProcess()
    const checkpoint = new FakeChildProcess()
    const spawnImpl = vi.fn().mockImplementation(() =>
      spawnImpl.mock.calls.length === 1 ? daemon as never : checkpoint as never
    )
    const launcher = createDefaultMountLauncher({ spawnImpl })

    try {
      await writeReadyState(localDir)
      const instance = await launcher.start({
        env: createCheckpointMountEnv(localDir),
        readyTimeoutMs: 50
      })
      await instance.ready

      await expect(instance.checkpointAndSeal?.({
        sessionId: "session-123",
        generation: 7,
        timeoutMs: 5
      })).rejects.toMatchObject({ code: "checkpoint_seal_timeout" })
      expect(checkpoint.killSignals).toContain("SIGKILL")
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

  it("reads scoped-layout state from the resolved mount root", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-default-launcher-scoped-status-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const scopedDir = path.join(localDir, "slack", "channels", "C123")
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)

    try {
      const lastReconcileAt = new Date(Date.now() - 1_000).toISOString()
      await mkdir(path.join(scopedDir, ".relay"), { recursive: true })
      await writeFile(
        path.join(scopedDir, ".relay", "state.json"),
        JSON.stringify({
          mode: "poll",
          intervalMs: 30_000,
          lastReconcileAt,
          daemon: { pid: 8888 },
          providers: [{ status: "ready" }]
        }),
        "utf8"
      )

      const status = await readMountedWorkspaceStatus({
        localDir,
        workspaceId: "ws_123",
        remotePath: "/slack/channels/C123",
        mode: "poll",
        localLayout: "scoped",
        relayfileBaseUrl: "https://relayfile.mount.test",
        relayfileToken: "rf_mount_token",
        expiresAt: null,
        suggestedRefreshAt: null
      })

      expect(status).toMatchObject({
        ready: true,
        mode: "poll",
        pid: 8888,
        lastReconcileAt
      })
      expect(fetchMock).not.toHaveBeenCalled()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })
})
