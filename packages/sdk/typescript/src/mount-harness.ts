import { mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { RelayFileApiError } from "./errors.js";
import { RelayFileClient } from "./client.js";

const DEFAULT_POLL_INTERVAL_MS = 1_000;

export interface RelayfileMountHarnessConfig {
  baseUrl: string;
  token: string;
  workspaceId: string;
  remotePath: string;
  localDir: string;
  mode: "poll" | "fuse";
}

export interface RelayfileMountHarnessOptions {
  env?: NodeJS.ProcessEnv | Record<string, string>;
  pollIntervalMs?: number;
  scopes?: string[];
  onEvent?: (event: RelayfileMountHarnessEvent) => void;
  client?: RelayFileClient;
}

export type RelayfileMountHarnessEvent =
  | {
      type: "started";
      localDir: string;
      remotePath: string;
      workspaceId: string;
    }
  | {
      type: "sync.completed";
      localDir: string;
      remotePath: string;
      files: number;
      directories: number;
    }
  | {
      type: "permission.denied";
      path: string;
      scopes: string[];
    }
  | {
      type: "stopped";
      localDir: string;
      remotePath: string;
    };

export interface RelayfileMountHarnessHandle {
  readonly config: RelayfileMountHarnessConfig;
  readonly localDir: string;
  syncOnce(): Promise<void>;
  writeLocalFile(relativePath: string, content: string): Promise<void>;
  stop(): Promise<void>;
}

export class MountHarnessPermissionError extends Error {
  readonly code = "permission_denied";
  readonly path: string;
  readonly scopes: string[];

  constructor(targetPath: string, scopes: string[]) {
    super(`Write denied for ${targetPath}: invited agent is read-only in the harness.`);
    this.name = "MountHarnessPermissionError";
    this.path = targetPath;
    this.scopes = [...scopes];
  }
}

export function readMountHarnessConfig(
  env: NodeJS.ProcessEnv | Record<string, string> = process.env
): RelayfileMountHarnessConfig {
  const baseUrl = readRequiredEnv(env, "RELAYFILE_BASE_URL");
  const token = readRequiredEnv(env, "RELAYFILE_TOKEN");
  const workspaceId = readRequiredEnv(env, "RELAYFILE_WORKSPACE");
  const localDir = readRequiredEnv(env, "RELAYFILE_LOCAL_DIR");
  const remotePath = normalizeRemotePath(env.RELAYFILE_REMOTE_PATH ?? "/");
  const mode = env.RELAYFILE_MOUNT_MODE === "fuse" ? "fuse" : "poll";

  return {
    baseUrl,
    token,
    workspaceId,
    remotePath,
    localDir: path.resolve(localDir),
    mode,
  };
}

export async function startMountHarness(
  options: RelayfileMountHarnessOptions = {}
): Promise<RelayfileMountHarnessHandle> {
  const harness = new RelayfileMountHarness(options);
  await harness.start();
  return harness;
}

class RelayfileMountHarness implements RelayfileMountHarnessHandle {
  readonly config: RelayfileMountHarnessConfig;
  readonly localDir: string;

  private readonly client: RelayFileClient;
  private readonly pollIntervalMs: number;
  private readonly scopes: string[];
  private readonly onEvent?: (event: RelayfileMountHarnessEvent) => void;
  private timer: NodeJS.Timeout | undefined;
  private stopped = false;
  private syncInFlight: Promise<void> | undefined;

  constructor(options: RelayfileMountHarnessOptions) {
    this.config = readMountHarnessConfig(options.env);
    this.localDir = this.config.localDir;
    this.pollIntervalMs = normalizePollIntervalMs(options.pollIntervalMs);
    this.scopes = [...(options.scopes ?? [])];
    this.onEvent = options.onEvent;
    this.client =
      options.client ??
      new RelayFileClient({
        baseUrl: this.config.baseUrl,
        token: this.config.token,
      });
  }

  async start(): Promise<void> {
    await mkdir(this.localDir, { recursive: true });
    this.emit({
      type: "started",
      localDir: this.localDir,
      remotePath: this.config.remotePath,
      workspaceId: this.config.workspaceId,
    });
    await this.syncOnce();
    this.scheduleNextSync();
  }

  async syncOnce(): Promise<void> {
    if (this.stopped) {
      return;
    }
    if (!this.syncInFlight) {
      this.syncInFlight = this.performSyncOnce().finally(() => {
        this.syncInFlight = undefined;
      });
    }
    await this.syncInFlight;
  }

  async writeLocalFile(relativePath: string, content: string): Promise<void> {
    const normalizedRelativePath = normalizeRelativePath(relativePath);
    const scopedTargetPath = joinRemotePath(this.config.remotePath, normalizedRelativePath);
    if (!this.scopes.includes("fs:write")) {
      const error = new MountHarnessPermissionError(scopedTargetPath, this.scopes);
      this.emit({
        type: "permission.denied",
        path: scopedTargetPath,
        scopes: [...this.scopes],
      });
      throw error;
    }

    const localPath = path.join(this.localDir, normalizedRelativePath);
    await mkdir(path.dirname(localPath), { recursive: true });
    await writeFile(localPath, content, "utf8");
  }

  async stop(): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.stopped = true;
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    if (this.syncInFlight) {
      await this.syncInFlight;
    }
    this.emit({
      type: "stopped",
      localDir: this.localDir,
      remotePath: this.config.remotePath,
    });
  }

  private async performSyncOnce(): Promise<void> {
    await mkdir(this.localDir, { recursive: true });

    const desiredFiles = new Map<string, string>();
    const desiredDirectories = new Set<string>([""]);

    await this.collectRemoteTree(
      this.config.remotePath,
      desiredFiles,
      desiredDirectories
    );

    for (const relativeDir of [...desiredDirectories].sort((left, right) => left.localeCompare(right))) {
      if (relativeDir === "") {
        continue;
      }
      await mkdir(path.join(this.localDir, relativeDir), { recursive: true });
    }

    for (const [relativePath, content] of [...desiredFiles.entries()].sort(([left], [right]) => left.localeCompare(right))) {
      const localPath = path.join(this.localDir, relativePath);
      await mkdir(path.dirname(localPath), { recursive: true });
      const currentContent = await readUtf8IfExists(localPath);
      if (currentContent !== content) {
        await writeFile(localPath, content, "utf8");
      }
    }

    await pruneLocalEntries(this.localDir, desiredFiles, desiredDirectories);

    this.emit({
      type: "sync.completed",
      localDir: this.localDir,
      remotePath: this.config.remotePath,
      files: desiredFiles.size,
      directories: desiredDirectories.size,
    });
  }

  private async collectRemoteTree(
    remotePath: string,
    desiredFiles: Map<string, string>,
    desiredDirectories: Set<string>
  ): Promise<void> {
    const tree = await this.client.listTree(this.config.workspaceId, {
      path: remotePath,
      depth: 1,
    });

    for (const entry of tree.entries) {
      const relativePath = relativeRemotePath(this.config.remotePath, entry.path);
      if (relativePath === "") {
        continue;
      }

      if (entry.type === "dir") {
        desiredDirectories.add(relativePath);
        await this.collectRemoteTree(entry.path, desiredFiles, desiredDirectories);
        continue;
      }

      desiredDirectories.add(path.posix.dirname(relativePath) === "." ? "" : path.posix.dirname(relativePath));
      const file = await this.client.readFile(this.config.workspaceId, entry.path);
      desiredFiles.set(relativePath, decodeContent(file.content, file.encoding));
    }
  }

  private scheduleNextSync(): void {
    if (this.stopped) {
      return;
    }
    this.timer = setTimeout(() => {
      void this.syncOnce()
        .catch((error) => {
          console.error(`mount-harness sync failed: ${formatErrorMessage(error)}`);
        })
        .finally(() => {
          this.scheduleNextSync();
        });
    }, this.pollIntervalMs);
  }

  private emit(event: RelayfileMountHarnessEvent): void {
    this.onEvent?.(event);
  }
}

