import {
  chmodSync,
  closeSync,
  constants as fsConstants,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  unlinkSync,
  renameSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
} from 'node:fs';
import type { Stats } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import watcher, { type AsyncSubscription } from '@parcel/watcher';
import { preserveMtime, statsImplySameContent } from './stat-compare.js';

export interface AutoSyncContext {
  realMountDir: string;
  realProjectDir: string;
  isExcluded: (relPosix: string) => boolean;
  /**
   * Normalized directory names that drive any-depth `isExcluded` matches.
   * Used purely to hint `@parcel/watcher` which subtrees to skip subscribing
   * to. The in-handler `isSyncCandidate` filter remains authoritative.
   */
  excludedAnyDepthNames: readonly string[];
  /**
   * Root-anchored excluded names/prefixes such as `build` or `packages/cache`.
   * These are matched only from the watch root to avoid hiding legitimate
   * nested source directories like `src/build`.
   */
  excludedRootPrefixes: readonly string[];
  /**
   * Directory-only ignore patterns (ending in `/`) must only match when the
   * path is a directory. Callers that know the path's type pass `isDirectory`;
   * callers that don't should omit the second argument and fall back to the
   * file-form check.
   */
  isIgnored: (relPosix: string, isDirectory?: boolean) => boolean;
  isReadonly: (relPosix: string) => boolean;
  /**
   * One-way project→mount paths. Project-side changes flow into the mount,
   * but mount-side changes never flow back. Unlike readonly, the mount copy
   * is left writable so tools (e.g. git) can mutate it locally; those
   * mutations are simply discarded on cleanup.
   */
  isNoSyncBack: (relPosix: string) => boolean;
  isReservedFile: (relPosix: string) => boolean;
  /**
   * True while the mount root still looks like a live mount (its marker
   * file exists). Checked before any deletion is mirrored across trees: a
   * mount directory that was torn down externally (crash cleanup, manual
   * rm) must read as "the mount is gone", never as "the agent deleted
   * every file" — without this, autosync would faithfully propagate the
   * teardown as a mass delete of the user's project.
   */
  mountRootIntact: () => boolean;
  /** Same guard for the project side: its disappearance must not empty the mount. */
  projectRootIntact: () => boolean;
  /**
   * Sync state seeded by the mount population loop: one entry per copied
   * file with both sides' mtimes recorded at copy time. When present,
   * `startAutoSync` clones it instead of running the full-tree
   * content-comparison priming pass — the copy already proved both sides
   * identical, so re-reading every file pair only rediscovers that.
   */
  initialState?: ReadonlyMap<string, FileState>;
}

export interface AutoSyncOptions {
  /**
   * Degraded-watcher full-reconcile interval as a safety net. Default: 10_000ms.
   * Set to 0 or Infinity to disable periodic full reconciles.
   */
  scanIntervalMs?: number;
  /**
   * Full-reconcile interval while both watcher subscriptions are healthy.
   * Default: 60_000ms, or `scanIntervalMs` when that option is explicitly set.
   * Set to 0 or Infinity to disable healthy-watcher full reconciles.
   */
  healthyScanIntervalMs?: number;
  /**
   * Per-path event debounce in ms. Rapid watcher events for the same path
   * are coalesced into a single sync. Default: 50.
   */
  debounceMs?: number;
  /** Invoked on errors during sync — logged by default consumer. */
  onError?: (err: Error) => void;
}

export interface AutoSyncHandle {
  stop(opts?: { signal?: AbortSignal }): Promise<void>;
  /** Drain currently debounced watcher events. Falls back to reconcile if watchers are degraded. */
  flushPending(opts?: { signal?: AbortSignal }): Promise<number>;
  /** Force a reconcile now; returns number of files copied/deleted. */
  reconcile(opts?: { signal?: AbortSignal }): Promise<number>;
  /** Mount-side paths that still need a final one-shot syncBack check. */
  getDirtyPaths(): IterableIterator<string>;
  /** True once both watchers subscribed and no watcher error has been observed. */
  watchersHealthy(): boolean;
  /** Cumulative files changed (copied or deleted) since autosync started. */
  totalChanges(): number;
  /** Resolves once both watchers have completed their initial scan. */
  ready(): Promise<void>;
  /**
   * Snapshot of the per-file sync state (both sides' last-synced mtimes),
   * keyed by posix-relative path. Persist it alongside a kept mount and feed
   * it to `attachMount` so the next session's first reconcile can
   * distinguish deletions from creations. Run a full `reconcile()` first if
   * the snapshot must cover paths only reconciles visit (e.g. `.git/**`
   * under `includeGit`).
   */
  exportState(): Record<string, FileState>;
}

