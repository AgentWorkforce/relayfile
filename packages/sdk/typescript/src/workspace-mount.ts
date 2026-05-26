import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  accessSync,
  chmodSync,
  constants,
  createWriteStream,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import https from 'node:https';
import os from 'node:os';
import path from 'node:path';

const RELAYFILE_VERSION = '0.1.6';
const RELEASE_BASE_URL = 'https://github.com/AgentWorkforce/relayfile/releases/download';
const CHECKSUMS_FILE = 'checksums.txt';
const CACHE_DIR = path.join(os.homedir(), '.agent-relay', 'bin');
const CACHE_PATH = path.join(CACHE_DIR, 'relayfile-mount');
const VERSION_PATH = path.join(CACHE_DIR, 'relayfile-mount.version');
const SUPPORTED_TARGETS = ['darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64'].join(', ');

const PLATFORM_ARCH_MAP: Record<string, string> = {
  'darwin:arm64': 'darwin-arm64',
  'darwin:x64': 'darwin-amd64',
  'linux:arm64': 'linux-arm64',
  'linux:x64': 'linux-amd64',
};

export interface MountConfig {
  binaryPath?: string;
  relayfileUrl: string;
  workspace: string;
  token: string;
  mountPoint?: string;
}

export interface MountHandle {
  pid: number;
  mountPoint: string;
  stop(): Promise<void>;
}

function ensureCacheDir(): void {
  mkdirSync(CACHE_DIR, { recursive: true });
}

function getRelayfileTarget(): string {
  const target = PLATFORM_ARCH_MAP[`${os.platform()}:${os.arch()}`];
  if (!target) {
    throw new Error(
      `Unsupported platform for relayfile-mount: ${os.platform()}-${os.arch()}. Supported targets: ${SUPPORTED_TARGETS}.`
    );
  }

  return target;
}

function getReleaseAssetUrl(assetName: string): string {
  return `${RELEASE_BASE_URL}/v${RELAYFILE_VERSION}/${assetName}`;
}

function readCachedVersion(): string | null {
  try {
    return readFileSync(VERSION_PATH, 'utf8').trim() || null;
  } catch {
    return null;
  }
}