async function pruneLocalEntries(
  rootDir: string,
  desiredFiles: Map<string, string>,
  desiredDirectories: Set<string>
): Promise<void> {
  if (!(await exists(rootDir))) {
    return;
  }

  const entries = await walkLocalTree(rootDir);
  for (const entry of entries.sort((left, right) => right.relativePath.localeCompare(left.relativePath))) {
    if (entry.relativePath === "") {
      continue;
    }
    const absolutePath = path.join(rootDir, entry.relativePath);
    if (entry.kind === "file") {
      if (!desiredFiles.has(entry.relativePath)) {
        await rm(absolutePath, { force: true });
      }
      continue;
    }
    if (!desiredDirectories.has(entry.relativePath)) {
      await rm(absolutePath, { recursive: true, force: true });
    }
  }
}

async function walkLocalTree(
  rootDir: string,
  currentDir = rootDir,
  entries: Array<{ relativePath: string; kind: "file" | "dir" }> = []
): Promise<Array<{ relativePath: string; kind: "file" | "dir" }>> {
  const children = await readdir(currentDir, { withFileTypes: true });
  for (const child of children) {
    const absolutePath = path.join(currentDir, child.name);
    const relativePath = normalizeRelativePath(path.relative(rootDir, absolutePath));
    if (child.isDirectory()) {
      entries.push({ relativePath, kind: "dir" });
      await walkLocalTree(rootDir, absolutePath, entries);
      continue;
    }
    entries.push({ relativePath, kind: "file" });
  }
  return entries;
}

async function readUtf8IfExists(filePath: string): Promise<string | undefined> {
  try {
    return await readFile(filePath, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return undefined;
    }
    throw error;
  }
}

