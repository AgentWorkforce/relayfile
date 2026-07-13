import {
  chmodSync,
  constants as fsConstants,
  copyFileSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import type { Stats } from 'node:fs';
import { spawnSync } from 'node:child_process';
import ignore, { type Ignore } from 'ignore';
import os from 'node:os';
import path from 'node:path';
import {
  startAutoSync,
  type AutoSyncContext,
  type AutoSyncHandle,
  type AutoSyncOptions,
  type FileState,
} from './auto-sync.js';
import { preserveMtime, statsImplySameContent } from './stat-compare.js';

export interface MountOptions {
  ignoredPatterns: string[];
  readonlyPatterns: string[];
  excludeDirs: string[];
  /**
   * Optional agent name used in the _MOUNT_README.md "Agent:" line.
   * If omitted, the doc uses a generic "agent" value.
   */
  agentName?: string;
  /**
   * Include the project's `.git` directory inside the mount with one-way
   * project→mount sync. Default: false (`.git` is excluded entirely, matching
   * historical behavior).
   *
   * When true:
   * - `.git` is copied into the mount on creation, so git commands work inside.
   * - Project-side changes under `.git/**` flow into the mount.
   * - Mount-side changes under `.git/**` do NOT flow back to the project, so
   *   commits/branches the agent creates inside the mount stay sandboxed and
   *   are discarded with the mount on cleanup. Push to a remote to keep them.
   */
  includeGit?: boolean;
  /**
   * Include the built-in list of large cache/build output directories in
   * the mount exclusion set. Default: true. The `.git` directory remains
   * excluded unless `includeGit` is true, even when this is false.
   */
  includeDefaultExcludeDirs?: boolean;
  /**
   * How the initial mount population enumerates project files.
   *
   * - `'walk'` (default): recursive directory walk honoring the exclude and
   *   ignore rules. Copies every non-excluded file it encounters, including
   *   gitignored build outputs and caches the default excludes don't cover.
   * - `'git'`: enumerate via `git ls-files --cached --others
   *   --exclude-standard` — exactly the tracked plus untracked-unignored
   *   set, so gitignored trees (nested caches, worktrees, build outputs at
   *   any depth) never enter the mount. Exclude and ignore rules still apply
   *   on top. Throws if the project is not a usable git checkout.
   * - `'auto'`: `'git'` when the project has a `.git`, no `.gitmodules`, and
   *   `git ls-files` succeeds; silently falls back to `'walk'` otherwise.
   *
   * With `includeGit: true`, git-list population copies `.git` as one
   * timestamp-preserving bulk clone (copy-on-write where the filesystem
   * supports it) instead of walking it file-by-file. When any ignored or
   * readonly pattern targets `.git` itself, population falls back to
   * `'walk'` so those patterns keep applying inside `.git`.
   */
  population?: 'walk' | 'git' | 'auto';
}

export interface MountHandle {
  mountDir: string;
  initialFileCount?: number;
  initialMountDurationMs?: number;
  /**
   * Which population strategy actually ran (after `'auto'` resolution), or
   * `'reattach'` for handles from {@link attachMount}.
   */
  population: 'git' | 'walk' | 'reattach';
  syncBack(opts?: { signal?: AbortSignal; paths?: Iterable<string> }): Promise<number>;
  /**
   * Start bidirectional auto-sync: watches both the mount and project trees
   * via @parcel/watcher and runs periodic full reconciles as a safety net,
   * with a slower cadence while watchers are healthy. Returns a handle you
   * must `stop()` before teardown.
   */
  startAutoSync(opts?: AutoSyncOptions): AutoSyncHandle;
  cleanup(): void;
}

interface ExcludeRules {
  anyDepthNames: Set<string>;
  rootPrefixes: Set<string>;
}

const DEFAULT_ANY_DEPTH_EXCLUDES = [
  '.git',
  'node_modules',
  '.npm-cache',
  '__pycache__',
  '.pytest_cache',
  '.mypy_cache',
  '.ruff_cache',
  '.gradle',
  '.nyc_output',
  '.turbo',
  '.cache',
  '.DS_Store',
  // Virtualenvs are never mount material and routinely nest below the root
  // (e.g. packages/*/py/.venv). They also self-ignore via an internal
  // `.gitignore` that root-level rules never see, so without an any-depth
  // exclude the sync layers would happily mirror thousands of interpreter
  // files.
  '.venv',
  'venv',
];

const DEFAULT_ROOT_EXCLUDES = [
  'target',
  '.next',
  'dist',
  'build',
  'out',
  'env',
  'coverage',
];
const MOUNT_README_FILENAME = '_MOUNT_README.md';
const MOUNT_MARKER_FILENAME = '.relayfile-local-mount';
const MOUNT_MARKER_CONTENT =
  'This directory is managed by @relayfile/local-mount. Do not place unrelated files here; the directory will be deleted when the mount is torn down.\n';

export async function createMount(
  projectDir: string,
  mountDir: string,
  options: MountOptions
): Promise<MountHandle> {
  const resolvedProjectDir = realpathSync(projectDir);
  const resolvedMountDir = path.resolve(mountDir);
  const readonlyPatterns = [...options.readonlyPatterns];
  const ignoredPatterns = [...options.ignoredPatterns];
  const includeGit = options.includeGit === true;
  const readonlyMatcher = createPathMatcher(readonlyPatterns);
  const includeDefaultExcludeDirs = options.includeDefaultExcludeDirs !== false;
  // `.git` is in the default any-depth excludes so the mount stays small and git
  // operations don't accidentally cross-mutate the host repo. When the caller
  // opts in via `includeGit`, drop it from the defaults and instead route it
  // through the noSyncBack matcher below so it stays one-way.
  const excludeRules = createExcludeRules(options.excludeDirs, includeGit, includeDefaultExcludeDirs);
  const noSyncBackPatterns = includeGit ? ['.git', '.git/**'] : [];
  const noSyncBackMatcher = createPathMatcher(noSyncBackPatterns);

  const requestedPopulation = options.population ?? 'walk';
  const gitPopulation =
    requestedPopulation !== 'walk'
      ? prepareGitPopulation(resolvedProjectDir, ignoredPatterns, readonlyPatterns, includeGit)
      : null;
  if (requestedPopulation === 'git' && gitPopulation === null) {
    throw new Error(
      `population: 'git' requires a plain git checkout at ${resolvedProjectDir} ` +
        "(no submodules, no pattern negations, no ignored/readonly patterns matching '.git')"
    );
  }
  const population: 'git' | 'walk' = gitPopulation === null ? 'walk' : 'git';
  // Git-list population must keep the *sync* layers in agreement with what it
  // mounted: gitignored files never enter the mount, so reconcile/syncBack
  // must treat them as ignored too or the first full reconcile would copy
  // every gitignored tree into the mount after all.
  const isIgnored = buildIgnoredPredicate(createPathMatcher(ignoredPatterns), gitPopulation);

  // Guard against mountDir === projectDir. We compare both the realpath'd
  // project dir and the plain resolved project dir so callers that pass the
  // same argument for both are caught even when the path is a symlink (e.g.
  // /tmp → /private/tmp on macOS). The mountDir itself may not yet exist,
  // so we cannot realpath it.
  if (
    resolvedMountDir === resolvedProjectDir ||
    resolvedMountDir === path.resolve(projectDir)
  ) {
    throw new Error('mountDir must be different from projectDir');
  }

  assertMountDirSafeToRemove(resolvedMountDir, resolvedProjectDir);
  removeMountDir(resolvedMountDir);
  mkdirSync(resolvedMountDir, { recursive: true });
  const realMountDir = realpathSync(resolvedMountDir);
  writeFileSync(path.join(realMountDir, MOUNT_MARKER_FILENAME), MOUNT_MARKER_CONTENT, 'utf8');

  const initialMountStartedAt = Date.now();
  // Sync state seeded during population: every copy records both sides'
  // mtimes, so autosync can skip its full-tree content-comparison priming
  // pass — the copy loop already proved the two sides identical.
  const initialState = new Map<string, FileState>();

  let initialFileCount: number;
  if (gitPopulation !== null) {
    initialFileCount = await populateFromGitFileList(
      resolvedProjectDir,
      realMountDir,
      gitPopulation.files,
      excludeRules,
      readonlyMatcher,
      isIgnored,
      initialState
    );
    if (includeGit) {
      initialFileCount += cloneGitInto(resolvedProjectDir, realMountDir);
      // The clone bypasses the per-file copy loop, so seed its state
      // explicitly: without entries, project-side .git deletions (pack-refs,
      // gc) would never propagate, and pre-autosync mount-side .git setup
      // writes would be clobbered by the first reconcile's no-history path.
      seedClonedGitState(resolvedProjectDir, realMountDir, initialState);
    }
  } else {
    initialFileCount = await walkProjectTree(
      resolvedProjectDir,
      resolvedProjectDir,
      realMountDir,
      realMountDir,
      excludeRules,
      readonlyMatcher,
      isIgnored,
      initialState
    );
  }
  const initialMountDurationMs = Date.now() - initialMountStartedAt;

  const readmePath = resolveSafeCopyTarget(realMountDir, path.join(realMountDir, MOUNT_README_FILENAME));
  if (!readmePath) {
    throw new Error('Failed to create mount readme inside mountDir');
  }

  writeFileSync(
    readmePath,
    buildMountReadme(options.agentName, readonlyPatterns, ignoredPatterns),
    'utf8'
  );

  return buildMountHandle({
    resolvedProjectDir,
    resolvedMountDir,
    realMountDir,
    excludeRules,
    readonlyMatcher,
    isIgnored,
    noSyncBackMatcher,
    initialState,
    initialFileCount,
    initialMountDurationMs,
    population,
  });
}

/**
 * Reattach to a mount directory a previous `createMount` populated (and a
 * previous session left behind) without wiping or re-copying anything.
 *
 * The caller owns correctness of the reuse: pass the same patterns the mount
 * was created with, and pass `initialState` from a prior
 * `AutoSyncHandle.exportState()` so the first reconcile can tell "unchanged
 * since last session" from "changed on one side" — without it, files deleted
 * from the project while the mount sat idle would be treated as new
 * mount-side creations and resurrected. Refuses directories that don't carry
 * the mount marker.
 */
export async function attachMount(
  projectDir: string,
  mountDir: string,
  options: MountOptions & { initialState?: Record<string, FileState> }
): Promise<MountHandle> {
  const resolvedProjectDir = realpathSync(projectDir);
  const resolvedMountDir = path.resolve(mountDir);
  const readonlyPatterns = [...options.readonlyPatterns];
  const ignoredPatterns = [...options.ignoredPatterns];
  const includeGit = options.includeGit === true;
  const readonlyMatcher = createPathMatcher(readonlyPatterns);
  // A reattached mount must ignore exactly what its population did: when the
  // caller requests git-mode semantics, re-derive the same gitignore-aware
  // predicate (the guards re-run too, so a repo that has since grown
  // submodules degrades to plain caller patterns — matching what a fresh
  // createMount would do).
  const gitPopulation =
    (options.population ?? 'walk') !== 'walk'
      ? prepareGitPopulation(resolvedProjectDir, ignoredPatterns, readonlyPatterns, includeGit)
      : null;
  const isIgnored = buildIgnoredPredicate(createPathMatcher(ignoredPatterns), gitPopulation);
  const includeDefaultExcludeDirs = options.includeDefaultExcludeDirs !== false;
  const excludeRules = createExcludeRules(options.excludeDirs, includeGit, includeDefaultExcludeDirs);
  const noSyncBackPatterns = includeGit ? ['.git', '.git/**'] : [];
  const noSyncBackMatcher = createPathMatcher(noSyncBackPatterns);

  if (
    resolvedMountDir === resolvedProjectDir ||
    resolvedMountDir === path.resolve(projectDir)
  ) {
    throw new Error('mountDir must be different from projectDir');
  }
  if ((options.population ?? 'walk') === 'git' && gitPopulation === null) {
    throw new Error(
      `population: 'git' requires a plain git checkout at ${resolvedProjectDir} ` +
        "(no submodules, no pattern negations, no ignored/readonly patterns matching '.git')"
    );
  }
  const markerPath = path.join(resolvedMountDir, MOUNT_MARKER_FILENAME);
  if (!existsSync(markerPath)) {
    throw new Error(
      `attachMount: ${resolvedMountDir} is missing the ${MOUNT_MARKER_FILENAME} marker; ` +
        'only directories previously populated by createMount can be reattached.'
    );
  }
  const realMountDir = realpathSync(resolvedMountDir);
  // Same root/overlap validation as createMount: cleanup() recursively
  // removes the mount dir, so a marked directory that overlaps the project
  // must never be attachable. Unlike createMount, the mount dir exists here
  // (marker just verified), so both sides compare as realpaths — otherwise
  // symlinked temp roots (macOS /var → /private/var) would defeat the check.
  assertMountDirSafeToRemove(realMountDir, resolvedProjectDir);

  const initialState = new Map<string, FileState>(
    Object.entries(options.initialState ?? {})
  );

  return buildMountHandle({
    resolvedProjectDir,
    resolvedMountDir,
    realMountDir,
    excludeRules,
    readonlyMatcher,
    isIgnored,
    noSyncBackMatcher,
    initialState,
    population: 'reattach',
  });
}

function buildMountHandle(input: {
  resolvedProjectDir: string;
  resolvedMountDir: string;
  realMountDir: string;
  excludeRules: ExcludeRules;
  readonlyMatcher: Ignore;
  isIgnored: IgnoredPredicate;
  noSyncBackMatcher: Ignore;
  initialState: Map<string, FileState>;
  initialFileCount?: number;
  initialMountDurationMs?: number;
  population: 'git' | 'walk' | 'reattach';
}): MountHandle {
  const {
    resolvedProjectDir,
    resolvedMountDir,
    realMountDir,
    excludeRules,
    readonlyMatcher,
    isIgnored,
    noSyncBackMatcher,
    initialState,
  } = input;

  const autoSyncContext: AutoSyncContext = {
    realMountDir,
    realProjectDir: resolvedProjectDir,
    isExcluded: (relPosix) => isExcludedPath(relPosix, excludeRules),
    excludedAnyDepthNames: [...excludeRules.anyDepthNames],
    excludedRootPrefixes: [...excludeRules.rootPrefixes],
    isIgnored,
    isReadonly: (relPosix) => isPathMatched(relPosix, readonlyMatcher),
    isNoSyncBack: (relPosix) => isPathMatched(relPosix, noSyncBackMatcher),
    isReservedFile: (relPosix) =>
      relPosix === MOUNT_README_FILENAME || relPosix === MOUNT_MARKER_FILENAME,
    mountRootIntact: () =>
      existsSync(path.join(realMountDir, MOUNT_MARKER_FILENAME)),
    projectRootIntact: () => existsSync(resolvedProjectDir),
    initialState,
  };

  return {
    mountDir: resolvedMountDir,
    initialFileCount: input.initialFileCount,
    initialMountDurationMs: input.initialMountDurationMs,
    population: input.population,
    async syncBack(opts?: { signal?: AbortSignal; paths?: Iterable<string> }): Promise<number> {
      let synced = 0;
      const realProjectDir = realpathSync(resolvedProjectDir);
      const realMountDir = realpathSync(resolvedMountDir);
      // No-sync-back subtrees (`.git/**` under includeGit) can never produce
      // a sync, so don't descend into them at all — `.git` alone is often
      // thousands of entries.
      const files = opts?.paths
        ? syncBackPathsToFiles(realMountDir, opts.paths)
        : listFiles(realMountDir, (relPosix) =>
            isPathMatched(relPosix, noSyncBackMatcher, true)
          );
      const signal = opts?.signal;

      for (const sourceFile of files) {
        if (signal?.aborted) {
          break;
        }

        const syncedForFile = syncMountedFileBack(
          sourceFile,
          realMountDir,
          realProjectDir,
          readonlyMatcher,
          isIgnored,
          noSyncBackMatcher,
          (relPosix) => isExcludedPath(relPosix, excludeRules)
        );
        synced += syncedForFile;

        if (signal && syncedForFile > 0 && !signal.aborted) {
          // Intentionally uses setTimeout(resolve, 0) rather than the
          // setImmediate-based yieldToEventLoop helper below: aborts
          // mid-walk are scheduled via setTimeout (see mount.test.ts
          // "syncBack: returns a partial count when aborted mid-walk"),
          // and matching the same queue makes the abort observable
          // between file syncs. setImmediate runs after timer
          // callbacks in a single I/O cycle and races the abort.
          await new Promise<void>((resolve) => setTimeout(resolve, 0));
        }
      }

      return synced;
    },
    startAutoSync(opts?: AutoSyncOptions): AutoSyncHandle {
      return startAutoSync(autoSyncContext, opts);
    },
    cleanup(): void {
      removeMountDir(resolvedMountDir);
    },
  };
}

function assertMountDirSafeToRemove(mountDir: string, projectDir: string): void {
  const resolved = path.resolve(mountDir);
  const parsed = path.parse(resolved);
  // Refuse filesystem roots (posix '/' or a Windows drive root like 'C:\').
  if (resolved === parsed.root) {
    throw new Error(`Refusing to use filesystem root as mountDir: ${resolved}`);
  }
  // Refuse anything that would overlap the project directory (destroying the
  // project would be catastrophic). `path.resolve(projectDir)` avoids following
  // symlinks so both the symlink-target and the literal argument are rejected.
  const resolvedProject = path.resolve(projectDir);
  if (
    resolved === resolvedProject ||
    resolved.startsWith(`${resolvedProject}${path.sep}`) ||
    resolvedProject.startsWith(`${resolved}${path.sep}`)
  ) {
    throw new Error(`mountDir ${resolved} overlaps projectDir ${resolvedProject}`);
  }
  // If the directory already exists, require it to be a directory we created
  // previously (identified by the mount marker file). Otherwise the caller
  // pointed us at an unrelated directory and removing it would destroy data.
  if (!existsSync(resolved)) {
    return;
  }
  let stat;
  try {
    stat = lstatSync(resolved);
  } catch (err) {
    throw new Error(`Failed to stat mountDir ${resolved}: ${(err as Error).message}`);
  }
  if (!stat.isDirectory()) {
    throw new Error(`mountDir ${resolved} exists and is not a directory`);
  }
  const markerPath = path.join(resolved, MOUNT_MARKER_FILENAME);
  if (!existsSync(markerPath)) {
    throw new Error(
      `Refusing to remove ${resolved}: missing ${MOUNT_MARKER_FILENAME} marker. ` +
        `Only directories previously created by createMount can be reused as mountDir.`
    );
  }
}

// Yield often enough that a consumer's setInterval (e.g. an `ora` spinner) can
// tick during init even on flat directories with thousands of entries. The
// goal is not throughput; it is keeping the parent event loop unblocked.
const WALK_YIELD_EVERY = 64;

async function walkProjectTree(
  projectDir: string,
  currentDir: string,
  mountDir: string,
  currentMountDir: string,
  excludeRules: ExcludeRules,
  readonlyMatcher: Ignore,
  isIgnored: IgnoredPredicate,
  state: Map<string, FileState>
): Promise<number> {
  await yieldToEventLoop();
  const entries = readdirSync(currentDir, { withFileTypes: true });

  let processed = 0;
  let copiedFiles = 0;
  for (const entry of entries) {
    if (processed > 0 && processed % WALK_YIELD_EVERY === 0) {
      await yieldToEventLoop();
    }
    processed += 1;

    const absolutePath = path.join(currentDir, entry.name);
    const relativePath = normalizeRelativePosix(path.relative(projectDir, absolutePath));

    if (!relativePath || relativePath.startsWith('..')) {
      continue;
    }

    if (isPathWithinRoot(absolutePath, mountDir)) {
      continue;
    }

    if (isExcludedPath(relativePath, excludeRules)) {
      continue;
    }

    if (isIgnored(relativePath, entry.isDirectory())) {
      continue;
    }

    const mountPath = path.join(currentMountDir, entry.name);

    if (entry.isDirectory()) {
      const safeMountDir = ensureDirectoryWithinRoot(mountDir, mountPath);
      if (!safeMountDir) {
        continue;
      }
      copiedFiles += await walkProjectTree(
        projectDir,
        absolutePath,
        mountDir,
        safeMountDir,
        excludeRules,
        readonlyMatcher,
        isIgnored,
        state
      );
      continue;
    }

    if (entry.isSymbolicLink()) {
      if (copySymlinkedFile(
        projectDir,
        mountDir,
        absolutePath,
        mountPath,
        relativePath,
        readonlyMatcher,
        state
      )) {
        copiedFiles += 1;
      }
      continue;
    }

    if (!entry.isFile()) {
      continue;
    }

    if (copyMountedFile(
      projectDir,
      mountDir,
      absolutePath,
      mountPath,
      relativePath,
      readonlyMatcher,
      state
    )) {
      copiedFiles += 1;
    }
  }

  return copiedFiles;
}

/**
 * Populate the mount from a git-provided file list instead of a full tree
 * walk. The list is exactly `git ls-files --cached --others
 * --exclude-standard` output, so gitignored trees never even get visited;
 * exclude and ignore rules are still applied per path so callers' patterns
 * behave identically to walk mode.
 */
async function populateFromGitFileList(
  projectDir: string,
  mountDir: string,
  gitFiles: string[],
  excludeRules: ExcludeRules,
  readonlyMatcher: Ignore,
  isIgnored: IgnoredPredicate,
  state: Map<string, FileState>
): Promise<number> {
  // Sorted order keeps sibling files adjacent so the ensure-directory cache
  // inside resolveSafeCopyTarget hits its realpath cache line after line.
  const sorted = [...gitFiles].sort();
  let processed = 0;
  let copiedFiles = 0;
  for (const raw of sorted) {
    if (processed > 0 && processed % WALK_YIELD_EVERY === 0) {
      await yieldToEventLoop();
    }
    processed += 1;

    const relativePath = normalizeRelativePosix(raw);
    if (!relativePath || relativePath.startsWith('..') || path.isAbsolute(raw)) {
      continue;
    }
    if (isExcludedPath(relativePath, excludeRules)) {
      continue;
    }
    if (isIgnored(relativePath, false)) {
      continue;
    }

    const absolutePath = path.join(projectDir, ...relativePath.split('/'));
    if (isPathWithinRoot(absolutePath, mountDir)) {
      continue;
    }
    let entryStat: Stats;
    try {
      entryStat = lstatSync(absolutePath);
    } catch {
      // Listed but deleted from the working tree (staged deletes) — skip.
      continue;
    }
    const mountPath = path.join(mountDir, ...relativePath.split('/'));

    if (entryStat.isSymbolicLink()) {
      if (copySymlinkedFile(
        projectDir,
        mountDir,
        absolutePath,
        mountPath,
        relativePath,
        readonlyMatcher,
        state
      )) {
        copiedFiles += 1;
      }
      continue;
    }
    // Non-files (submodule gitlinks appear as directories, sockets, fifos)
    // are never mount candidates.
    if (!entryStat.isFile()) {
      continue;
    }
    if (copyMountedFile(
      projectDir,
      mountDir,
      absolutePath,
      mountPath,
      relativePath,
      readonlyMatcher,
      state
    )) {
      copiedFiles += 1;
    }
  }
  return copiedFiles;
}

interface GitPopulation {
  /** Tracked + untracked-unignored files, posix-relative to the project. */
  files: string[];
  /**
   * Root-scoped ignore rules: the project `.gitignore`,
   * `.git/info/exclude`, and the user's global excludes file
   * (`core.excludesFile`). Applied by the sync layers so they agree with
   * the populated set.
   */
  gitignoreLines: string[];
  /**
   * Nested `.gitignore` rule files discovered in the tracked file list,
   * each scoped to its directory the way git scopes them. Untracked nested
   * `.gitignore` files (rare — usually self-ignoring cache dirs) aren't
   * listed and so aren't represented; files they ignore merely lose the
   * walk-pruning speedup on the project side.
   */
  nestedIgnores: Array<{ prefix: string; lines: string[] }>;
  /**
   * Tracked files the gitignore rules match anyway (`git add -f` survivors,
   * `ls-files -ci`). Git syncs these regardless of ignore rules, so the
   * gitignore-derived matchers must except them — caller patterns still win.
   */
  trackedIgnoredFiles: string[];
}

type IgnoredPredicate = (relPosix: string, isDirectory?: boolean) => boolean;

/**
 * The "is this path hidden from the mount and its sync?" decision.
 *
 * Walk mode: caller patterns only (historical behavior). Git mode: caller
 * patterns first (they always win), then the repo's gitignore rules — except
 * for tracked-but-gitignored files (and their ancestor directories, so walk
 * pruning can't hide them), which git itself treats as ordinary content.
 */
function buildIgnoredPredicate(
  callerMatcher: Ignore,
  gitPopulation: GitPopulation | null
): IgnoredPredicate {
  if (gitPopulation === null) {
    return (relPosix, isDirectory) => isPathMatched(relPosix, callerMatcher, isDirectory);
  }
  const gitignoreMatcher = createPathMatcher(gitPopulation.gitignoreLines);
  // Nested .gitignore files scope to their directory: rules match paths
  // relative to the rule file's location, exactly as git applies them.
  const nested = gitPopulation.nestedIgnores.map(({ prefix, lines }) => ({
    prefix: prefix === '' ? '' : `${prefix}/`,
    matcher: createPathMatcher(lines),
  }));
  const trackedIgnored = new Set(gitPopulation.trackedIgnoredFiles);
  const trackedIgnoredDirs = new Set<string>();
  for (const file of gitPopulation.trackedIgnoredFiles) {
    let dir = file;
    while (dir.includes('/')) {
      dir = dir.slice(0, dir.lastIndexOf('/'));
      trackedIgnoredDirs.add(dir);
    }
  }
  return (relPosix, isDirectory) => {
    if (isPathMatched(relPosix, callerMatcher, isDirectory)) return true;
    if (isDirectory ? trackedIgnoredDirs.has(relPosix) : trackedIgnored.has(relPosix)) {
      return false;
    }
    if (isPathMatched(relPosix, gitignoreMatcher, isDirectory)) return true;
    for (const { prefix, matcher } of nested) {
      if (prefix !== '' && !relPosix.startsWith(prefix)) continue;
      const scoped = prefix === '' ? relPosix : relPosix.slice(prefix.length);
      if (scoped && isPathMatched(scoped, matcher, isDirectory)) return true;
    }
    return false;
  };
}

function runGitLsFiles(projectDir: string, args: string[]): string[] | null {
  let result;
  try {
    result = spawnSync('git', ['-C', projectDir, 'ls-files', '-z', ...args], {
      maxBuffer: 1024 * 1024 * 1024,
    });
  } catch {
    return null;
  }
  if (result.error || result.status !== 0) return null;
  return result.stdout.toString('utf8').split('\0').filter(Boolean);
}

function readIgnoreRuleLines(filePath: string): string[] {
  try {
    return readFileSync(filePath, 'utf8').split('\n');
  } catch {
    return [];
  }
}

/**
 * Decide whether git-list population applies and gather its inputs.
 * Returns null (→ walk fallback) when any precondition fails:
 *
 * - not a git checkout, or git itself fails;
 * - `.gitmodules` present — submodule working trees are populated by the
 *   walk but invisible to a plain `ls-files` call;
 * - caller patterns contain negations (`!keep`) — those can re-include
 *   gitignored paths, which a git-derived file list can never surface;
 * - with `includeGit`, patterns that match the root `.git` tree — the bulk
 *   `.git` clone doesn't consult matchers, only the walk does.
 */
function prepareGitPopulation(
  projectDir: string,
  ignoredPatterns: readonly string[],
  readonlyPatterns: readonly string[],
  includeGit: boolean
): GitPopulation | null {
  if (!existsSync(path.join(projectDir, '.git'))) return null;
  if (existsSync(path.join(projectDir, '.gitmodules'))) return null;
  const allPatterns = [...ignoredPatterns, ...readonlyPatterns];
  if (allPatterns.some((p) => p.trim().startsWith('!'))) return null;
  // Probe the actual matchers rather than inspecting pattern strings: this
  // catches every syntax that can reach inside `.git` (globs included)
  // without false-positiving on `.github` / `.gitignore`-style names.
  if (includeGit && patternsMatchGitDir(allPatterns)) return null;

  const trackedIgnored = runGitLsFiles(projectDir, ['--cached', '-i', '--exclude-standard']);
  if (trackedIgnored === null) return null;

  const listed = runGitLsFiles(projectDir, ['--cached', '--others', '--exclude-standard']);
  if (listed === null) return null;

  // Belt and braces: ls-files never emits `.git` paths, but the mount must
  // not trust a spawned tool's output for that invariant.
  const files = listed.filter((p) => p !== '.git' && !p.startsWith('.git/'));

  // Nested .gitignore files scope their rules to their own directory; the
  // listing honors them already, the sync predicate needs them explicitly.
  const nestedIgnores: Array<{ prefix: string; lines: string[] }> = [];
  for (const file of files) {
    if (!file.endsWith('/.gitignore')) continue;
    const prefix = normalizeRelativePosix(file.slice(0, -'/.gitignore'.length));
    const lines = readIgnoreRuleLines(path.join(projectDir, ...file.split('/')));
    const hasRules = lines.some((l) => {
      const t = l.trim();
      return t !== '' && !t.startsWith('#');
    });
    if (hasRules && prefix !== '') nestedIgnores.push({ prefix, lines });
  }

  return {
    files,
    gitignoreLines: [
      ...readIgnoreRuleLines(path.join(projectDir, '.gitignore')),
      ...readIgnoreRuleLines(path.join(projectDir, '.git', 'info', 'exclude')),
      // The user's global excludes also shaped the listing; without them the
      // sync layers would re-import globally-ignored files (.DS_Store etc.).
      ...readGlobalGitExcludeLines(projectDir),
    ],
    nestedIgnores,
    trackedIgnoredFiles: trackedIgnored.map((p) => normalizeRelativePosix(p)),
  };
}

/**
 * Would any caller pattern hide or freeze something under the root `.git`?
 * Tested against representative `.git` paths through the real matcher so
 * glob spellings are covered; `.github`-style names don't match.
 */
function patternsMatchGitDir(patterns: readonly string[]): boolean {
  const matcher = createPathMatcher([...patterns]);
  return (
    isPathMatched('.git', matcher, true) ||
    matcher.ignores('.git/config') ||
    matcher.ignores('.git/hooks/pre-commit')
  );
}

/**
 * Record autosync state for every file the `.git` bulk clone produced. The
 * clone preserved timestamps, so this is a stat-only sweep of both sides —
 * no content reads. Files whose project counterpart vanished between clone
 * and sweep are simply skipped and take the first-sight path later.
 */
function seedClonedGitState(
  projectDir: string,
  mountDir: string,
  state: Map<string, FileState>
): void {
  const mountGit = path.join(mountDir, '.git');
  if (!existsSync(mountGit)) return;
  for (const mountAbs of listFiles(mountGit)) {
    const rel = normalizeRelativePosix(path.relative(mountDir, mountAbs));
    if (!rel || rel.startsWith('..')) continue;
    try {
      const mountStat = lstatSync(mountAbs);
      if (!mountStat.isFile()) continue;
      const projectStat = lstatSync(path.join(projectDir, ...rel.split('/')));
      if (!projectStat.isFile()) continue;
      state.set(rel, {
        mountMtimeMs: mountStat.mtimeMs,
        projectMtimeMs: projectStat.mtimeMs,
      });
    } catch {
      /* skipped entries take autosync's first-sight path */
    }
  }
}

/** Lines from the user's global git excludes file (core.excludesFile or the XDG default). */
function readGlobalGitExcludeLines(projectDir: string): string[] {
  let configured: string | null = null;
  try {
    const result = spawnSync(
      'git',
      ['-C', projectDir, 'config', '--path', '--get', 'core.excludesfile'],
      { maxBuffer: 1024 * 1024 }
    );
    if (!result.error && result.status === 0) {
      configured = result.stdout.toString('utf8').trim() || null;
    }
  } catch {
    /* fall through to the XDG default */
  }
  const candidate =
    configured !== null
      ? // `--path` expands `~`; a bare relative value (a misconfiguration git
        // itself resolves against its process cwd) resolves against the
        // project dir here, matching the `git -C projectDir` listing calls.
        path.resolve(projectDir, configured)
      : path.join(
          process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'),
          'git',
          'ignore'
        );
  return readIgnoreRuleLines(candidate);
}

/**
 * Copy the project's `.git` into the mount as a single timestamp-preserving
 * bulk clone (copy-on-write via FICLONE where the filesystem supports it).
 * Returns the entry count contribution for `initialFileCount` (0 or 1 — the
 * per-file count of the walk path isn't worth a second traversal here).
 *
 * A worktree-style `.git` *file* (gitdir pointer) is copied as-is, matching
 * the walk path's behavior.
 */
function cloneGitInto(projectDir: string, mountDir: string): number {
  const source = path.join(projectDir, '.git');
  let sourceStat: Stats;
  try {
    sourceStat = lstatSync(source);
  } catch {
    return 0;
  }
  const target = path.join(mountDir, '.git');
  try {
    if (!sourceStat.isDirectory()) {
      // A worktree-style `.git` *file* is a gitdir pointer into the host
      // checkout's private worktree metadata. Copying it verbatim would let
      // git commands inside the mount mutate the host's index/HEAD, so
      // linked worktrees get no sandboxed `.git` at all under git
      // population. (The legacy walk still copies the pointer; fixing that
      // pre-existing behavior is out of scope here.)
      return 0;
    }
    cpSync(source, target, {
      recursive: true,
      force: true,
      preserveTimestamps: true,
      mode: fsConstants.COPYFILE_FICLONE,
      // Sockets and fifos (e.g. fsmonitor--daemon.ipc) can't be copied.
      filter: (src) => {
        try {
          const st = lstatSync(src);
          return st.isDirectory() || st.isFile() || st.isSymbolicLink();
        } catch {
          return false;
        }
      },
    });
    return 1;
  } catch {
    // Best-effort: a partially cloned .git is still more useful than a
    // failed mount. Git commands inside the mount surface any gaps.
    return 0;
  }
}

function yieldToEventLoop(): Promise<void> {
  return new Promise<void>((resolve) => setImmediate(resolve));
}

function copySymlinkedFile(
  projectDir: string,
  mountDir: string,
  sourcePath: string,
  mountPath: string,
  relativePath: string,
  readonlyMatcher: Ignore,
  state: Map<string, FileState>
): boolean {
  let realSource: string;
  let resolvedStat: Stats;
  try {
    realSource = realpathSync(sourcePath);
    resolvedStat = statSync(sourcePath);
  } catch {
    return false;
  }

  if (!isPathWithinRoot(realSource, projectDir) || !resolvedStat.isFile()) {
    return false;
  }

  return copyMountedFile(
    projectDir,
    mountDir,
    realSource,
    mountPath,
    relativePath,
    readonlyMatcher,
    // Auto-sync stats the project side with a symlink-rejecting lstat, so a
    // seeded entry for a symlink's path would read as "project side gone,
    // mount unchanged" on the first reconcile and delete the mount copy.
    // Leave symlink copies unseeded — they take the historical
    // first-sight path, whose symlink-target write guard keeps them alive.
    null,
    resolvedStat.mode
  );
}

function copyMountedFile(
  sourceRoot: string,
  mountDir: string,
  sourcePath: string,
  mountPath: string,
  relativePath: string,
  readonlyMatcher: Ignore,
  state: Map<string, FileState> | null,
  sourceMode?: number
): boolean {
  const safeMountPath = resolveSafeCopyTarget(mountDir, mountPath);
  if (!safeMountPath) {
    return false;
  }

  const safeSourcePath = resolveVerifiedFilePath(sourceRoot, sourcePath);
  if (!safeSourcePath) {
    return false;
  }

  const sourceStat = statSync(safeSourcePath);
  copyFileSync(safeSourcePath, safeMountPath, fsConstants.COPYFILE_FICLONE);
  // Carry the source mtime onto the copy so both trees stat as "the same
  // write" — reconcile and syncBack can then trust the stat quick check
  // instead of re-reading file contents.
  preserveMtime(safeMountPath, sourceStat);
  if (state) recordCopiedState(state, relativePath, safeMountPath, sourceStat);

  if (isPathMatched(relativePath, readonlyMatcher)) {
    chmodSync(safeMountPath, 0o444);
    return true;
  }

  const mode = sourceMode ?? sourceStat.mode;
  chmodSync(safeMountPath, mode & 0o777);
  return true;
}

/**
 * Seed the autosync state for a file the population loop just copied. The
 * mount side is stat'd after the mtime carry-over so the recorded value is
 * exactly what a later stat will report.
 */
function recordCopiedState(
  state: Map<string, FileState>,
  relativePath: string,
  mountPath: string,
  sourceStat: Stats
): void {
  try {
    state.set(normalizeRelativePosix(relativePath), {
      mountMtimeMs: statSync(mountPath).mtimeMs,
      projectMtimeMs: sourceStat.mtimeMs,
    });
  } catch {
    /* unseeded entries just take autosync's first-sight path */
  }
}

function ensureDirectory(pathValue: string): void {
  mkdirSync(pathValue, { recursive: true });
}

function ensureDirectoryWithinRoot(rootPath: string, dirPath: string): string | null {
  if (!isPathWithinRoot(dirPath, rootPath)) {
    return null;
  }

  try {
    ensureDirectory(dirPath);
    const realDir = realpathSync(dirPath);
    return isPathWithinRoot(realDir, rootPath) ? realDir : null;
  } catch {
    return null;
  }
}

function listFiles(
  baseDir: string,
  skipDir?: (relPosix: string) => boolean
): string[] {
  const files: string[] = [];
  const stack = [baseDir];
  while (stack.length > 0) {
    const current = stack.pop();
    if (!current) continue;
    const entries = readdirSync(current, { withFileTypes: true });
    for (const entry of entries) {
      const entryPath = path.join(current, entry.name);
      if (entry.isDirectory()) {
        if (skipDir) {
          const relPosix = normalizeRelativePosix(path.relative(baseDir, entryPath));
          if (relPosix && skipDir(relPosix)) continue;
        }
        stack.push(entryPath);
      } else if (entry.isFile() || entry.isSymbolicLink()) {
        files.push(entryPath);
      }
    }
  }
  return files;
}

function syncBackPathsToFiles(mountDir: string, relPaths: Iterable<string>): string[] {
  const files: string[] = [];
  const seen = new Set<string>();
  for (const relPath of relPaths) {
    const sourceFile = resolveSyncBackSource(mountDir, relPath);
    if (!sourceFile || seen.has(sourceFile)) {
      continue;
    }
    seen.add(sourceFile);
    files.push(sourceFile);
  }
  return files;
}

function resolveSyncBackSource(mountDir: string, relPath: string): string | null {
  const normalized = normalizeRelativePosix(relPath);
  if (!normalized || path.isAbsolute(normalized)) {
    return null;
  }
  const candidate = path.resolve(mountDir, ...normalized.split('/').filter(Boolean));
  return isPathWithinRoot(candidate, mountDir) ? candidate : null;
}

function normalizeRelativePosix(filePath: string): string {
  return filePath.split(path.sep).join('/');
}

function createPathMatcher(patterns: string[]): Ignore {
  return ignore().add(
    patterns.map((pattern) => pattern.trim()).filter((pattern) => pattern !== '' && !pattern.startsWith('#'))
  );
}

function createExcludeRules(
  excludeDirs: string[],
  includeGit: boolean,
  includeDefaultExcludeDirs: boolean
): ExcludeRules {
  const anyDepthNames = new Set<string>();
  const rootPrefixes = new Set<string>();

  if (includeDefaultExcludeDirs) {
    addExcludeEntries(anyDepthNames, rootPrefixes, DEFAULT_ANY_DEPTH_EXCLUDES, 'any-depth');
    addExcludeEntries(anyDepthNames, rootPrefixes, DEFAULT_ROOT_EXCLUDES, 'root-prefix');
  } else if (!includeGit) {
    addExcludeEntries(anyDepthNames, rootPrefixes, ['.git'], 'any-depth');
  }

  if (includeGit) {
    anyDepthNames.delete('.git');
  }

  // Preserve caller-supplied excludeDirs semantics: bare names match at any
  // depth, while path-style entries are root-anchored prefixes.
  addExcludeEntries(anyDepthNames, rootPrefixes, excludeDirs, 'legacy');

  return { anyDepthNames, rootPrefixes };
}

function addExcludeEntries(
  anyDepthNames: Set<string>,
  rootPrefixes: Set<string>,
  entries: string[],
  mode: 'any-depth' | 'root-prefix' | 'legacy'
): void {
  for (const entry of entries) {
    const normalized = normalizeRelativePosix(entry).replace(/^\/+|\/+$/g, '');
    if (!normalized) {
      continue;
    }
    if (mode === 'root-prefix' || (mode === 'legacy' && normalized.includes('/'))) {
      rootPrefixes.add(normalized);
    } else {
      anyDepthNames.add(normalized);
    }
  }
}

function isPathMatched(relPath: string, matcher: Ignore, isDirectory = false): boolean {
  const normalized = normalizeRelativePosix(relPath);
  // For directories, ask the matcher about the trailing-slash form — that's
  // the canonical "is this directory ignored?" question in gitignore semantics
  // and is what makes trailing-slash negations (`!dir/`) actually take effect.
  // The previous implementation OR'd the bare-name check first, which short-
  // circuited to "ignored" when a pattern like `/*` matched the bare name —
  // even if a later `!dir/` negated the directory form. The slash form is
  // safe for bare-name patterns too: `ignore` treats `node_modules` as
  // matching both `node_modules` and `node_modules/`.
  if (isDirectory) {
    return matcher.ignores(`${normalized}/`);
  }
  return matcher.ignores(normalized);
}

function hasSameContent(left: string, right: string): boolean {
  try {
    const leftStat = statSync(left);
    const rightStat = statSync(right);
    if (leftStat.size !== rightStat.size) {
      return false;
    }
    // Population preserves source mtimes onto copies, so equal size plus
    // (near-)equal mtime means "same write" without re-reading either side.
    if (statsImplySameContent(leftStat, rightStat)) {
      return true;
    }
    // Same size but diverged mtimes: fall back to a full byte comparison.
    const leftContent = readFileSync(left);
    const rightContent = readFileSync(right);
    return leftContent.equals(rightContent);
  } catch {
    return false;
  }
}

function syncMountedFileBack(
  sourceFile: string,
  mountDir: string,
  projectDir: string,
  readonlyMatcher: Ignore,
  isIgnored: IgnoredPredicate,
  noSyncBackMatcher: Ignore,
  isExcluded: (relPosix: string) => boolean
): number {
  const relative = resolveSyncRelativePath(
    sourceFile,
    mountDir,
    readonlyMatcher,
    isIgnored,
    noSyncBackMatcher,
    isExcluded
  );
  if (!relative) return 0;

  const safeTargetPath = resolveVerifiedSyncTarget(projectDir, relative);
  if (!safeTargetPath) return 0;

  if (existsSync(safeTargetPath) && hasSameContent(sourceFile, safeTargetPath)) {
    return 0;
  }

  copyFileSync(sourceFile, safeTargetPath);
  // Keep both sides stat-identical so a later pass (or a reused mount) can
  // take the quick check instead of re-reading content.
  try {
    preserveMtime(safeTargetPath, statSync(sourceFile));
  } catch {
    /* best effort */
  }
  return 1;
}

function resolveSyncRelativePath(
  sourceFile: string,
  mountDir: string,
  readonlyMatcher: Ignore,
  isIgnored: IgnoredPredicate,
  noSyncBackMatcher: Ignore,
  isExcluded: (relPosix: string) => boolean
): string | null {
  const relative = path.relative(mountDir, sourceFile);
  if (relative === '' || relative.startsWith('..')) return null;
  const relativePosix = normalizeRelativePosix(relative);
  if (relativePosix === MOUNT_README_FILENAME) return null;
  if (relativePosix === MOUNT_MARKER_FILENAME) return null;
  if (
    isExcluded(relativePosix) ||
    isPathMatched(relative, readonlyMatcher) ||
    isIgnored(relativePosix) ||
    isPathMatched(relative, noSyncBackMatcher)
  ) return null;

  try {
    if (lstatSync(sourceFile).isSymbolicLink()) return null;

    const realSource = realpathSync(sourceFile);
    if (!isPathWithinRoot(realSource, mountDir)) return null;
  } catch {
    return null;
  }

  return relative;
}

function resolveVerifiedSyncTarget(projectDir: string, relativePath: string): string | null {
  const targetPath = path.resolve(projectDir, relativePath);
  if (!isPathWithinRoot(targetPath, projectDir)) return null;

  const safeTargetPath = resolveSafeCopyTarget(projectDir, targetPath);
  if (!safeTargetPath || !existsSync(safeTargetPath)) {
    return safeTargetPath;
  }

  try {
    const targetStat = lstatSync(safeTargetPath);
    if (targetStat.isSymbolicLink()) return null;

    const realTarget = realpathSync(safeTargetPath);
    return isPathWithinRoot(realTarget, projectDir) ? safeTargetPath : null;
  } catch {
    return null;
  }
}

function isExcludedPath(relativePath: string, excludeRules: ExcludeRules): boolean {
  const normalized = normalizeRelativePosix(relativePath).replace(/^\/+|\/+$/g, '');
  if (!normalized) return false;
  const segments = normalized.split('/');
  return segments.some((segment, index) => {
    const prefix = segments.slice(0, index + 1).join('/');
    return excludeRules.anyDepthNames.has(segment) || excludeRules.rootPrefixes.has(prefix);
  });
}

function isPathWithinRoot(candidatePath: string, rootPath: string): boolean {
  const resolvedCandidate = path.resolve(candidatePath);
  const resolvedRoot = path.resolve(rootPath);
  return resolvedCandidate === resolvedRoot || resolvedCandidate.startsWith(`${resolvedRoot}${path.sep}`);
}

function resolveSafeCopyTarget(rootPath: string, candidatePath: string): string | null {
  if (!isPathWithinRoot(candidatePath, rootPath)) {
    return null;
  }

  const parentPath = path.dirname(candidatePath);
  const realParent = ensureDirectoryWithinRoot(rootPath, parentPath);
  if (!realParent) {
    return null;
  }

  return path.join(realParent, path.basename(candidatePath));
}

function resolveVerifiedFilePath(rootPath: string, candidatePath: string): string | null {
  try {
    const realCandidate = realpathSync(candidatePath);
    const candidateStat = statSync(candidatePath);
    if (!candidateStat.isFile()) {
      return null;
    }

    return isPathWithinRoot(realCandidate, rootPath) ? realCandidate : null;
  } catch {
    return null;
  }
}

function removeMountDir(mountDir: string): void {
  if (!existsSync(mountDir)) {
    return;
  }

  try {
    rmSync(mountDir, { recursive: true, force: true });
  } catch {
    // Best-effort cleanup.
  }
}

function buildMountReadme(
  agentName: string | undefined,
  readonlyPatterns: string[],
  ignoredPatterns: string[]
): string {
  const readonlyList = readonlyPatterns.length > 0 ? readonlyPatterns.join('\n') : '(none)';
  const ignoredList = ignoredPatterns.length > 0 ? ignoredPatterns.join('\n') : '(none)';
  const agentLine = agentName ? `\nAgent: ${agentName}\n` : '';
  return `# Workspace Permissions

This workspace is a mounted mirror of the project directory.
File access is controlled by project-local .agentignore and .agentreadonly.

## Read-only files (cannot be modified)
${readonlyList}

## Hidden files (not available in this workspace)
${ignoredList}

## Writable files
All other files can be read and modified freely.

If you get "permission denied", the file is read-only.
Changes to read-only files are not synced back to the source project.
Edits or permission changes to read-only files inside this mount may be discarded or overwritten when the mount is recreated.
${agentLine}`;
}
