# @relayfile/local-mount

Create a temporary mounted mirror of a project directory, enforce `.agentignore` and `.agentreadonly` rules inside that mount, run an agent or CLI there, then sync writable changes back to the real project on exit.

This package is useful when you want an agent to work in a constrained workspace without giving it direct write access to every file in the source tree.

## Install

```bash
npm install @relayfile/local-mount
```

## What it exports

### `createMount(projectDir, mountDir, options)`

Builds a mounted copy of `projectDir` at `mountDir` and resolves with a handle:

```ts
const handle = await createMount(projectDir, mountDir, options);

interface MountHandle {
  mountDir: string;
  initialFileCount?: number;
  initialMountDurationMs?: number;
  syncBack(opts?: { signal?: AbortSignal; paths?: Iterable<string> }): Promise<number>;
  startAutoSync(opts?: AutoSyncOptions): AutoSyncHandle;
  cleanup(): void;
}
```

`createMount` returns `Promise<MountHandle>`. The walker yields the event loop between directory entries so consumer-side timers (e.g. an `ora` spinner driven by `setInterval`) keep firing while the mount is being built.
The returned handle includes initial mount timing and copied-file count metadata so callers can report setup performance.

Behavior:
- Copies regular files into the mount, requesting a filesystem reflink clone when the source and mount are on a compatible same-volume filesystem and falling back to a byte copy otherwise
- Applies ignore rules from `ignoredPatterns`
- Marks read-only matches as mode `0o444`
- Excludes `.git`, `node_modules`, `.npm-cache`, and common build/cache output directories by default. Pass `includeGit: true` to opt the project's `.git` directory back in (see [Including `.git`](#including-git))
- Writes `_MOUNT_README.md` and `.relayfile-local-mount` into the mount
- Skips syncing `_MOUNT_README.md`, `.relayfile-local-mount`, ignored files, read-only files, and symlinks back to the source project

### `readAgentDotfiles(projectDir, options?)`

Reads project-local permission dotfiles and returns compiled pattern lists:

- `.agentignore`
- `.agentreadonly`
- `.{agentName}.agentignore` (optional)
- `.{agentName}.agentreadonly` (optional)

Blank lines and `#` comments are ignored.

```ts
const { ignoredPatterns, readonlyPatterns } = readAgentDotfiles(projectDir, {
  agentName: 'reviewer',
});
```

### `launchOnMount(options)`

High-level helper that:
1. creates a mount,
2. starts bidirectional auto-sync (see below, controllable via `autoSync`) and waits for the watchers to be ready when auto-sync is enabled,
3. runs `onBeforeLaunch`, then runs a CLI inside the mount,
4. forwards `SIGINT` and `SIGTERM`,
5. stops auto-sync and runs a final sync-back pass after the child exits,
6. cleans up the mount directory.

It resolves with the child process exit code. `onAfterSync(count)` receives the sum of files changed by auto-sync plus the final sync-back pass.

`launchOnMount({ shutdownSignal })` threads an optional `AbortSignal` into the shutdown phase only. It does not cancel the spawned CLI. If shutdown is aborted, `onAfterSync` still fires with the partial count gathered so far and the mount directory is still cleaned up.

### Auto-sync

By default, `launchOnMount` keeps the mount and project directory in sync continuously while the CLI is running, rather than only at exit. The same machinery is available standalone via `handle.startAutoSync()`.

```ts
interface AutoSyncOptions {
  /** Degraded-watcher full-reconcile interval. Default: 10_000 ms. 0/Infinity disables periodic scans. */
  scanIntervalMs?: number;
  /** Healthy-watcher full-reconcile interval. Default: 60_000 ms, or scanIntervalMs when set. */
  healthyScanIntervalMs?: number;
  /** Per-path event debounce in ms. Default: 50 ms. */
  debounceMs?: number;
  /** Invoked on sync errors. Defaults to swallowing them. */
  onError?: (err: Error) => void;
}

interface AutoSyncHandle {
  stop(opts?: { signal?: AbortSignal }): Promise<void>;
  flushPending(opts?: { signal?: AbortSignal }): Promise<number>;
  reconcile(opts?: { signal?: AbortSignal }): Promise<number>;
  getDirtyPaths(): IterableIterator<string>;
  watchersHealthy(): boolean;
  totalChanges(): number;
  ready(): Promise<void>;
}
```

Control it from `launchOnMount`:

```ts
// Disable entirely — only the final sync-back pass runs.
launchOnMount({ /* ... */, autoSync: false });

// Tune it.
launchOnMount({ /* ... */, autoSync: { healthyScanIntervalMs: 120_000, debounceMs: 100 } });

// Disable periodic full reconciles and rely on watcher events.
launchOnMount({ /* ... */, autoSync: { scanIntervalMs: 0 } });
```

How it works:
- [@parcel/watcher](https://www.npmjs.com/package/@parcel/watcher) watches both the mount and the project tree using native FSEvents/inotify/ReadDirectoryChangesW
- every `healthyScanIntervalMs` while watchers are healthy, a full reconcile walks both trees as a low-frequency safety net for missed events
- if watcher setup fails or a watcher reports an error, full reconciles fall back to `scanIntervalMs`
- watcher events are tracked as dirty paths, so shutdown can flush pending path-level work and make the final sync-back proportional to the number of mount-side changes when the watcher state stayed healthy
- `stop({ signal })` still closes watchers if aborted, but skips the final draining reconcile
- per-file `mtime` is tracked at the last sync, so the scan skips files that haven't changed

Conflict and delete rules:
- both sides changed since last sync → **mount wins**
- only one side changed → propagate that change
- one side deleted and the other unchanged since last sync → propagate the delete
- one side deleted and the other changed since last sync → recreate the missing file from the changed side
- readonly paths never flow mount→project; project-side edits still flow into the mount (the mount copy is re-chmodded `0o444`)
- `_MOUNT_README.md`, `.relayfile-local-mount`, ignored paths, and excluded directories never cross

## Default Excludes

By default, mounts skip directories and files that are usually large generated output
or local caches. These names match at any path depth:

```txt
.git
node_modules
.npm-cache
__pycache__
.pytest_cache
.mypy_cache
.ruff_cache
.gradle
.nyc_output
.turbo
.cache
.DS_Store
```

These more generic names match only at the project root so source paths such as
`src/build/` or `packages/env/` are still mounted:

```txt
target
.next
dist
build
out
.venv
venv
env
coverage
```

Pass `includeDefaultExcludeDirs: false` to opt out of the broad build/cache list.
For safety, `.git` stays excluded unless you also pass `includeGit: true`.
`excludeDirs` still appends to whichever default set is active; bare caller entries
retain the historical any-depth behavior, while path-style entries are root-relative
prefixes.

## Including `.git`

By default, the project's `.git` directory is excluded from the mount, which means git commands inside the mount fail with `fatal: not a git repository`. Pass `includeGit: true` (on `createMount` or `launchOnMount`) to copy `.git` into the mount with **one-way project→mount sync**:

- `.git` is copied on mount creation, so `git status`, `git log`, `git diff`, `git commit`, etc. all work inside the mount.
- Project-side changes under `.git/**` flow into the mount (e.g. if a teammate's tooling moves `HEAD` while the agent is running).
- Mount-side changes under `.git/**` are **not** synced back to the project. Branches, commits, or refs the agent creates in the mount stay sandboxed and are discarded on cleanup.

If the agent needs its commits to survive, push to a remote from inside the mount. Source files outside `.git` continue to follow the normal bidirectional sync rules.

```ts
launchOnMount({
  cli: 'claude',
  args: ['--print', 'Inspect the diff and propose a fix.'],
  projectDir,
  mountDir,
  includeGit: true,
});
```

Note that `.git` can be sizable (hundreds of MB on long-lived repos); the initial mount creation copies the whole tree.

## Dotfile semantics

`@relayfile/local-mount` uses glob-style patterns, powered by [`ignore`](https://www.npmjs.com/package/ignore).

### `.agentignore`

Files matching these patterns are omitted from the mount entirely.

Example:

```gitignore
secrets/
.env
coverage/
```

### `.agentreadonly`

Files matching these patterns are copied into the mount, but made read-only.
Changes to those files are not synced back.

Example:

```gitignore
package.json
docs/**
*.lock
```

### Per-agent overrides

If you pass `agentName`, the package also reads:

- `.{agentName}.agentignore`
- `.{agentName}.agentreadonly`

Those patterns are appended to the generic ones.

## Practical example

```ts
import { launchOnMount, readAgentDotfiles } from '@relayfile/local-mount';
import path from 'node:path';
import os from 'node:os';

const projectDir = '/projects/acme-api';
const mountDir = path.join(os.tmpdir(), 'acme-api-agent-mount');
const abortController = new AbortController();

const { ignoredPatterns, readonlyPatterns } = readAgentDotfiles(projectDir, {
  agentName: 'reviewer',
});

const result = await launchOnMount({
  cli: 'claude',
  args: ['--print', 'Review the codebase and update TODOs if needed.'],
  projectDir,
  mountDir,
  ignoredPatterns,
  readonlyPatterns,
  excludeDirs: ['vendor-cache'],
  agentName: 'reviewer',
  onBeforeLaunch: async (dir) => {
    // Add extra instructions or scratch files inside the mount if needed.
    console.log(`Mount ready at ${dir}`);
  },
  shutdownSignal: abortController.signal,
  onAfterSync: async (count) => {
    console.log(`Synced ${count} writable file(s) back to the project`);
  },
});

console.log(result.exitCode);
```

## Sync-back behavior

`syncBack()` is the one-shot, mount-only sweep used as a final pass (and available on its own if you disable auto-sync). It only writes files that are safe and writable:

- changed writable files are copied back
- new writable files created in the mount are copied back
- unchanged files are skipped
- ignored files are skipped
- read-only matches are skipped
- symlinks inside the mount are skipped

The returned number is the count of files written back to `projectDir` in that pass. `syncBack()` never deletes — delete propagation is handled by auto-sync.

`syncBack({ signal })` checks for aborts between files. If the signal aborts during shutdown, it returns the partial count accumulated so far instead of throwing, which lets callers report a partial sync and still run cleanup.

`syncBack({ paths })` limits the pass to explicit mount-relative paths. This is used by auto-sync shutdown after `watchersHealthy()` confirms both watchers subscribed and no watcher error was observed; otherwise callers should omit `paths` to keep the full-walk safety net.

`reconcile({ signal })` and the internal tree walk also poll for aborts between file visits so an in-flight draining scan can stop cooperatively.

## Safety constraints

The implementation is intentionally conservative about `mountDir`:

- `mountDir` must be different from `projectDir`
- `mountDir` cannot be a filesystem root
- `mountDir` cannot overlap the source project directory in either direction
- if `mountDir` already exists, it must contain the `.relayfile-local-mount` marker file from a previous mount created by this package

These checks help prevent accidental deletion of unrelated directories during mount recreation and cleanup.

## Why copy instead of symlink?

The mount is built by copying files rather than symlinking them. Symlinks would break several of the package's guarantees:

1. **`.agentreadonly` can't be enforced.** Read-only is implemented by `chmod 0o444` on the mount copy. `chmod` follows symlinks, so applying it to a symlink would mark the *source* file read-only, flipping the project's permissions instead of restricting the agent's view.
2. **The auto-sync conflict model assumes two distinct files.** Rules like "both sides changed → mount wins", "one side deleted → propagate", and "readonly paths never flow mount→project but project-side edits still flow into the mount" only make sense if mount and source are separate bytes. Through a symlink they're the same inode — there's no mount-side copy to re-chmod `0o444` after a project-side edit.
3. **Editor save-via-rename breaks symlinks anyway.** Most editors save by writing a temp file and renaming it over the target, which replaces the symlink with a regular file and severs the link. A "live view" via symlinks isn't reliable in practice.
4. **Containment.** A copy gives you a checkpoint: if the agent destroys files or writes garbage, the source is untouched until `syncBack()` filters and copies back. With symlinks, every keystroke is live on the project.
5. **`.agentignore` hiding works fine with symlinks** (just don't link), but the readonly and conflict semantics still need copies — so a hybrid would be more complex than just copying everything.

Source-side symlinks that resolve to regular files inside the project *are* followed when building the mount; the resolved bytes are copied. Symlinks the agent creates inside the mount are skipped on sync-back.

On filesystems that support copy-on-write clones (for example APFS on macOS and btrfs/xfs/zfs on Linux), initial mount creation requests a non-forcing reflink copy. This preserves the distinct inode and copy-on-write semantics required above while avoiding an up-front byte copy when `projectDir` and `mountDir` are on the same filesystem. Unsupported filesystems and cross-device mounts fall back to ordinary copies.

## Notes

- Requires Node.js 18+
- The current implementation copies files into the mount rather than creating filesystem-level FUSE mounts
- Source symlinks are only copied when they resolve to regular files inside the source project

## License

MIT
