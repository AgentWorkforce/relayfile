import { spawn } from 'node:child_process';
import { createSymlinkMount, type SymlinkMountHandle } from './symlink-mount.js';

export interface LaunchOnMountOptions {
  /** Binary name or absolute path to the CLI to spawn, e.g. 'claude'. */
  cli: string;
  /** The real project directory to mirror. */
  projectDir: string;
  /** Where to create the mount. Must differ from projectDir. */
  mountDir: string;
  /** Argv to pass to the CLI after its binary name. */
  args: string[];
  /** Glob-style ignore patterns (files excluded entirely from the mount). */
  ignoredPatterns?: string[];
  /** Glob-style readonly patterns (files copied with mode 0o444). */
  readonlyPatterns?: string[];
  /** Extra directory names to exclude from the mount on top of defaults. */
  excludeDirs?: string[];
  /** Extra env vars merged on top of `process.env`. */
  env?: NodeJS.ProcessEnv;
  /** Optional agent name, used in the _MOUNT_README.md "Agent:" line. */
  agentName?: string;
  /**
   * Invoked after the mount is created but before the CLI is spawned.
   * Useful for writing additional files into the mount (overrides, extra docs).
   */
  onBeforeLaunch?: (mountDir: string) => void | Promise<void>;
  /**
   * Invoked after sync-back completes, before cleanup. Receives the number of
   * files that were written back to the project directory.
   */
  onAfterSync?: (syncedFileCount: number) => void | Promise<void>;
}

export interface LaunchOnMountResult {
  exitCode: number;
}

/**
 * Create a mount of `projectDir` at `mountDir`, spawn `cli` with `args` using
 * the mount as its cwd, forward SIGINT/SIGTERM to the child, sync writable
 * changes back on exit, then clean up the mount. Resolves with the child's
 * exit code.
 */
export async function launchOnMount(opts: LaunchOnMountOptions): Promise<LaunchOnMountResult> {
  const handle: SymlinkMountHandle = createSymlinkMount(opts.projectDir, opts.mountDir, {
    ignoredPatterns: opts.ignoredPatterns ?? [],
    readonlyPatterns: opts.readonlyPatterns ?? [],
    excludeDirs: opts.excludeDirs ?? [],
    agentName: opts.agentName,
  });

  let syncedCount = 0;
  let finalized = false;

  const finalize = async (): Promise<void> => {
    if (finalized) return;
    finalized = true;
    try {
      syncedCount = await handle.syncBack();
      if (opts.onAfterSync) {
        await opts.onAfterSync(syncedCount);
      }
    } finally {
      handle.cleanup();
    }
  };

  try {
    if (opts.onBeforeLaunch) {
      await opts.onBeforeLaunch(handle.mountDir);
    }

    const envVars: NodeJS.ProcessEnv = {
      ...process.env,
      ...(opts.env ?? {}),
    };

    const exitCode = await new Promise<number>((resolve, reject) => {
      const child = spawn(opts.cli, opts.args, {
        cwd: handle.mountDir,
        stdio: 'inherit',
        env: envVars,
      });

      let resolvedExit = 0;
      let cleanupInProgress: Promise<void> | undefined;

      const forwardAndCleanup = (signal: NodeJS.Signals) => {
        if (!child.killed && child.exitCode === null) {
          try {
            child.kill(signal);
          } catch {
            // ignore; we'll rely on 'close' handler
          }
        }
        if (!cleanupInProgress) {
          cleanupInProgress = new Promise<void>((r) => {
            if (child.exitCode !== null) {
              r();
              return;
            }
            const t = setTimeout(r, 2000);
            child.once('close', () => {
              clearTimeout(t);
              r();
            });
          });
        }
      };

      const onSigint = () => forwardAndCleanup('SIGINT');
      const onSigterm = () => forwardAndCleanup('SIGTERM');

      process.once('SIGINT', onSigint);
      process.once('SIGTERM', onSigterm);

      child.on('error', (err) => {
        process.removeListener('SIGINT', onSigint);
        process.removeListener('SIGTERM', onSigterm);
        reject(err);
      });

      child.on('close', (code, signal) => {
        process.removeListener('SIGINT', onSigint);
        process.removeListener('SIGTERM', onSigterm);
        if (typeof code === 'number') {
          resolvedExit = code;
        } else if (signal === 'SIGINT') {
          resolvedExit = 130;
        } else if (signal === 'SIGTERM') {
          resolvedExit = 143;
        } else if (typeof signal === 'string') {
          // Conventional mapping: 128 + signal number. We don't have the
          // numeric signal handy, so fall back to 1 for unknown signals.
          resolvedExit = 1;
        } else {
          resolvedExit = 1;
        }
        if (cleanupInProgress) {
          cleanupInProgress.then(() => resolve(resolvedExit));
        } else {
          resolve(resolvedExit);
        }
      });
    });

    await finalize();
    return { exitCode };
  } catch (err) {
    await finalize().catch(() => {
      // Already errored; swallow cleanup failure so the original error surfaces.
    });
    throw err;
  }
}
