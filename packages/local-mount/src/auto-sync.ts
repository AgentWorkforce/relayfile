import {
  chmodSync,
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
import chokidar, { type FSWatcher } from 'chokidar';

export interface AutoSyncContext {
  realMountDir: string;
  realProjectDir: string;
  isExcluded: (relPosix: string) => boolean;
  /**
   * Directory-only ignore patterns (ending in `/`) must only match when the
   * path is a directory. Callers that know the path's type pass `isDirectory`;
   * callers that don't (chokidar's prune filter) should check both forms.
   */
  isIgnored: (relPosix: string, isDirectory?: boolean) => boolean;
  isReadonly: (relPosix: string) => boolean;
  isReservedFile: (relPosix: string) => boolean;
}

export interface AutoSyncOptions {
  /** Full-reconcile interval as a safety net. Default: 10_000ms. */
  scanIntervalMs?: number;
  /** chokidar awaitWriteFinish stabilityThreshold in ms. Default: 200. */
  writeFinishMs?: number;
  /** Invoked on errors during sync — logged by default consumer. */
  onError?: (err: Error) => void;
}

export interface AutoSyncHandle {
  stop(opts?: { signal?: AbortSignal }): Promise<void>;
  /** Force a reconcile now; returns number of files copied/deleted. */
  reconcile(opts?: { signal?: AbortSignal }): Promise<number>;
  /** Cumulative files changed (copied or deleted) since autosync started. */
  totalChanges(): number;
  /** Resolves once both watchers have completed their initial scan. */
  ready(): Promise<void>;
}

interface FileState {
  mountMtimeMs?: number;
  projectMtimeMs?: number;
}

export function startAutoSync(
  ctx: AutoSyncContext,
  opts: AutoSyncOptions = {}
): AutoSyncHandle {
  const scanIntervalMs = opts.scanIntervalMs ?? 10_000;
  const writeFinishMs = opts.writeFinishMs ?? 200;
  const onError = opts.onError ?? (() => { /* ignore by default */ });

  const state = new Map<string, FileState>();

  primeState(state, ctx);

  let syncing = false;
  let pending = false;
  let totalChanges = 0;

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
    try {
      count = reconcile(state, ctx, onError, signal);
    } catch (err) {
      onError(err as Error);
    } finally {
      syncing = false;
    }
    if (pending && !signal?.aborted) {
      pending = false;
      try {
        count += reconcile(state, ctx, onError, signal);
      } catch (err) {
        onError(err as Error);
      }
    }
    totalChanges += count;
    return count;
  };

  const syncPathFromRoot = (root: string, absPath: string): void => {
    const rel = path.relative(root, absPath);
    if (rel === '' || rel.startsWith('..')) return;
    const relPosix = rel.split(path.sep).join('/');
    if (!isSyncCandidate(relPosix, ctx)) return;
    try {
      const changed = syncOneFile(relPosix, state, ctx);
      if (changed) totalChanges += 1;
    } catch (err) {
      onError(err as Error);
    }
  };

  const makeWatcher = (root: string): { watcher: FSWatcher; ready: Promise<void> } => {
    const watcher = chokidar.watch(root, {
      ignoreInitial: true,
      persistent: true,
      followSymlinks: false,
      awaitWriteFinish: {
        stabilityThreshold: writeFinishMs,
        pollInterval: 50,
      },
      ignored: (candidate: string, stats?: Stats) =>
        shouldChokidarIgnore(candidate, root, ctx, stats),
    });
    const onEvent = (p: string) => syncPathFromRoot(root, p);
    watcher.on('add', onEvent);
    watcher.on('change', onEvent);
    watcher.on('unlink', onEvent);
    watcher.on('error', (err) => onError(err as Error));
    const ready = new Promise<void>((resolve) => {
      watcher.once('ready', () => resolve());
    });
    return { watcher, ready };
  };

  const mount = makeWatcher(ctx.realMountDir);
  const project = makeWatcher(ctx.realProjectDir);
  const mountWatcher = mount.watcher;
  const projectWatcher = project.watcher;
  const watchersReady = Promise.all([mount.ready, project.ready]);

  const interval = setInterval(() => {
    void runReconcile();
  }, scanIntervalMs);
  // Do not keep the event loop alive just because of our scan timer.
  interval.unref?.();

  return {
    async stop(opts?: { signal?: AbortSignal }) {
      clearInterval(interval);
      await Promise.all([mountWatcher.close(), projectWatcher.close()]);
      if (opts?.signal?.aborted) {
        return;
      }
      // Drain any pending work so callers can rely on "stopped means quiesced".
      await runReconcile(opts);
    },
    reconcile: runReconcile,
    totalChanges: () => totalChanges,
    ready: async () => {
      await watchersReady;
    },
  };
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
 *   for readonly files; then drop the mount change).
 * - Only project changed → project→mount.
 * - One side missing:
 *   • Other side changed since last sync → recreate the missing side.
 *   • Otherwise → propagate the delete.
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
      if (readonly) {
        // Readonly can't accept mount-side writes; fall back to project→mount.
        return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
      }
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    if (mountStat && !projectStat) {
      if (readonly) {
        // New file in mount with a readonly pattern → cannot sync back.
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
    if (mountChanged && !readonly) {
      return doMountToProject(relPosix, state, ctx, mountAbs, projectAbs);
    }
    if (projectChanged) {
      return doProjectToMount(relPosix, state, ctx, projectAbs, mountAbs, readonly);
    }
    return false;
  }

  if (mountStat && !projectStat) {
    if (mountChanged && !readonly) {
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
    if (readonly) {
      // Readonly deletes in mount don't sync back; recreate mount from project.
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
  copyFileSync(mountAbs, target);
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
  copyFileSync(projectAbs, target);
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

function shouldChokidarIgnore(
  candidate: string,
  root: string,
  ctx: AutoSyncContext,
  stats?: Stats
): boolean {
  if (candidate === root) return false;
  const rel = path.relative(root, candidate);
  if (rel === '' || rel.startsWith('..')) return false;
  const relPosix = rel.split(path.sep).join('/');
  if (ctx.isExcluded(relPosix)) return true;
  if (ctx.isReservedFile(relPosix)) return true;
  // chokidar calls this filter twice: first without stats (pre-stat prune),
  // then again with stats once it knows the entry type. Only apply the
  // directory-form match when we have stats confirming it's a directory,
  // otherwise a directory-only pattern like `cache/` would wrongly prune a
  // same-named file.
  if (stats) {
    return ctx.isIgnored(relPosix, stats.isDirectory());
  }
  return ctx.isIgnored(relPosix);
}