export interface FileState {
  mountMtimeMs?: number;
  projectMtimeMs?: number;
}

const STOP_EVENT_SETTLE_MS = 250;
const DEFAULT_SCAN_INTERVAL_MS = 10_000;
const DEFAULT_HEALTHY_SCAN_INTERVAL_MS = 60_000;
const MAX_SCAN_INTERVAL_MS = 2_147_483_647;

function normalizeScanInterval(
  name: string,
  value: number | undefined,
  fallback: number
): number | null {
  const interval = value ?? fallback;
  if (interval === 0 || interval === Infinity) return null;
  if (!Number.isFinite(interval) || interval < 0 || interval > MAX_SCAN_INTERVAL_MS) {
    throw new RangeError(
      `${name} must be between 0 and ${MAX_SCAN_INTERVAL_MS}, or Infinity`
    );
  }
  return interval;
}

export function startAutoSync(
  ctx: AutoSyncContext,
  opts: AutoSyncOptions = {}
): AutoSyncHandle {
  const scanIntervalMs = normalizeScanInterval(
    'scanIntervalMs',
    opts.scanIntervalMs,
    DEFAULT_SCAN_INTERVAL_MS
  );
  const healthyScanIntervalMs = normalizeScanInterval(
    'healthyScanIntervalMs',
    opts.healthyScanIntervalMs,
    opts.scanIntervalMs === undefined ? DEFAULT_HEALTHY_SCAN_INTERVAL_MS : opts.scanIntervalMs
  );
  const debounceMs = opts.debounceMs ?? 50;
  const onError = opts.onError ?? (() => { /* ignore by default */ });

  // Population-seeded state skips the priming walk entirely. Entries are
  // cloned, not shared: the internal map mutates on every sync, and callers
  // hold (or persist) their snapshot under a readonly contract.
  const state = new Map<string, FileState>(
    Array.from(ctx.initialState ?? [], ([rel, fileState]) => [rel, { ...fileState }] as const)
  );

  if (!ctx.initialState) {
    primeState(state, ctx);
  }

  let syncing = false;
  let pending = false;
  let stopping = false;
  let stopped = false;
  let watchersReadySettled = false;
  let watcherDegraded = false;
  let totalChanges = 0;
  const pendingPaths = new Set<string>();
  const pendingDebounces = new Map<string, NodeJS.Timeout>();
  const dirtyMountPaths = new Set<string>();

  const watchersHealthy = (): boolean => watchersReadySettled && !watcherDegraded;

  const clearPendingDebounces = (): void => {
    for (const t of pendingDebounces.values()) clearTimeout(t);
    pendingDebounces.clear();
  };

  const syncPath = (relPosix: string): number => {
    if (!isSyncCandidate(relPosix, ctx)) {
      dirtyMountPaths.delete(relPosix);
      return 0;
    }
    try {
      const changed = syncOneFile(relPosix, state, ctx);
      dirtyMountPaths.delete(relPosix);
      return changed ? 1 : 0;
    } catch (err) {
      onError(err as Error);
      return 0;
    }
  };

  const flushPendingPaths = async (opts?: { signal?: AbortSignal }): Promise<number> => {
    const signal = opts?.signal;
    if (signal?.aborted) {
      return 0;
    }

    let count = 0;
    let processed = 0;
    for (const relPosix of Array.from(pendingPaths)) {
      if (signal?.aborted) {
        break;
      }
      pendingPaths.delete(relPosix);
      count += syncPath(relPosix);
      processed += 1;
      if (signal && processed % 64 === 0 && !signal.aborted) {
        await new Promise<void>((resolve) => setImmediate(resolve));
      }
    }

    totalChanges += count;
    return count;
  };

  const runReconcile = async (opts?: { signal?: AbortSignal }): Promise<number> => {
    const signal = opts?.signal;
    if (signal?.aborted) {
      return 0;
    }
    if (syncing) {
      pending = true;
      return 0;
    }
    syncing = true;
    let count = 0;
    let completed = false;
    try {
      count = reconcile(state, ctx, onError, signal);
      completed = !signal?.aborted;
    } catch (err) {
      onError(err as Error);
    } finally {
      syncing = false;
    }
    if (pending && !signal?.aborted) {
      pending = false;
      try {
        count += reconcile(state, ctx, onError, signal);
        completed = !signal?.aborted;
      } catch (err) {
        onError(err as Error);
        completed = false;
      }
    }
    if (completed) {
      pendingPaths.clear();
      dirtyMountPaths.clear();
    }
    totalChanges += count;
    return count;
  };

  let periodicTimer: NodeJS.Timeout | undefined;
  let periodicReconcileRunning = false;

  const clearPeriodicReconcile = (): void => {
    if (periodicTimer) {
      clearTimeout(periodicTimer);
      periodicTimer = undefined;
    }
  };

  const nextScanInterval = (): number | null => {
    return watchersHealthy() ? healthyScanIntervalMs : scanIntervalMs;
  };

  const schedulePeriodicReconcile = (): void => {
    clearPeriodicReconcile();
    if (stopping || stopped) return;

    const delay = nextScanInterval();
    if (delay === null) return;

    periodicTimer = setTimeout(() => {
      periodicTimer = undefined;
      periodicReconcileRunning = true;
      void runReconcile().finally(() => {
        periodicReconcileRunning = false;
        schedulePeriodicReconcile();
      });
    }, delay);
    periodicTimer.unref?.();
  };

  const reschedulePeriodicReconcile = (): void => {
    if (periodicReconcileRunning) return;
    schedulePeriodicReconcile();
  };

  const markWatcherDegraded = (err: Error): void => {
    const alreadyDegraded = watcherDegraded;
    watcherDegraded = true;
    if (!alreadyDegraded) {
      reschedulePeriodicReconcile();
    }
    onError(err);
  };

  const flushPending = async (opts?: { signal?: AbortSignal }): Promise<number> => {
    if (opts?.signal?.aborted) {
      return 0;
    }
    try {
      await watchersReady;
    } catch {
      // Subscription setup failure is already marked degraded and surfaced.
    }
    if (opts?.signal?.aborted) {
      return 0;
    }

    clearPendingDebounces();
    if (!watchersHealthy()) {
      return runReconcile(opts);
    }
    return flushPendingPaths(opts);
  };

  const schedulePathSync = (root: string, absPath: string): void => {
    if (stopped) return;
    const relPosix = root === ctx.realMountDir
      ? toRelPosix(absPath, ctx)
      : toRelPosixFromProject(absPath, ctx);
    if (relPosix === null || !isSyncCandidate(relPosix, ctx)) return;
    pendingPaths.add(relPosix);
    if (root === ctx.realMountDir) {
      dirtyMountPaths.add(relPosix);
    }
    // During stop(), keep accepting queued watcher events so the final flush
    // can process them, but don't create timers that could outlive teardown.
    if (stopping) return;
    // Coalesce bursts of events for the same path. The reconcile path
    // re-checks content via mtime+bytes, so a partial-write event that
    // races a later write is harmless.
    const existing = pendingDebounces.get(relPosix);
    if (existing) clearTimeout(existing);
    const t = setTimeout(() => {
      pendingDebounces.delete(relPosix);
      pendingPaths.delete(relPosix);
      totalChanges += syncPath(relPosix);
    }, debounceMs);
    pendingDebounces.set(relPosix, t);
  };

  const subscribeTo = (root: string): Promise<AsyncSubscription> =>
    watcher.subscribe(
      root,
      (err, events) => {
        if (err) { markWatcherDegraded(err); return; }
        for (const ev of events) {
          schedulePathSync(root, ev.path);
        }
      },
      { ignore: buildIgnoreGlobs(ctx, root) }
    );

  let mountSub: AsyncSubscription | undefined;
  let projectSub: AsyncSubscription | undefined;
  // Subscribe in parallel but track each outcome independently. With
  // Promise.all, a failure on one side would reject before the other's
  // assignment ran and leak the succeeded subscription. allSettled lets us
  // tear down whichever fulfilled before re-throwing the first failure.
  const watchersReady = (async () => {
    const [mountResult, projectResult] = await Promise.allSettled([
      subscribeTo(ctx.realMountDir),
      subscribeTo(ctx.realProjectDir),
    ]);
    if (mountResult.status === 'fulfilled') mountSub = mountResult.value;
    if (projectResult.status === 'fulfilled') projectSub = projectResult.value;
    if (mountResult.status === 'fulfilled' && projectResult.status === 'fulfilled') {
      watchersReadySettled = true;
      reschedulePeriodicReconcile();
      return;
    }
    watchersReadySettled = true;
    watcherDegraded = true;
    reschedulePeriodicReconcile();
    await Promise.allSettled([
      mountSub?.unsubscribe(),
      projectSub?.unsubscribe(),
    ]);
    mountSub = undefined;
    projectSub = undefined;
    throw mountResult.status === 'rejected'
      ? mountResult.reason
      : (projectResult as PromiseRejectedResult).reason;
  })();
  // If subscription setup fails, surface via onError rather than an unhandled
  // rejection. stop() still awaits the same promise and will observe the
  // rejection after the cleanup above has already run.
  watchersReady.catch((err) => markWatcherDegraded(err as Error));

  schedulePeriodicReconcile();

  return {
    async stop(opts?: { signal?: AbortSignal }) {
      try {
        await watchersReady;
      } catch {
        // Setup failed and already cleaned up any partial subscription;
        // mountSub / projectSub were reset to undefined before the throw.
      }
      if (!opts?.signal?.aborted && watchersHealthy()) {
        await new Promise<void>((resolve) => setTimeout(resolve, STOP_EVENT_SETTLE_MS));
      }
      stopping = true;
      clearPeriodicReconcile();
      clearPendingDebounces();
      await Promise.allSettled([
        mountSub?.unsubscribe(),
        projectSub?.unsubscribe(),
      ]);
      clearPendingDebounces();
      if (opts?.signal?.aborted) {
        stopped = true;
        stopping = false;
        return;
      }
      // Drain pending watcher work when the watcher state is trusted; otherwise
      // keep the historical full-reconcile safety net.
      if (watchersHealthy()) {
        await flushPendingPaths(opts);
      } else {
        await runReconcile(opts);
      }
      stopped = true;
      stopping = false;
    },
    flushPending,
    reconcile: runReconcile,
    getDirtyPaths: () => new Set(dirtyMountPaths).values(),
    watchersHealthy,
    totalChanges: () => totalChanges,
    ready: async () => {
      await watchersReady;
    },
    exportState: () =>
      Object.fromEntries(Array.from(state, ([rel, fileState]) => [rel, { ...fileState }])),
  };
}

