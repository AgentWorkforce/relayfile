import { spawn, type ChildProcess } from "node:child_process"
import { createWriteStream } from "node:fs"
import {
  mkdir,
  readFile,
  rename,
  stat,
  unlink,
  writeFile
} from "node:fs/promises"
import path from "node:path"
import process from "node:process"
import { RelayFileClient } from "./client.js"
import { getRelayfileMountBinaryPath } from "./mount-path.js"
import {
  CloudAbortError,
  MountModeUnavailableError,
  MountReadyTimeoutError,
  RelayfileSetupError
} from "./setup-errors.js"
import type {
  MountLocalLayout,
  MountLauncher,
  MountLauncherInstance,
  MountLauncherStart,
  MountMode,
  MountSyncMode,
  MountedWorkspaceStatus,
  ReadMountedWorkspaceStatusInput
} from "./setup-types.js"

const DEFAULT_READY_POLL_INTERVAL_MS = 250
const DEFAULT_STOP_TIMEOUT_MS = 10_000
const LOG_ROTATION_MAX_BYTES = 10 * 1024 * 1024
const LOG_ROTATION_FILES = 3
const FUSE_UNAVAILABLE_SIGNATURE = "fuse mode is not available in this build"

interface DefaultMountLauncherOptions {
  spawnImpl?: typeof spawn
  now?: () => number
  readyPollIntervalMs?: number
}

interface MountStateFile {
  mode?: string
  intervalMs?: number
  lastHeartbeatAt?: string
  lastReconcileAt?: string
  lastEventAt?: string
  pendingWriteback?: number
  pendingConflicts?: number
  providers?: Array<{
    status?: string
  }>
  daemon?: {
    pid?: number
  }
}

export const defaultMountLauncher = createDefaultMountLauncher()

export function createDefaultMountLauncher(
  options: DefaultMountLauncherOptions = {}
): MountLauncher {
  return {
    async start(input: MountLauncherStart): Promise<MountLauncherInstance> {
      return startRelayfileMount(input, options)
    }
  }
}

export async function readMountedWorkspaceStatus(
  input: ReadMountedWorkspaceStatusInput
): Promise<MountedWorkspaceStatus> {
  const state = await readMountStateFile(
    resolveMountLocalDir(input.localDir, input.remotePath, input.localLayout)
  )
  if (state && !isMountStateStale(state)) {
    return {
      ready: isMountStateReady(state),
      mode: normalizeMountMode(state.mode) ?? input.mode,
      pid: state.daemon?.pid ?? input.pid,
      lastHeartbeatAt: normalizeIsoString(state.lastHeartbeatAt),
      lastReconcileAt: normalizeIsoString(state.lastReconcileAt),
      lastEventAt: normalizeIsoString(state.lastEventAt),
      expiresAt: input.expiresAt,
      suggestedRefreshAt: input.suggestedRefreshAt,
      pendingWriteback: normalizeInteger(state.pendingWriteback),
      pendingConflicts: normalizeInteger(state.pendingConflicts)
    }
  }

  const ready = await probeMountedWorkspace(input)
  return {
    ready,
    mode: input.mode,
    pid: input.pid,
    expiresAt: input.expiresAt,
    suggestedRefreshAt: input.suggestedRefreshAt
  }
}

