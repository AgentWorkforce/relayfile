# @relayfile/local-mount

Create a temporary mounted mirror of a project directory, enforce `.agentignore` and `.agentreadonly` rules inside that mount, run an agent or CLI there, then sync writable changes back to the real project on exit.

This package is useful when you want an agent to work in a constrained workspace without giving it direct write access to every file in the source tree.

## Install

```bash
npm install @relayfile/local-mount
```

## What it exports

### `createMount(projectDir, mountDir, options)`

Builds a mounted copy of `projectDir` at `mountDir` and returns a handle:

```ts
interface MountHandle {
  mountDir: string;
  syncBack(opts?: { signal?: AbortSignal }): Promise<number>;
  startAutoSync(opts?: AutoSyncOptions): AutoSyncHandle;
  cleanup(): void;
}
```

Behavior:
- Copies regular files into the mount
- Applies ignore rules from `ignoredPatterns`
- Marks read-only matches as mode `0o444`
- Excludes `.git` and `node_modules` by default. Pass `includeGit: true` to opt the project's `.git` directory back in (see [Including `.git`](#including-git))
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
2. starts bidirectional auto-sync (see below, controllable via `autoSync`),
3. runs a CLI inside the mount,
4. forwards `SIGINT` and `SIGTERM`,
5. stops auto-sync and runs a final sync-back pass after the child exits,
6. cleans up the mount directory.

It resolves with the child process exit code. `onAfterSync(count)` receives the sum of files changed by auto-sync plus the final sync-back pass.

`launchOnMount({ shutdownSignal })` threads an optional `AbortSignal` into the shutdown phase only. It does not cancel the spawned CLI. If shutdown is aborted, `onAfterSync` still fires with the partial count gathered so far and the mount directory is still cleaned up.

### Auto-sync

By default, `launchOnMount` keeps the mount and project directory in sync continuously while the CLI is running, rather than only at exit. The same machinery is available standalone via `handle.startAutoSync()`.

```ts
interface AutoSyncOptions {
  /** Full-reconcile interval as a safety net. Default: 10_000 ms. */
  scanIntervalMs?: number;
  /** Per-path event debounce in ms. Default: 50 ms. */
  debounceMs?: number;
  /** Invoked on sync errors. Defaults to swallowing them. */
  onError?: (err: Error) => void;
}

interface AutoSyncHandle {
  stop(opts?: { signal?: AbortSignal }): Promise<void>;
  reconcile(opts?: { signal?: AbortSignal }): Promise<number>;
  totalChanges(): number;
  ready(): Promise<void>;
}
```

Control it from `launchOnMount`:

```ts
// Disable entirely — only the final sync-back pass runs.
launchOnMount({ /* ... */, autoSync: false });

// Tune it.
launchOnMount({ /* ... */, autoSync: { scanIntervalMs: 5_000, debounceMs: 100 } });
```

How it works:
- [@parcel/watcher](https://www.npmjs.com/package/@parcel/watcher) watches both the mount and the project tree using native FSEvents/inotify/ReadDirectoryChangesW
- every `scanIntervalMs`, a full reconcile walks both trees as a safety net for missed events
- `stop({ signal })` still closes watchers if aborted, but skips the final draining reconcile
- per-file `mtime` is tracked at the last sync, so the scan skips files that haven't changed

Conflict and delete rules:
- both sides changed since last sync → **mount wins**
- only one side changed → propagate that change
- one side deleted and the other unchanged since last sync → propagate the delete
- one side deleted and the other changed since last sync → recreate the missing file from the changed side
- readonly paths never flow mount→project; project-side edits still flow into the mount (the mount copy is re-chmodded `0o444`)
- `_MOUNT_README.md`, `.relayfile-local-mount`, ignored paths, and excluded directories never cross

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
  excludeDirs: ['dist'],
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

## Notes

- Requires Node.js 18+
- The current implementation copies files into the mount rather than creating filesystem-level FUSE mounts
- Source symlinks are only copied when they resolve to regular files inside the source project

## License

MIT