function buildIgnoreGlobs(ctx: AutoSyncContext, watchRoot: string): string[] {
  // @parcel/watcher's wrapper splits each ignore entry by is-glob: globs are
  // compiled by picomatch and matched as regexes against absolute event paths;
  // non-globs are resolved as literal absolute paths. For each excluded entry
  // (library defaults + user-supplied excludeDirs) we emit shapes that mirror
  // `isExcludedPath`'s semantics, so a watcher-suppressed event never differs
  // from what the in-handler filter would have rejected.
  //
  //   - Any-depth names (e.g. `node_modules`) emit `**/<name>` plus
  //     `**/<name>/**`. picomatch turns both into depth-agnostic regexes
  //     that catch the dir and its descendants.
  //   - Root prefixes (e.g. `build` or `build/cache`) are root-anchored
  //     in `isExcludedPath` — they only match `<root>/build/cache`, NOT
  //     `<root>/src/build/cache`. Emit absolute patterns rooted at the
  //     watch dir so the watcher hides the same set: a literal absolute
  //     path (which the wrapper routes to ignorePaths) plus an anchored
  //     descendant glob.
  const globs: string[] = [];
  for (const name of ctx.excludedAnyDepthNames) {
    globs.push(`**/${name}`, `**/${name}/**`);
  }
  for (const prefix of ctx.excludedRootPrefixes) {
    globs.push(`${watchRoot}/${prefix}`, `${watchRoot}/${prefix}/**`);
  }
  return globs;
}