async function exists(targetPath: string): Promise<boolean> {
  try {
    await stat(targetPath);
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return false;
    }
    throw error;
  }
}

function decodeContent(content: string, encoding?: "utf-8" | "base64"): string {
  if (encoding === "base64") {
    return Buffer.from(content, "base64").toString("utf8");
  }
  return content;
}

function joinRemotePath(rootPath: string, relativePath: string): string {
  return normalizeRemotePath(
    `${normalizeRemotePath(rootPath).replace(/\/$/, "")}/${relativePath}`.replace(/\/{2,}/g, "/")
  );
}

function relativeRemotePath(rootPath: string, targetPath: string): string {
  const normalizedRoot = normalizeRemotePath(rootPath);
  const normalizedTarget = normalizeRemotePath(targetPath);
  if (normalizedRoot === normalizedTarget) {
    return "";
  }
  if (normalizedRoot === "/") {
    return normalizeRelativePath(normalizedTarget.slice(1));
  }
  if (!normalizedTarget.startsWith(`${normalizedRoot}/`)) {
    throw new Error(`Remote path ${normalizedTarget} is outside root ${normalizedRoot}.`);
  }
  return normalizeRelativePath(normalizedTarget.slice(normalizedRoot.length + 1));
}

function normalizeRemotePath(input: string): string {
  const trimmed = input.trim();
  const normalized = path.posix.normalize(trimmed === "" ? "/" : trimmed);
  return normalized.startsWith("/") ? normalized : `/${normalized}`;
}

function normalizeRelativePath(input: string): string {
  const normalized = path.posix.normalize(input.replace(/\\/g, "/"));
  if (normalized === "." || normalized === "") {
    return "";
  }
  return normalized.replace(/^\/+/, "");
}

function normalizePollIntervalMs(value: number | undefined): number {
  if (value === undefined || !Number.isFinite(value) || value <= 0) {
    return DEFAULT_POLL_INTERVAL_MS;
  }
  return Math.floor(value);
}

function readRequiredEnv(
  env: NodeJS.ProcessEnv | Record<string, string>,
  key: string
): string {
  const value = env[key];
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`Missing required mount env: ${key}`);
  }
  return value.trim();
}

function formatErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

export async function runMountHarnessCli(
  argv: string[] = process.argv.slice(2),
  env: NodeJS.ProcessEnv | Record<string, string> = process.env
): Promise<void> {
  const options = parseCliArgs(argv);
  const harness = await startMountHarness({
    env,
    pollIntervalMs: options.pollIntervalMs,
    scopes: options.scopes,
    onEvent: (event) => {
      console.log(JSON.stringify(event));
    },
  });

  if (options.once) {
    await harness.stop();
    return;
  }

  const stopAndExit = async () => {
    await harness.stop();
  };

  const signalHandler = () => {
    void stopAndExit()
      .then(() => {
        process.exitCode = 0;
      })
      .finally(() => {
        process.removeListener("SIGINT", signalHandler);
        process.removeListener("SIGTERM", signalHandler);
      });
  };

  process.on("SIGINT", signalHandler);
  process.on("SIGTERM", signalHandler);

  await new Promise<void>((resolve) => {
    process.on("beforeExit", () => {
      resolve();
    });
  });
}

function parseCliArgs(argv: string[]): {
  once: boolean;
  pollIntervalMs?: number;
  scopes?: string[];
} {
  const parsed: {
    once: boolean;
    pollIntervalMs?: number;
    scopes?: string[];
  } = {
    once: false,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--once") {
      parsed.once = true;
      continue;
    }
    if (arg === "--poll-interval-ms") {
      const rawValue = argv[index + 1];
      if (!rawValue) {
        throw new Error("Missing value for --poll-interval-ms");
      }
      parsed.pollIntervalMs = Number.parseInt(rawValue, 10);
      index += 1;
      continue;
    }
    if (arg === "--scopes") {
      const rawValue = argv[index + 1];
      if (!rawValue) {
        throw new Error("Missing value for --scopes");
      }
      parsed.scopes = rawValue
        .split(",")
        .map((scope) => scope.trim())
        .filter(Boolean);
      index += 1;
      continue;
    }
    throw new Error(`Unknown argument: ${arg}`);
  }

  return parsed;
}

function isDirectExecution(): boolean {
  const entry = process.argv[1];
  if (!entry) {
    return false;
  }
  return import.meta.url === pathToFileURL(entry).href;
}

if (isDirectExecution()) {
  void runMountHarnessCli().catch((error) => {
    const status =
      error instanceof MountHarnessPermissionError
        ? {
            code: error.code,
            path: error.path,
            scopes: error.scopes,
          }
        : error instanceof RelayFileApiError
          ? {
              code: error.code,
              status: error.status,
              message: error.message,
            }
          : {
              message: formatErrorMessage(error),
            };
    console.error(JSON.stringify({ type: "error", ...status }));
    process.exitCode = 1;
  });
}
