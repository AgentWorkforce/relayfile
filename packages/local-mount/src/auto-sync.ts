import {
  chmodSync,
  constants as fsConstants,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
} from 'node:fs';
import type { Stats } from 'node:fs';
import path from 'node:path';
import watcher, { type AsyncSubscription } from '@parcel/watcher';

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
}

interface FileState {
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

  const state = new Map<string, FileState>();

  primeState(state, ctx);

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
    // Project deleted externally and mount hasn't been touched since → mirror.
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
  copyFileSync(mountAbs, target, fsConstants.COPYFILE_FICLONE);
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
  // The mount copy of a readonly file has mode 0o444, which blocks
  // copyFileSync from overwriting it. Temporarily restore write permission.
  if (existsSync(target)) {
    try { chmodSync(target, 0o644); } catch { /* best effort */ }
  }
  copyFileSync(projectAbs, target, fsConstants.COPYFILE_FICLONE);
  if (readonly) {
    try { chmodSync(target, 0o444); } catch { /* best effort */ }
  } else {
    const mode = safeFileStat(projectAbs)?.mode;
    if (mode !== undefined) {
      try { chmodSync(target, mode & 0o777); } catch { /* best effort */ }
    }
  }
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

function isSymlinkTarget(target: string): boolean {
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
    const a = statSync(left);
    const b = statSync(right);
    if (a.size !== b.size) return false;
    return readFileSync(left).equals(readFileSync(right));
  } catch {
    return false;
  }
}

function resolveSafeWriteTarget(root: string, candidate: string): string | null {
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
    mkdirSync(parent, { recursive: true });
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