function primeState(state: Map<string, FileState>, ctx: AutoSyncContext): void {
  // Record current mtimes for every file that exists in both trees with the
  // same content. Files that differ are left out so the first reconcile sees
  // no prev entry and picks a winner via the content-based resolution path.
  walk(ctx.realMountDir, ctx, (abs) => {
    const rel = toRelPosix(abs, ctx);
    if (rel === null) return;
    if (!isSyncCandidate(rel, ctx)) return;
    const mountStat = safeFileStat(abs);
    if (!mountStat) return;
    const projectAbs = path.join(ctx.realProjectDir, rel);
    const projectStat = safeFileStat(projectAbs);
    if (!projectStat) return;
    if (!sameContent(abs, projectAbs)) return;
    state.set(rel, {
      mountMtimeMs: mountStat.mtimeMs,
      projectMtimeMs: projectStat.mtimeMs,
    });
  });
}

function reconcile(
  state: Map<string, FileState>,
  ctx: AutoSyncContext,
  onError: (err: Error) => void,
  signal?: AbortSignal
): number {
  const seen = new Set<string>();
  let count = 0;

  const visit = (relPosix: string): void => {
    if (seen.has(relPosix)) return;
    seen.add(relPosix);
    if (!isSyncCandidate(relPosix, ctx)) return;
    try {
      const changed = syncOneFile(relPosix, state, ctx);
      if (changed) count += 1;
    } catch (err) {
      onError(err as Error);
    }
  };

  walk(ctx.realMountDir, ctx, (abs) => {
    if (signal?.aborted) return;
    const rel = toRelPosix(abs, ctx);
    if (rel !== null) visit(rel);
  }, signal);

  if (signal?.aborted) {
    return count;
  }

  walk(ctx.realProjectDir, ctx, (abs) => {
    if (signal?.aborted) return;
    const rel = toRelPosixFromProject(abs, ctx);
    if (rel !== null) visit(rel);
  }, signal);

  if (signal?.aborted) {
    return count;
  }

  // Tombstone sweep: any path in state we didn't visit had both sides absent,
  // so it's fully gone.
  for (const rel of Array.from(state.keys())) {
    if (!seen.has(rel)) {
      const mountAbs = path.join(ctx.realMountDir, rel);
      const projectAbs = path.join(ctx.realProjectDir, rel);
      if (!existsSync(mountAbs) && !existsSync(projectAbs)) {
        state.delete(rel);
      }
    }
  }

  return count;
}

