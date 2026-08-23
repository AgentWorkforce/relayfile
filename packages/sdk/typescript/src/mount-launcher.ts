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
import { assertExactMountLayout } from "./mount-layout-guard.js"
import {
  CloudAbortError,
  MountModeUnavailableError,
  MountMultiPathUnsupportedError,
  MountReadyTimeoutError,
  RelayfileSetupError
} from "./setup-errors.js"
import type {
  MountLocalLayout,
  MountLauncher,
  CheckpointCapableMountLauncherInstance,
  MountLauncherInstance,
  MountLauncherStart,
  MountMode,
  MountSyncMode,
  CheckpointAndSealInput,
  MountedWorkspaceStatus,
  ReadMountedWorkspaceStatusInput
} from "./setup-types.js"
import type { CheckpointSeal } from "./types.js"

const DEFAULT_READY_POLL_INTERVAL_MS = 250
const DEFAULT_STOP_TIMEOUT_MS = 10_000
const LOG_ROTATION_MAX_BYTES = 10 * 1024 * 1024
const LOG_ROTATION_FILES = 3
const DEFAULT_CHECKPOINT_TIMEOUT_MS = 30_000
const MAX_CHECKPOINT_OUTPUT_BYTES = 1024 * 1024
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
  const effectiveEnv = {
    ...process.env,
    ...input.env
  }
  assertExactMountLayout(effectiveEnv)
  if ((effectiveEnv.RELAYFILE_MOUNT_PATHS_FILE ?? "").trim() !== "") {
    throw new MountMultiPathUnsupportedError()
  }
  const localDir = path.resolve(effectiveEnv.RELAYFILE_LOCAL_DIR ?? process.cwd())
  const mountLocalDir = resolveMountLocalDir(
    localDir,
    effectiveEnv.RELAYFILE_REMOTE_PATH,
    effectiveEnv.RELAYFILE_MOUNT_LOCAL_LAYOUT
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
    env: effectiveEnv,
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
    command,
    effectiveEnv,
    cwd: input.cwd ?? mountLocalDir,
    spawnImpl: options.spawnImpl ?? spawn,
    now: options.now ?? Date.now,
    readyPollIntervalMs:
      options.readyPollIntervalMs ?? DEFAULT_READY_POLL_INTERVAL_MS
  })
}