async function startRelayfileMount(
  input: MountLauncherStart,
  options: DefaultMountLauncherOptions
): Promise<MountLauncherInstance> {
  const localDir = path.resolve(input.env.RELAYFILE_LOCAL_DIR ?? process.cwd())
  const mountLocalDir = resolveMountLocalDir(
    localDir,
    input.env.RELAYFILE_REMOTE_PATH,
    input.env.RELAYFILE_MOUNT_LOCAL_LAYOUT
  )
  const relayDir = path.join(mountLocalDir, ".relay")
  const logPath = path.join(relayDir, "mount.log")
  const pidPath = path.join(relayDir, "mount.pid")
  await mkdir(relayDir, { recursive: true })
  await rotateMountLogIfNeeded(logPath)

  const command = await resolveRelayfileMountCommand()
  const args = input.background === false ? ["--once"] : []
  const child = (options.spawnImpl ?? spawn)(command, args, {
    cwd: input.cwd ?? mountLocalDir,
    env: {
      ...process.env,
      ...input.env
    },
    stdio: ["ignore", "pipe", "pipe"]
  })

  const outputBuffer: string[] = []
  const logStream = createWriteStream(logPath, { flags: "a" })
  pipeChildOutput(child, logStream, outputBuffer, input)

  if (typeof child.pid === "number" && child.pid > 0) {
    await writeAtomicFile(pidPath, `${child.pid}\n`)
  }

  return new RelayfileMountProcessInstance({
    child,
    logStream,
    pidPath,
    outputBuffer,
    input,
    localDir: mountLocalDir,
    now: options.now ?? Date.now,
    readyPollIntervalMs:
      options.readyPollIntervalMs ?? DEFAULT_READY_POLL_INTERVAL_MS
  })
}

class RelayfileMountProcessInstance implements MountLauncherInstance {
  readonly pid?: number
  readonly ready: Promise<void>

  private readonly child: ChildProcess
  private readonly logStream: NodeJS.WritableStream
  private readonly pidPath: string
  private readonly outputBuffer: string[]
  private readonly input: MountLauncherStart
  private readonly localDir: string
  private readonly now: () => number
  private readonly readyPollIntervalMs: number

  private exited = false
  private stopping?: Promise<void>
  private readyResolved = false

  constructor(input: {
    child: ChildProcess
    logStream: NodeJS.WritableStream
    pidPath: string
    outputBuffer: string[]
    input: MountLauncherStart
    localDir: string
    now: () => number
    readyPollIntervalMs: number
  }) {
    this.child = input.child
    this.logStream = input.logStream
    this.pidPath = input.pidPath
    this.outputBuffer = input.outputBuffer
    this.input = input.input
    this.localDir = input.localDir
    this.pid = input.child.pid ?? undefined
    this.now = input.now
    this.readyPollIntervalMs = input.readyPollIntervalMs

    this.child.once("exit", () => {
      this.exited = true
    })

    this.ready = this.waitForReady()
  }

  async status(): Promise<MountedWorkspaceStatus> {
    return readMountedWorkspaceStatus({
      localDir: this.localDir,
      workspaceId: this.input.env.RELAYFILE_WORKSPACE ?? "",
      remotePath: this.input.env.RELAYFILE_REMOTE_PATH ?? "/",
      mode: normalizeMountMode(this.input.env.RELAYFILE_MOUNT_MODE) ?? "poll",
      localLayout: normalizeMountLocalLayout(this.input.env.RELAYFILE_MOUNT_LOCAL_LAYOUT),
      syncMode: normalizeMountSyncMode(this.input.env.RELAYFILE_MOUNT_SYNC_MODE),
      relayfileBaseUrl: this.input.env.RELAYFILE_BASE_URL ?? "",
      relayfileToken: this.input.env.RELAYFILE_TOKEN ?? "",
      expiresAt: null,
      suggestedRefreshAt: null,
      pid: this.pid
    })
  }

  async stop(): Promise<void> {
    if (!this.stopping) {
      this.stopping = this.performStop()
    }
    await this.stopping
  }

  private async waitForReady(): Promise<void> {
    const startedAt = this.now()
    const timeoutAt = startedAt + this.input.readyTimeoutMs

    for (;;) {
      if (this.input.signal?.aborted) {
        await this.stop()
        throw new CloudAbortError("mountWorkspace")
      }

      const status = await this.status()
      if (status.ready) {
        this.readyResolved = true
        return
      }

      if (this.isFuseUnavailable()) {
        throw new MountModeUnavailableError("fuse")
      }

      if (this.exited) {
        throw this.buildEarlyExitError()
      }

      if (this.now() >= timeoutAt) {
        const error = new MountReadyTimeoutError(
          this.localDir,
          this.input.readyTimeoutMs
        )
        await this.stop()
        throw error
      }

      await delay(this.readyPollIntervalMs)
    }
  }

