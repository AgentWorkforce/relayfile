import { spawn } from 'node:child_process';
import { createMount, type MountHandle } from './mount.js';
import type { AutoSyncHandle, AutoSyncOptions } from './auto-sync.js';

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
  /**
   * Include the project's `.git` directory inside the mount with one-way
   * project→mount sync. Defaults to false. See {@link MountOptions.includeGit}
   * for details.
   */
  includeGit?: boolean;
  /** Extra env vars merged on top of `process.env`. */
  env?: NodeJS.ProcessEnv;
  /** Optional agent name, used in the _MOUNT_README.md "Agent:" line. */
  agentName?: string;
  /**
   * Optional signal used during shutdown work after the child exits.
   * It is passed to both autosync shutdown and the final mount→project sync-back.
   * If aborted, autosync stop may skip its draining reconcile and sync-back
   * returns the partial count accumulated so far, so fewer changes may be
   * propagated than during an uninterrupted shutdown.
   */
  shutdownSignal?: AbortSignal;
  /**
   * Invoked after the mount is created but before the CLI is spawned.
   * Useful for writing additional files into the mount (overrides, extra docs).
   */
  onBeforeLaunch?: (mountDir: string) => void | Promise<void>;
  /**
   * Invoked after sync-back completes, before cleanup. Receives the total
   * number of file changes propagated during the run — the sum of autosync
   * activity in both directions (including deletes) and the final mount→
   * project syncBack. Use this as an "anything changed?" signal rather than
   * a strict mount→project count.
   */
  onAfterSync?: (syncedFileCount: number) => void | Promise<void>;
  /**
   * Auto-sync behavior. By default, bidirectional auto-sync runs during the
   * lifetime of the spawned CLI. Pass `false` to disable, or an options object
   * to tune the scan interval / write-finish debounce.
   */
  autoSync?: boolean | AutoSyncOptions;
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
  const handle: MountHandle = createMount(opts.projectDir, opts.mountDir, {
    ignoredPatterns: opts.ignoredPatterns ?? [],
    readonlyPatterns: opts.readonlyPatterns ?? [],
    excludeDirs: opts.excludeDirs ?? [],
    agentName: opts.agentName,
    includeGit: opts.includeGit,
  });

  let syncedCount = 0;
  let finalized = false;
  let autoSync: AutoSyncHandle | undefined;

  const finalize = async (): Promise<void> => {
    if (finalized) return;
    finalized = true;
    try {
      let autoSyncChanges = 0;
      if (autoSync) {
        await autoSync.stop({ signal: opts.shutdownSignal });
        autoSyncChanges = autoSync.totalChanges();
        autoSync = undefined;
      }
      const finalSynced = await handle.syncBack({ signal: opts.shutdownSignal });
      syncedCount = autoSyncChanges + finalSynced;
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

    if (opts.autoSync !== false) {
      const autoSyncOpts = typeof opts.autoSync === 'object' ? opts.autoSync : undefined;
      autoSync = handle.startAutoSync(autoSyncOpts);
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