class RelayfileMountProcessInstance
  implements CheckpointCapableMountLauncherInstance {
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
  private readonly command: string
  private readonly effectiveEnv: NodeJS.ProcessEnv
  private readonly cwd: string
  private readonly spawnImpl: typeof spawn

  private exited = false
  private stopping?: Promise<void>
  private readyResolved = false
  private checkpointPromise?: Promise<CheckpointSeal>

  get stopped(): boolean {
    return this.exited
  }

  constructor(input: {
    child: ChildProcess
    logStream: NodeJS.WritableStream
    pidPath: string
    outputBuffer: string[]
    input: MountLauncherStart
    localDir: string
    command: string
    effectiveEnv: NodeJS.ProcessEnv
    cwd: string
    spawnImpl: typeof spawn
    now: () => number
    readyPollIntervalMs: number
  }) {
    this.child = input.child
    this.logStream = input.logStream
    this.pidPath = input.pidPath
    this.outputBuffer = input.outputBuffer
    this.input = input.input
    this.localDir = input.localDir
    this.command = input.command
    this.effectiveEnv = input.effectiveEnv
    this.cwd = input.cwd
    this.spawnImpl = input.spawnImpl
    this.pid = input.child.pid ?? undefined
    this.now = input.now
    this.readyPollIntervalMs = input.readyPollIntervalMs

    this.child.once("exit", () => {
      this.exited = true
    })

    this.ready = this.waitForReady()
  }

  async status(): Promise<MountedWorkspaceStatus> {
    const status = await readMountedWorkspaceStatus({
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
    return this.exited ? { ...status, ready: false } : status
  }

  async stop(): Promise<void> {
    if (!this.stopping) {
      this.stopping = this.performStop()
    }
    await this.stopping
  }

  async checkpointAndSeal(input: CheckpointAndSealInput): Promise<CheckpointSeal> {
    this.validateCheckpointPreconditions(input)
    if (!this.checkpointPromise) {
      this.checkpointPromise = this.performCheckpointAndSeal(input)
    }
    return this.checkpointPromise
  }

  private validateCheckpointPreconditions(input: CheckpointAndSealInput): void {
    validateCheckpointInput(input)
    if (normalizeMountMode(this.effectiveEnv.RELAYFILE_MOUNT_MODE) !== "poll") {
      throw new RelayfileSetupError(
        "checkpointAndSeal currently requires a poll-mode mount; a FUSE mount must remain running until daemon checkpoint IPC is available.",
        "checkpoint_seal_mode_unavailable"
      )
    }
    if (normalizeMountSyncMode(this.effectiveEnv.RELAYFILE_MOUNT_SYNC_MODE) === "pull-only") {
      throw new RelayfileSetupError(
        "checkpointAndSeal cannot drain a pull-only mount.",
        "checkpoint_seal_mode_unavailable"
      )
    }
    if (normalizeRemotePath(this.effectiveEnv.RELAYFILE_REMOTE_PATH ?? "/") !== "/") {
      throw new RelayfileSetupError(
        "checkpointAndSeal v1 requires a full-root (/) mount.",
        "checkpoint_seal_root_unavailable"
      )
    }
    const scopes = (this.effectiveEnv.RELAYFILE_MOUNT_SCOPES ?? "")
      .split(/\s+/)
      .map((scope) => scope.trim())
      .filter(Boolean)
    if (
      !scopeGrants(scopes, "sync", "trigger") ||
      !scopeGrants(scopes, "ops", "read") ||
      !scopeGrantsFullRoot(scopes, "read") ||
      !scopeGrantsFullRoot(scopes, "write")
    ) {
      throw new RelayfileSetupError(
        "checkpointAndSeal requires sync:trigger, ops:read, and full-root fs:read/fs:write scopes.",
        "checkpoint_seal_scope_unavailable"
      )
    }
  }

  private async performCheckpointAndSeal(input: CheckpointAndSealInput): Promise<CheckpointSeal> {
    await this.ready
    await this.stop()
    return runCheckpointSealProcess({
      command: this.command,
      cwd: this.cwd,
      env: this.effectiveEnv,
      spawnImpl: this.spawnImpl,
      input
    })
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

async function runCheckpointSealProcess(input: {
  command: string
  cwd: string
  env: NodeJS.ProcessEnv
  spawnImpl: typeof spawn
  input: CheckpointAndSealInput
}): Promise<CheckpointSeal> {
  const { sessionId, generation, timeoutMs, ttlSeconds } = validateCheckpointInput(input.input)
  const workspaceId = input.env.RELAYFILE_WORKSPACE?.trim() ?? ""
  const root = normalizeRemotePath(input.env.RELAYFILE_REMOTE_PATH)

  const args = [
    "--checkpoint-and-seal",
    "--checkpoint-session", sessionId,
    "--checkpoint-generation", String(generation),
    "--checkpoint-seal-ttl", `${ttlSeconds}s`,
    "--timeout", `${timeoutMs}ms`
  ]
  const child = input.spawnImpl(input.command, args, {
    cwd: input.cwd,
    env: input.env,
    stdio: ["ignore", "pipe", "pipe"]
  })

  return new Promise<CheckpointSeal>((resolve, reject) => {
    let stdout = ""
    let stderr = ""
    let settled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const finish = (error?: Error, seal?: CheckpointSeal): void => {
      if (settled) return
      settled = true
      if (timer) clearTimeout(timer)
      input.input.signal?.removeEventListener("abort", onAbort)
      if (error) reject(error)
      else resolve(seal!)
    }
    const append = (current: string, chunk: unknown): string => {
      const next = current + (Buffer.isBuffer(chunk) ? chunk.toString("utf8") : String(chunk))
      if (Buffer.byteLength(next) > MAX_CHECKPOINT_OUTPUT_BYTES) {
        child.kill("SIGKILL")
        finish(new RelayfileSetupError("relayfile-mount checkpoint output exceeded 1 MiB.", "checkpoint_seal_invalid_output"))
      }
      return next
    }
    child.stdout?.on("data", (chunk) => { stdout = append(stdout, chunk) })
    child.stderr?.on("data", (chunk) => { stderr = append(stderr, chunk) })
    child.once("error", (error) => {
      finish(new RelayfileSetupError(`relayfile-mount checkpoint failed to start: ${error.message}`, "checkpoint_seal_failed"))
    })
    child.once("exit", (code, signal) => {
      if (settled) return
      if (code !== 0) {
        const detail = stderr.trim().slice(-2_000)
        finish(new RelayfileSetupError(
          `relayfile-mount checkpoint failed (${signal ?? `exit ${code ?? "unknown"}`})${detail ? `: ${detail}` : ""}`,
          "checkpoint_seal_failed"
        ))
        return
      }
      let parsed: unknown
      try {
        parsed = JSON.parse(stdout.trim())
      } catch {
        finish(new RelayfileSetupError("relayfile-mount returned malformed checkpoint JSON.", "checkpoint_seal_invalid_output"))
        return
      }
      try {
        finish(
          undefined,
          validateCheckpointSeal(parsed, workspaceId, root, sessionId, generation)
        )
      } catch (error) {
        finish(error instanceof Error ? error : new Error(String(error)))
      }
    })
    const onAbort = (): void => {
      child.kill("SIGKILL")
      finish(new CloudAbortError("checkpointAndSeal"))
    }
    input.input.signal?.addEventListener("abort", onAbort, { once: true })
    timer = setTimeout(() => {
      child.kill("SIGKILL")
      finish(new RelayfileSetupError(`checkpointAndSeal timed out after ${timeoutMs}ms.`, "checkpoint_seal_timeout"))
    }, timeoutMs)
    timer.unref?.()
  })
}

function validateCheckpointInput(input: CheckpointAndSealInput): {
  sessionId: string
  generation: number
  timeoutMs: number
  ttlSeconds: number
} {
  const sessionId =
    typeof input.sessionId === "string" ? input.sessionId.trim() : ""
  const generation = input.generation
  const timeoutMs = input.timeoutMs ?? DEFAULT_CHECKPOINT_TIMEOUT_MS
  const ttlSeconds = input.ttlSeconds ?? 60
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(sessionId)) {
    throw new RelayfileSetupError(
      "checkpointAndSeal requires a valid sessionId.",
      "checkpoint_seal_invalid_input"
    )
  }
  if (
    !Number.isSafeInteger(generation) || generation <= 0 ||
    !Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 ||
    !Number.isSafeInteger(ttlSeconds) || ttlSeconds <= 0 || ttlSeconds > 300
  ) {
    throw new RelayfileSetupError(
      "checkpointAndSeal requires positive safe generation/timeout values and ttlSeconds <= 300.",
      "checkpoint_seal_invalid_input"
    )
  }
  if (input.signal?.aborted) {
    throw new CloudAbortError("checkpointAndSeal")
  }
  return { sessionId, generation, timeoutMs, ttlSeconds }
}

function scopeGrants(
  scopes: string[],
  resource: string,
  action: string
): boolean {
  const bare = `${resource}:${action}`
  return scopes.some((scope) => {
    if (scope === bare) return true
    const [plane, grantedResource, grantedAction] = scope.split(":", 4)
    return (
      (plane === "relayfile" || plane === "*") &&
      (grantedResource === resource || grantedResource === "*") &&
      (grantedAction === action || grantedAction === "*")
    )
  })
}

function scopeGrantsFullRoot(
  scopes: string[],
  action: "read" | "write"
): boolean {
  const relevantNarrowScopes: string[] = []
  for (const scope of scopes) {
    const segments = scope.split(":", 4)
    if (segments.length < 3) continue
    const [plane, resource, grantedAction, scopePath] = segments
    const actionMatches =
      grantedAction === action ||
      grantedAction === "*" ||
      grantedAction === "manage"
    if (!actionMatches) continue
    if (
      (plane === "relayfile" || plane === "*") &&
      (resource === "fs" || resource === "*")
    ) {
      if (scopePath === undefined || scopePath.trim() === "*") return true
      relevantNarrowScopes.push(scopePath.trim())
      continue
    }
    if (plane === "workspace") {
      if (scopePath === undefined || scopePath.trim() === "*") return true
      relevantNarrowScopes.push(scopePath.trim())
    }
  }
  if (relevantNarrowScopes.some((scopePath) => scopePath === "/" || scopePath === "/**")) {
    return true
  }
  return relevantNarrowScopes.length === 0 && scopes.includes(`fs:${action}`)
}

function validateCheckpointSeal(
  value: unknown,
  workspaceId: string,
  root: string,
  sessionId: string,
  generation: number
): CheckpointSeal {
  if (!value || typeof value !== "object") {
    throw new RelayfileSetupError("relayfile-mount returned an invalid checkpoint seal.", "checkpoint_seal_invalid_output")
  }
  const seal = value as Partial<CheckpointSeal>
  const strings = [seal.sealId, seal.sealToken, seal.workspaceId, seal.root, seal.digest, seal.workspaceRevision, seal.eventCursor, seal.issuedAt, seal.expiresAt]
  if (
    strings.some((field) => typeof field !== "string") ||
    seal.sealToken!.trim() === "" ||
    seal.workspaceId !== workspaceId ||
    normalizeRemotePath(seal.root) !== root ||
    seal.sessionId !== sessionId ||
    seal.generation !== generation ||
    !seal.digest!.startsWith("sha256:") ||
    Number.isNaN(Date.parse(seal.issuedAt!)) ||
    Number.isNaN(Date.parse(seal.expiresAt!))
  ) {
    throw new RelayfileSetupError("relayfile-mount returned an unbound or incomplete checkpoint seal.", "checkpoint_seal_invalid_output")
  }
  return seal as CheckpointSeal
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
  return mode === "pull-only"
    ? "pull-only"
    : mode === "write-only"
      ? "write-only"
      : "mirror"
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