  private buildEarlyExitError(): Error {
    if (this.isFuseUnavailable()) {
      return new MountModeUnavailableError("fuse")
    }
    return new RelayfileSetupError(
      "relayfile-mount exited before the workspace became ready.",
      "mount_launch_failed"
    )
  }

  private isFuseUnavailable(): boolean {
    return (
      normalizeMountMode(this.input.env.RELAYFILE_MOUNT_MODE) === "fuse" &&
      this.outputBuffer.join("").includes(FUSE_UNAVAILABLE_SIGNATURE)
    )
  }

  private async performStop(): Promise<void> {
    if (!this.exited && typeof this.child.pid === "number") {
      this.child.kill("SIGTERM")
      await waitForExit(this.child, DEFAULT_STOP_TIMEOUT_MS)
    }
    if (!this.exited && typeof this.child.pid === "number") {
      this.child.kill("SIGKILL")
      await waitForExit(this.child, 1_000)
    }
    if (!this.readyResolved) {
      this.exited = true
    }
    this.logStream.end()
    await unlinkIfExists(this.pidPath)
  }
}

function pipeChildOutput(
  child: ChildProcess,
  logStream: NodeJS.WritableStream,
  outputBuffer: string[],
  input: MountLauncherStart
): void {
  const appendChunk = (streamName: "stdout" | "stderr", chunk: Buffer | string) => {
    const text = typeof chunk === "string" ? chunk : chunk.toString("utf8")
    logStream.write(text)
    outputBuffer.push(text)
    if (outputBuffer.length > 32) {
      outputBuffer.splice(0, outputBuffer.length - 32)
    }
    input.onEvent?.({
      type: streamName,
      text
    })
  }

  child.stdout?.on("data", (chunk) => {
    appendChunk("stdout", chunk)
  })
  child.stderr?.on("data", (chunk) => {
    appendChunk("stderr", chunk)
  })
}

async function probeMountedWorkspace(
  input: ReadMountedWorkspaceStatusInput
): Promise<boolean> {
  const client = new RelayFileClient({
    baseUrl: input.relayfileBaseUrl,
    token: input.relayfileToken
  })
  try {
    await client.listTree(input.workspaceId, {
      path: input.remotePath,
      depth: 1
    })
    return true
  } catch {
    return false
  }
}

async function readMountStateFile(localDir: string): Promise<MountStateFile | null> {
  const statePath = path.join(localDir, ".relay", "state.json")
  try {
    const payload = await readFile(statePath, "utf8")
    const parsed = JSON.parse(payload) as unknown
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return null
    }
    return parsed as MountStateFile
  } catch {
    return null
  }
}

function isMountStateReady(state: MountStateFile): boolean {
  if (!normalizeIsoString(state.lastReconcileAt)) {
    return false
  }
  const providers = Array.isArray(state.providers) ? state.providers : []
  return providers.every((provider) => {
    const status = normalizeNonEmptyString(provider?.status)
    return status === undefined || status === "ready" || status === "syncing" || status === "unknown"
  })
}

function isMountStateStale(state: MountStateFile): boolean {
  const lastReconcileAt = normalizeIsoString(state.lastReconcileAt)
  const intervalMs = normalizeInteger(state.intervalMs)
  if (!lastReconcileAt || !intervalMs || intervalMs <= 0) {
    return false
  }
  const reconciledAt = Date.parse(lastReconcileAt)
  return !Number.isNaN(reconciledAt) && Date.now() - reconciledAt > intervalMs * 2
}

