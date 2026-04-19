# @relayfile/local-mount

Create a temporary mounted mirror of a project directory, enforce `.agentignore` and `.agentreadonly` rules inside that mount, run an agent or CLI there, then sync writable changes back to the real project on exit.

This package is useful when you want an agent to work in a constrained workspace without giving it direct write access to every file in the source tree.

## Install

```bash
npm install @relayfile/local-mount
```

## What it exports

### `createSymlinkMount(projectDir, mountDir, options)`

Builds a mounted copy of `projectDir` at `mountDir` and returns a handle:

```ts
interface SymlinkMountHandle {
  mountDir: string;
  syncBack(): Promise<number>;
  cleanup(): void;
}
```

Behavior:
- Copies regular files into the mount
- Applies ignore rules from `ignoredPatterns`
- Marks read-only matches as mode `0o444`
- Excludes `.git` and `node_modules` by default
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
2. runs a CLI inside it,
3. forwards `SIGINT` and `SIGTERM`,
4. syncs writable changes back after the child exits,
5. cleans up the mount directory.

It resolves with the child process exit code.

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
  onAfterSync: async (count) => {
    console.log(`Synced ${count} writable file(s) back to the project`);
  },
});

console.log(result.exitCode);
```

## Sync-back behavior

When `syncBack()` runs, the package only writes back files that are safe and writable:

- changed writable files are copied back
- new writable files created in the mount are copied back
- unchanged files are skipped
- ignored files are skipped
- read-only matches are skipped
- symlinks inside the mount are skipped

The returned number is the count of files actually written back to `projectDir`.

## Safety constraints

The implementation is intentionally conservative about `mountDir`:

- `mountDir` must be different from `projectDir`
- `mountDir` cannot be a filesystem root
- `mountDir` cannot overlap the source project directory in either direction
- if `mountDir` already exists, it must contain the `.relayfile-local-mount` marker file from a previous mount created by this package

These checks help prevent accidental deletion of unrelated directories during mount recreation and cleanup.

## Notes

- Requires Node.js 18+
- The current implementation copies files into the mount rather than creating filesystem-level FUSE mounts
- Source symlinks are only copied when they resolve to regular files inside the source project

## License

MIT