function isExecutable(filePath: string): boolean {
  try {
    accessSync(filePath, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function downloadErrorMessage(url: string, status: number): string {
  return `Download failed with status ${status} for ${url}`;
}

function downloadBinary(url: string, destPath: string, maxRedirects = 5): Promise<void> {
  ensureCacheDir();

  const attemptDownload = (
    currentUrl: string,
    redirectsRemaining: number,
    resolve: () => void,
    reject: (error: Error) => void
  ) => {
    const request = https.get(currentUrl, (res) => {
      const status = res.statusCode ?? 0;
      const location = res.headers.location;
      const isRedirect = status >= 300 && status < 400 && location;

      if (isRedirect) {
        if (redirectsRemaining <= 0) {
          res.resume();
          reject(new Error('Too many redirects while downloading relayfile-mount'));
          return;
        }

        const nextUrl = new URL(location, currentUrl).toString();
        res.resume();
        attemptDownload(nextUrl, redirectsRemaining - 1, resolve, reject);
        return;
      }

      if (status !== 200) {
        res.resume();
        reject(new Error(downloadErrorMessage(currentUrl, status)));
        return;
      }

      const fileStream = createWriteStream(destPath, { mode: 0o755 });
      res.pipe(fileStream);
      fileStream.on('finish', () => {
        fileStream.close(() => resolve());
      });
      fileStream.on('error', (error) => reject(error instanceof Error ? error : new Error(String(error))));
      res.on('error', (error) => reject(error instanceof Error ? error : new Error(String(error))));
    });

    request.on('error', (error) => reject(error instanceof Error ? error : new Error(String(error))));
  };

  return new Promise<void>((resolve, reject) => {
    attemptDownload(url, maxRedirects, resolve, reject);
  }).catch((error: unknown) => {
    try {
      rmSync(destPath, { force: true });
    } catch {
      // Ignore cleanup failures.
    }

    throw error;
  });
}

function downloadText(url: string, maxRedirects = 5): Promise<string> {
  const fetchWithRedirects = (
    currentUrl: string,
    redirectsRemaining: number,
    resolve: (text: string) => void,
    reject: (error: Error) => void
  ) => {
    const request = https.get(currentUrl, (res) => {
      const status = res.statusCode ?? 0;
      const location = res.headers.location;
      const isRedirect = status >= 300 && status < 400 && location;

      if (isRedirect) {
        if (redirectsRemaining <= 0) {
          res.resume();
          reject(new Error('Too many redirects while downloading relayfile checksums'));
          return;
        }

        const nextUrl = new URL(location, currentUrl).toString();
        res.resume();
        fetchWithRedirects(nextUrl, redirectsRemaining - 1, resolve, reject);
        return;
      }

      if (status !== 200) {
        res.resume();
        reject(new Error(downloadErrorMessage(currentUrl, status)));
        return;
      }

      const chunks: Buffer[] = [];
      res.on('data', (chunk) => {
        chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
      });
      res.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
      res.on('error', (error) => reject(error instanceof Error ? error : new Error(String(error))));
    });

    request.on('error', (error) => reject(error instanceof Error ? error : new Error(String(error))));
  };

  return new Promise((resolve, reject) => {
    fetchWithRedirects(url, maxRedirects, resolve, reject);
  });
}

function getExpectedChecksum(checksumContent: string, binaryName: string): string {
  for (const line of checksumContent.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) {
      continue;
    }

    const match = trimmed.match(/^([a-fA-F0-9]{64})\s+\*?(.+)$/);
    if (!match) {
      continue;
    }

    const entryName = path.basename(match[2].trim());
    if (entryName === binaryName) {
      return match[1].toLowerCase();
    }
  }

  throw new Error(`No checksum entry found for ${binaryName}`);
}

async function verifyChecksum(filePath: string, binaryName: string): Promise<void> {
  const checksumUrl = getReleaseAssetUrl(CHECKSUMS_FILE);
  const checksumContent = await downloadText(checksumUrl);
  const expectedHash = getExpectedChecksum(checksumContent, binaryName);
  const actualHash = createHash('sha256').update(readFileSync(filePath)).digest('hex');

  if (actualHash !== expectedHash) {
    throw new Error(`Checksum mismatch for ${binaryName}: expected ${expectedHash}, got ${actualHash}`);
  }
}

function resignBinaryForMacOS(binaryPath: string): void {
  if (os.platform() !== 'darwin') {
    return;
  }

  try {
    // Pass the binary path as a separate argv entry so shell metacharacters
    // in the cache path cannot break or hijack the codesign invocation.
    execFileSync('codesign', ['--force', '--sign', '-', binaryPath], { stdio: 'pipe' });
  } catch {
    // Ignore best-effort re-sign failures.
  }
}

async function ensureRelayfileMountBinary(binaryPath?: string): Promise<string> {
  if (binaryPath) {
    return binaryPath;
  }

  if (process.env.RELAYFILE_ROOT) {
    return path.join(process.env.RELAYFILE_ROOT, 'bin', 'relayfile-mount');
  }

  const target = getRelayfileTarget();
  const binaryName = `relayfile-mount-${target}`;
  const downloadUrl = getReleaseAssetUrl(binaryName);

  ensureCacheDir();

  if (existsSync(CACHE_PATH) && readCachedVersion() === RELAYFILE_VERSION) {
    if (!isExecutable(CACHE_PATH)) {
      chmodSync(CACHE_PATH, 0o755);
    }
    return CACHE_PATH;
  }

  const tempPath = path.join(CACHE_DIR, `relayfile-mount.${process.pid}.${Date.now()}.download`);

  try {
    await downloadBinary(downloadUrl, tempPath);
    await verifyChecksum(tempPath, binaryName);
    chmodSync(tempPath, 0o755);
    renameSync(tempPath, CACHE_PATH);
    chmodSync(CACHE_PATH, 0o755);
    resignBinaryForMacOS(CACHE_PATH);
    writeFileSync(VERSION_PATH, `${RELAYFILE_VERSION}\n`, 'utf8');
    return CACHE_PATH;
  } catch (error) {
    try {
      rmSync(tempPath, { force: true });
    } catch {
      // Ignore cleanup failures.
    }

    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`Failed to install relayfile-mount from ${downloadUrl}: ${message}`);
  }
}

const DEFAULT_COMMAND_TIMEOUT_MS = 5 * 60 * 1000;

async function runCommandCapture(
  command: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  timeoutMs: number = DEFAULT_COMMAND_TIMEOUT_MS
): Promise<string> {
  return await new Promise((resolve, reject) => {
    const proc = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'], env });
    let output = '';
    let settled = false;

    const settle = (fn: () => void): void => {
      if (settled) {
        return;
      }
      settled = true;
      clearTimeout(timer);
      fn();
    };

    const timer = setTimeout(() => {
      // Best-effort terminate the stalled child so we don't leak the process.
      try {
        proc.kill('SIGTERM');
      } catch {
        // Ignore kill failures; the timeout error below is what matters.
      }
      settle(() => reject(new Error(`command timed out after ${timeoutMs}ms`)));
    }, timeoutMs);

    proc.stdout.setEncoding('utf8');
    proc.stderr.setEncoding('utf8');

    proc.stdout.on('data', (chunk: string) => {
      output += chunk;
    });
    proc.stderr.on('data', (chunk: string) => {
      output += chunk;
    });

    proc.on('error', (error) => {
      settle(() => reject(error));
    });

    proc.on('close', (code, signal) => {
      if (code === 0) {
        settle(() => resolve(output));
        return;
      }

      const reason = signal ? `signal ${signal}` : `exit code ${typeof code === 'number' ? code : 'unknown'}`;
      const detail = output.trim();
      settle(() => reject(new Error(detail || `command failed with ${reason}`)));
    });
  });
}

function ensureProcessRunning(processRef: ChildProcess): boolean {
  return processRef.exitCode === null && !processRef.killed;
}

async function stopMountProcess(processRef: ChildProcess): Promise<void> {
  if (processRef.exitCode !== null || !processRef.pid) {
    return;
  }

  processRef.kill('SIGTERM');
  await new Promise<void>((resolve) => {
    const timeout = setTimeout(() => {
      if (processRef.exitCode === null && processRef.pid) {
        processRef.kill('SIGKILL');
      }
      resolve();
    }, 1200);
    processRef.once('exit', () => {
      clearTimeout(timeout);
      resolve();
    });
  });
}

export async function ensureRelayfileMount(config: MountConfig): Promise<MountHandle> {
  const binaryPath = await ensureRelayfileMountBinary(config.binaryPath);
  if (!existsSync(binaryPath)) {
    throw new Error(`missing relayfile mount binary: ${binaryPath}`);
  }

  // Track whether we created the mountPoint ourselves. We must only `rm` the
  // directory on shutdown/failure when this function owns it — never when a
  // caller passed their own path, since that could destroy unrelated user data.
  let mountPoint: string;
  let ownsMountPoint: boolean;
  if (config.mountPoint) {
    mountPoint = config.mountPoint;
    ownsMountPoint = false;
    mkdirSync(mountPoint, { recursive: true });
  } else {
    mountPoint = await mkdtemp(path.join(os.tmpdir(), `relayfile-mount-${config.workspace}-`));
    ownsMountPoint = true;
  }

  const cleanupMountPoint = async (): Promise<void> => {
    if (!ownsMountPoint) {
      return;
    }
    await rm(mountPoint, { recursive: true, force: true }).catch(() => undefined);
  };

  const mountBaseArgs = [
    '--base-url',
    config.relayfileUrl,
    '--workspace',
    config.workspace,
    '--local-dir',
    mountPoint,
  ];
  const onceArgs = [...mountBaseArgs, '--once'];
  const mountEnv = {
    ...process.env,
    RELAYFILE_TOKEN: config.token,
  };

  let mountProc: ChildProcess | undefined;
  let startupPhase = 'initial workspace sync';
  try {
    await runCommandCapture(binaryPath, onceArgs, mountEnv);

    startupPhase = 'mount process startup';
    const startedMountProc = spawn(binaryPath, mountBaseArgs, {
      stdio: ['ignore', 'ignore', 'ignore'],
      env: mountEnv,
    });
    mountProc = startedMountProc;

    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => resolve(), 600);
      startedMountProc.on('error', (spawnError) => {
        clearTimeout(timer);
        reject(spawnError);
      });
      startedMountProc.on('spawn', () => {
        clearTimeout(timer);
        resolve();
      });
    });

    if (!ensureProcessRunning(startedMountProc) || typeof startedMountProc.pid !== 'number') {
      await stopMountProcess(startedMountProc).catch(() => undefined);
      throw new Error(`mount process for workspace ${config.workspace} exited before continuing`);
    }
  } catch (error) {
    if (mountProc) {
      await stopMountProcess(mountProc).catch(() => undefined);
    }
    await cleanupMountPoint();
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`${startupPhase} failed for ${config.workspace}: ${message}`);
  }

  if (!mountProc || typeof mountProc.pid !== 'number') {
    await cleanupMountPoint();
    throw new Error(`mount process startup failed for ${config.workspace}: missing process id`);
  }

  let stopPromise: Promise<void> | undefined;
  const startedMountProc = mountProc;

  return {
    pid: mountProc.pid,
    mountPoint,
    async stop(): Promise<void> {
      if (!stopPromise) {
        // Memoize the in-flight shutdown so concurrent callers all await the
        // same termination + cleanup sequence instead of returning early
        // before stopMountProcess and cleanupMountPoint have settled.
        stopPromise = (async (): Promise<void> => {
          await stopMountProcess(startedMountProc).catch(() => undefined);
          await cleanupMountPoint();
        })();
      }
      await stopPromise;
    },
  };
}