function normalizeMountMode(mode?: string): MountMode | undefined {
  return mode === "fuse" ? "fuse" : mode === "poll" ? "poll" : undefined
}

function normalizeMountLocalLayout(layout?: string): MountLocalLayout {
  return layout === "scoped" ? "scoped" : "exact"
}

function normalizeMountSyncMode(mode?: string): MountSyncMode {
  return mode === "write-only" ? "write-only" : "mirror"
}

function resolveMountLocalDir(
  localDir: string,
  remotePath?: string,
  localLayout?: string
): string {
  const root = path.resolve(localDir)
  if (normalizeMountLocalLayout(localLayout) !== "scoped") {
    return root
  }
  const normalizedRemote = normalizeRemotePath(remotePath)
  if (normalizedRemote === "/") {
    return root
  }
  return path.join(root, ...normalizedRemote.split("/").filter(Boolean))
}

function normalizeRemotePath(remotePath?: string): string {
  const trimmed = typeof remotePath === "string" ? remotePath.trim() : ""
  if (!trimmed || trimmed === "/") {
    return "/"
  }
  const slashNormalized = trimmed.replace(/\\/g, "/")
  const normalized = path.posix.normalize(
    slashNormalized.startsWith("/") ? slashNormalized : `/${slashNormalized}`
  )
  return normalized === "/" ? "/" : normalized.replace(/\/+$/, "")
}

function normalizeIsoString(value: unknown): string | undefined {
  if (typeof value !== "string" || value.trim() === "") {
    return undefined
  }
  return Number.isNaN(Date.parse(value)) ? undefined : value
}

function normalizeInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.trunc(value)
    : undefined
}

function normalizeNonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined
}

async function resolveRelayfileMountCommand(): Promise<string> {
  // Delegates to the shared resolver, which checks the RELAYFILE_MOUNT_BIN
  // override, local source-checkout builds, the platform-specific optional-dep
  // package (@relayfile/mount-<platform>-<arch>), then PATH. Falls back to the
  // bare command name so spawn surfaces a clear ENOENT if nothing is found.
  return getRelayfileMountBinaryPath() ?? "relayfile-mount"
}

async function rotateMountLogIfNeeded(logPath: string): Promise<void> {
  try {
    const info = await stat(logPath)
    if (info.size < LOG_ROTATION_MAX_BYTES) {
      return
    }
  } catch {
    return
  }

  const oldest = `${logPath}.${LOG_ROTATION_FILES}`
  await unlinkIfExists(oldest)
  for (let index = LOG_ROTATION_FILES - 1; index >= 1; index -= 1) {
    const source = `${logPath}.${index}`
    const target = `${logPath}.${index + 1}`
    try {
      await rename(source, target)
    } catch {
      // ignore missing rotation slots
    }
  }
  try {
    await rename(logPath, `${logPath}.1`)
  } catch {
    // ignore missing active log
  }
}

async function writeAtomicFile(targetPath: string, content: string): Promise<void> {
  const tempPath = `${targetPath}.tmp-${process.pid}-${Date.now()}`
  await mkdir(path.dirname(targetPath), { recursive: true })
  await writeFile(tempPath, content, "utf8")
  await rename(tempPath, targetPath)
}

async function waitForExit(
  child: ChildProcess,
  timeoutMs: number
): Promise<void> {
  if (child.exitCode !== null) {
    return
  }

  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      child.removeListener("exit", onExit)
      resolve()
    }, timeoutMs)
    const onExit = () => {
      clearTimeout(timer)
      child.removeListener("exit", onExit)
      resolve()
    }
    child.once("exit", onExit)
  })
}

async function unlinkIfExists(targetPath: string): Promise<void> {
  try {
    await unlink(targetPath)
  } catch {
    // ignore
  }
}

async function delay(delayMs: number): Promise<void> {
  await new Promise<void>((resolve) => {
    setTimeout(resolve, delayMs)
  })
}