/**
 * Sync a single relPath and return true if a copy or delete actually happened.
 *
 * Resolution rules ("mount wins"):
 * - If both sides changed since last sync → mount→project.
 * - Only mount changed → mount→project (unless mount-side change is disallowed
 *   for readonly / noSyncBack files; then drop the mount change).
 * - Only project changed → project→mount.
 * - One side missing:
 *   • Other side changed since last sync → recreate the missing side.
 *   • Otherwise → propagate the delete.
 *
 * `readonly` and `noSyncBack` both forbid mount→project. The split exists so
 * the chmod 0o444 only fires for true readonly entries (e.g. `.agentreadonly`
 * matches), while noSyncBack entries (e.g. `.git/**` when `includeGit: true`)
 * stay writable in the mount so tools can mutate them locally.
 */
function syncOneFile(
  relPosix: string,
  state: Map<string, FileState>,
  ctx: AutoSyncContext
): boolean {
  const mountAbs = path.join(ctx.realMountDir, relPosix);
  const projectAbs = path.join(ctx.realProjectDir, relPosix);

  const mountStat = safeFileStat(mountAbs);
  const projectStat = safeFileStat(projectAbs);

  const prev = state.get(relPosix);
  const readonly = ctx.isReadonly(relPosix);
  const noSyncBack = readonly || ctx.isNoSyncBack(relPosix);

  if (!mountStat && !projectStat) {
    state.delete(relPosix);
    return false;
  }

  if (!prev) {
    // First time we've seen this path.
    if (mountStat && projectStat) {
      if (sameContent(mountAbs, projectAbs)) {
        state.set(relPosix, {
          mountMtimeMs: mountStat.mtimeMs,
          projectMtimeMs: projectStat.mtimeMs,
        });
        return false;
      }
      // Differ with no history: arbitrary tiebreak → mount wins.
      if (noSyncBack) {
        // Mount-side writes never flow back; fall back to project→mount.
        return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
      }
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    if (mountStat && !projectStat) {
      if (noSyncBack) {
        // New file in mount with a no-sync-back pattern → cannot sync back.
        return false;
      }
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    if (!mountStat && projectStat) {
      return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
    }
  }

  // Use strict inequality rather than `>`: on filesystems with coarse mtime
  // resolution, or after a backdated touch, a real content change can land
  // with a non-greater mtime.
  const mountChanged = mountStat
    ? prev?.mountMtimeMs === undefined || mountStat.mtimeMs !== prev.mountMtimeMs
    : false;
  const projectChanged = projectStat
    ? prev?.projectMtimeMs === undefined || projectStat.mtimeMs !== prev.projectMtimeMs
    : false;

  if (mountStat && projectStat) {
    if (!mountChanged && !projectChanged) return false;
    if (mountChanged && !noSyncBack) {
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    if (projectChanged) {
      return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
    }
    return false;
  }

  if (mountStat && !projectStat) {
    if (mountChanged && !noSyncBack) {
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    // Project deleted externally and mount hasn't been touched since →
    // mirror — but only while the project root itself is still there. A
    // vanished project tree is a teardown, not a per-file delete.
    if (!ctx.projectRootIntact()) return false;
    return doDeleteMount(relPosix, state, mountAbs);
  }

  if (!mountStat && projectStat) {
    if (projectChanged) {
      return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
    }
    // Mount deleted and project hasn't been touched since → mirror to project.
    if (noSyncBack) {
      // No-sync-back deletes in mount don't propagate; recreate from project.
      return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
    }
    // Guard the catastrophic case: if the mount root itself is gone (torn
    // down externally while this autosync was still alive), every mount
    // file reads as "deleted" — propagating that would erase the project.
    if (!ctx.mountRootIntact()) return false;
    return doDeleteProject(relPosix, state, projectAbs);
  }

  return false;
}

function doMountToProject(
  relPosix: string,
  state: Map<string, FileState>,
  ctx: AutoSyncContext,
  mountAbs: string,
  projectAbs: string
): boolean {
  const target = resolveSafeWriteTarget(ctx.realProjectDir, projectAbs);
  if (!target) return false;
  if (isSymlinkTarget(target)) return false;
  if (existsSync(target) && sameContent(mountAbs, target)) {
    updateState(state, relPosix, mountAbs, target);
    return false;
  }
  if (!safeCopyOnto(mountAbs, target)) return false;
  const mountStat = safeFileStat(mountAbs);
  if (mountStat) preserveMtime(target, mountStat);
  updateState(state, relPosix, mountAbs, target);
  return true;
}

function doProjectToMount(
  relPosix: string,
  state: Map<string, FileState>,
  ctx: AutoSyncContext,
  projectAbs: string,
  mountAbs: string,
  readonly: boolean
): boolean {
  const target = resolveSafeWriteTarget(ctx.realMountDir, mountAbs);
  if (!target) return false;
  if (isSymlinkTarget(target)) return false;
  if (existsSync(target) && sameContent(projectAbs, target)) {
    updateState(state, relPosix, target, projectAbs);
    return false;
  }
  // The mode is applied to the temporary file before the rename, so a readonly
  // (0o444) mount copy no longer has to be chmod'd writable first. That
  // temporary un-protection was a small window in which the readonly guarantee
  // did not hold; renaming over the target removes the need for it entirely.
  const sourceStat = safeFileStat(projectAbs);
  const mode = readonly ? 0o444 : sourceStat?.mode !== undefined ? sourceStat.mode & 0o777 : undefined;

  if (!safeCopyOnto(projectAbs, target, mode)) return false;
  if (sourceStat) preserveMtime(target, sourceStat);
  updateState(state, relPosix, target, projectAbs);
  return true;
}

function doDeleteMount(
  relPosix: string,
  state: Map<string, FileState>,
  mountAbs: string
): boolean {
  try {
    rmSync(mountAbs, { force: true });
  } catch {
    return false;
  }
  state.delete(relPosix);
  return true;
}

function doDeleteProject(
  relPosix: string,
  state: Map<string, FileState>,
  projectAbs: string
): boolean {
  try {
    rmSync(projectAbs, { force: true });
  } catch {
    return false;
  }
  state.delete(relPosix);
  return true;
}

function updateState(
  state: Map<string, FileState>,
  relPosix: string,
  mountAbs: string,
  projectAbs: string
): void {
  const mountStat = safeFileStat(mountAbs);
  const projectStat = safeFileStat(projectAbs);
  state.set(relPosix, {
    mountMtimeMs: mountStat?.mtimeMs,
    projectMtimeMs: projectStat?.mtimeMs,
  });
}

function isSyncCandidate(relPosix: string, ctx: AutoSyncContext): boolean {
  if (!relPosix || relPosix.startsWith('..')) return false;
  if (ctx.isReservedFile(relPosix)) return false;
  if (ctx.isExcluded(relPosix)) return false;
  if (ctx.isIgnored(relPosix)) return false;
  return true;
}

function toRelPosix(absPath: string, ctx: AutoSyncContext): string | null {
  const rel = path.relative(ctx.realMountDir, absPath);
  if (rel === '' || rel.startsWith('..')) return null;
  return rel.split(path.sep).join('/');
}

function toRelPosixFromProject(absPath: string, ctx: AutoSyncContext): string | null {
  const rel = path.relative(ctx.realProjectDir, absPath);
  if (rel === '' || rel.startsWith('..')) return null;
  return rel.split(path.sep).join('/');
}

function safeFileStat(p: string): Stats | null {
  try {
    const s = lstatSync(p);
    if (s.isSymbolicLink()) return null;
    if (!s.isFile()) return null;
    return s;
  } catch {
    return null;
  }
}

/** @internal exported for the adversarial confinement suite. */
export function isSymlinkTarget(target: string): boolean {
  // If the target already exists as a symlink, writing through it would
  // follow the link and potentially escape the mount/project root. Refuse.
  try {
    return lstatSync(target).isSymbolicLink();
  } catch {
    return false;
  }
}

function sameContent(left: string, right: string): boolean {
  try {
    const leftStat = statSync(left);
    const rightStat = statSync(right);
    if (leftStat.size !== rightStat.size) return false;
    // Copies preserve source mtimes, so equal size plus (near-)equal mtime
    // means "same write" — skip re-reading both files.
    if (statsImplySameContent(leftStat, rightStat)) return true;
  } catch {
    return false;
  }
  return sameContentBytes(left, right);
}

function sameContentBytes(left: string, right: string): boolean {
  try {
    const a = statSync(left);
    const b = statSync(right);
    if (a.size !== b.size) return false;
    return readFileSync(left).equals(readFileSync(right));
  } catch {
    return false;
  }
}

/**
 * Exported for the adversarial confinement suite in
 * auto-sync-confinement.test.ts. Not part of the package's public API — the
 * test drives the real resolver rather than a copy of it, because a copy proves
 * nothing about this code.
 */
export function resolveSafeWriteTarget(root: string, candidate: string): string | null {
  // `root` must already be realpath'd — the only caller does this at
  // mount.ts:195. Passing an unresolved root will reject everything.
  const resolvedRoot = path.resolve(root);
  const resolvedCandidate = path.resolve(candidate);
  if (
    resolvedCandidate !== resolvedRoot &&
    !resolvedCandidate.startsWith(`${resolvedRoot}${path.sep}`)
  ) {
    return null;
  }
  const parent = path.dirname(resolvedCandidate);
  try {
    // Directories are created one component at a time, refusing to traverse a
    // symlink, and only after the component is known to be safe.
    //
    // `mkdirSync(parent, { recursive: true })` used to run BEFORE the resolved
    // parent was validated, so a symlinked component caused directories to be
    // created outside the root and only then was the write refused — a refusal
    // with a side effect. `recursive: true` also creates *through* a symlinked
    // component, which is the traversal it was supposed to prevent.
    if (!createDirectoriesWithin(resolvedRoot, parent)) {
      return null;
    }
    const realParent = realpathSync(parent);
    if (
      realParent !== resolvedRoot &&
      !realParent.startsWith(`${resolvedRoot}${path.sep}`)
    ) {
      return null;
    }
    return path.join(realParent, path.basename(resolvedCandidate));
  } catch {
    return null;
  }
}

/**
 * Create every missing component of `dir` beneath `root`, one at a time,
 * refusing to follow or create through a symlink. Returns false on the first
 * component that is not a real directory.
 */
function createDirectoriesWithin(root: string, dir: string): boolean {
  if (dir === root) return true;

  const relative = path.relative(root, dir);
  if (relative.startsWith('..') || path.isAbsolute(relative)) return false;

  // Each component is opened and held for the duration, and — where the
  // platform allows — the next component is resolved *relative to that
  // descriptor* rather than by recomputed path.
  //
  // Checking a component with `lstat` and then creating the next one by path is
  // a race: an already-accepted directory can be swapped for an
  // outside-directed symlink before the following segment is created, and
  // `mkdirSync` would then create directories outside the root. Resolving
  // through the held descriptor means a swap on disk cannot redirect the
  // create, because the descriptor still refers to the directory that was
  // validated.
  //
  // On Linux `/proc/self/fd/<fd>/name` gives that resolution from the kernel.
  // Elsewhere there is no equivalent without a native `openat`, so the walk
  // falls back to paths and the held descriptors serve a narrower purpose: they
  // pin each inode so it cannot be freed and recycled, which keeps the caller's
  // subsequent `realpath` containment check meaningful. The write is still
  // refused in that case — the residual is that a directory may have been
  // created outside the root before the refusal.
  const held: number[] = [];
  try {
    let parentFd = openSync(root, fsConstants.O_RDONLY | fsConstants.O_DIRECTORY);
    held.push(parentFd);
    let currentPath = root;

    for (const segment of relative.split(path.sep)) {
      if (!segment) continue;

      currentPath = path.join(currentPath, segment);
      const childPath = DESCRIPTOR_RELATIVE
        ? `/proc/self/fd/${parentFd}/${segment}`
        : currentPath;

      const info = lstatSync(childPath, { throwIfNoEntry: false });
      if (!info) {
        mkdirSync(childPath); // one component, never recursive
      } else if (info.isSymbolicLink() || !info.isDirectory()) {
        // Refused rather than followed.
        return false;
      }

      // O_NOFOLLOW so a symlink that appeared since the lstat cannot be opened;
      // O_DIRECTORY so anything no longer a directory cannot be either.
      parentFd = openSync(
        childPath,
        fsConstants.O_RDONLY | fsConstants.O_DIRECTORY | fsConstants.O_NOFOLLOW
      );
      held.push(parentFd);
    }
    return true;
  } catch {
    return false;
  } finally {
    for (const fd of held) {
      try { closeSync(fd); } catch { /* already closed */ }
    }
  }
}

/** True where `/proc/self/fd` provides kernel-side descriptor-relative resolution. */
const DESCRIPTOR_RELATIVE = process.platform === 'linux';

/**
 * Copy `source` onto `target` without ever writing *through* whatever `target`
 * currently names.
 *
 * The content is written to a temporary sibling inside the already-validated
 * parent directory and then renamed over the target. That is what makes this
 * safe, and it closes two confirmed escapes that a check-then-copy sequence
 * could not:
 *
 *   - **Hardlink.** A hardlink inside the root pointing at a file outside it is
 *     path-indistinguishable from a real file and `realpath` cannot resolve it,
 *     because a hardlink has no target. `copyFileSync` onto that name wrote
 *     straight through to the outside file. `rename` replaces the *directory
 *     entry* instead, so the linked file keeps its content.
 *
 *   - **TOCTOU.** `isSymlinkTarget(target)` followed by `copyFileSync(target)`
 *     is two path lookups, and a target swapped for a symlink in between was
 *     followed. `rename` does not follow a final symlink — it replaces it.
 *
 * It also makes the write atomic: a reader sees the old file or the new one,
 * never a partial or zero-length one, and an interrupted copy leaves the target
 * untouched. Reflink cloning is preserved, since the copy into the temporary
 * file still uses COPYFILE_FICLONE.
 */
export function safeCopyOnto(source: string, target: string, mode?: number): boolean {
  const dir = path.dirname(target);

  // The temporary name is RANDOM and SHORT, and the copy is EXCLUSIVE. Both
  // properties are load-bearing:
  //
  //   - Random + exclusive, because the agent controls the mount. A name
  //     derived from the target basename plus pid and a counter is predictable,
  //     and `copyFileSync` follows a destination symlink — so the agent could
  //     pre-create that exact path pointing at a file outside the mount and the
  //     copy would overwrite the victim *before* the safe rename ever ran. That
  //     is the same escape this function exists to close, reintroduced by the
  //     fix; COPYFILE_EXCL makes the create fail if anything is already there,
  //     symlink included, and randomness means there is nothing to pre-empt.
  //
  //   - Short and independent of the target basename, because a basename near
  //     the filesystem's per-component limit (NAME_MAX, typically 255) would
  //     make a derived temporary name too long. `copyFileSync` then fails
  //     ENAMETOOLONG, which this function reports as a refusal, and auto-sync
  //     silently stops updating that file in either direction.
  const temp = path.join(dir, `.rfsync-${randomBytes(9).toString('hex')}`);

  try {
    copyFileSync(source, temp, fsConstants.COPYFILE_FICLONE | fsConstants.COPYFILE_EXCL);
    if (mode !== undefined) {
      try { chmodSync(temp, mode); } catch { /* best effort */ }
    }
    renameSync(temp, target);
    return true;
  } catch {
    try { unlinkSync(temp); } catch { /* best effort */ }
    return false;
  }
}

function walk(
  root: string,
  ctx: AutoSyncContext,
  visit: (absPath: string) => void,
  signal?: AbortSignal
): void {
  const stack = [root];
  while (stack.length > 0) {
    if (signal?.aborted) return;
    const cur = stack.pop();
    if (!cur) continue;
    let entries;
    try {
      entries = readdirSync(cur, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (signal?.aborted) return;
      const abs = path.join(cur, entry.name);
      const rel = path.relative(root, abs).split(path.sep).join('/');
      if (!rel || rel.startsWith('..')) continue;
      if (ctx.isExcluded(rel) || ctx.isIgnored(rel, entry.isDirectory())) continue;
      if (entry.isDirectory()) {
        stack.push(abs);
      } else if (entry.isFile() || entry.isSymbolicLink()) {
        visit(abs);
      }
    }
  }
}
