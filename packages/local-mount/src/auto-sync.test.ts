import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { attachMount, createMount } from './mount.js';

function tmpDir(): string {
  return mkdtempSync(path.join(os.tmpdir(), 'local-mount-autosync-'));
}

function write(file: string, body: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, body, 'utf8');
}

/**
 * Wait up to `timeoutMs` for `check` to return true. Useful for letting
 * the watcher observe a write and propagate it.
 */
async function waitFor(check: () => boolean, timeoutMs = 5000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (check()) return;
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms`);
}

describe('startAutoSync', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('propagates mount→project edits without waiting for final syncBack', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    // Use a short debounce so the test runs quickly; still long enough
    // to coalesce a single write.
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'edited-in-mount', 'utf8');
      await waitFor(() => readFileSync(path.join(projectDir, 'file.txt'), 'utf8') === 'edited-in-mount');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('tracks dirty mount paths and flushes pending debounced writes', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 10_000, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      expect(auto.watchersHealthy()).toBe(true);
      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'changed', 'utf8');

      await waitFor(() => Array.from(auto.getDirtyPaths()).includes('file.txt'));
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('original');

      const flushed = await auto.flushPending();

      expect(flushed).toBe(1);
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('changed');
      expect(Array.from(auto.getDirtyPaths())).toEqual([]);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('propagates project→mount external edits', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(path.join(projectDir, 'file.txt'), 'edited-externally', 'utf8');
      await waitFor(() =>
        readFileSync(path.join(handle.mountDir, 'file.txt'), 'utf8') === 'edited-externally'
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('propagates mount→project deletes', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      rmSync(path.join(handle.mountDir, 'file.txt'));
      await waitFor(() => !existsSync(path.join(projectDir, 'file.txt')));
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('propagates project→mount deletes', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      rmSync(path.join(projectDir, 'file.txt'));
      await waitFor(() => !existsSync(path.join(handle.mountDir, 'file.txt')));
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('respects readonly patterns: mount-side edits do not sync back', async () => {
    write(path.join(projectDir, 'locked.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: ['locked.txt'],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      // Bypass the 0o444 permission for the test.
      const mountFile = path.join(handle.mountDir, 'locked.txt');
      chmodSync(mountFile, 0o644);
      writeFileSync(mountFile, 'tampered', 'utf8');

      // Give autosync time to notice and choose not to propagate.
      await new Promise((r) => setTimeout(r, 300));
      await auto.reconcile();

      expect(readFileSync(path.join(projectDir, 'locked.txt'), 'utf8')).toBe('original');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('readonly: project-side edits flow into the mount', async () => {
    write(path.join(projectDir, 'locked.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: ['locked.txt'],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(path.join(projectDir, 'locked.txt'), 'updated-externally', 'utf8');
      await waitFor(() =>
        readFileSync(path.join(handle.mountDir, 'locked.txt'), 'utf8') === 'updated-externally'
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('mount-wins: concurrent edits on both sides resolve to mount content', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    // Don't start autosync yet — set up the conflict state first, then
    // trigger a single reconcile so we exercise the resolution rule.
    const auto = handle.startAutoSync({ debounceMs: 10, scanIntervalMs: 10_000 });
    // Stop immediately to drain priming; then mutate and reconcile manually.
    await auto.stop();

    const auto2 = handle.startAutoSync({ debounceMs: 10, scanIntervalMs: 10_000 });
    await auto2.ready();
    try {
      writeFileSync(path.join(projectDir, 'file.txt'), 'project-side', 'utf8');
      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'mount-side', 'utf8');

      await waitFor(() =>
        readFileSync(path.join(projectDir, 'file.txt'), 'utf8') === 'mount-side'
      );
      expect(readFileSync(path.join(handle.mountDir, 'file.txt'), 'utf8')).toBe('mount-side');
    } finally {
      await auto2.stop();
      handle.cleanup();
    }
  });

  it('ignored paths are never synced in either direction', async () => {
    write(path.join(projectDir, 'keep.txt'), 'keep');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: ['secrets/'],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      // File appearing in project under an ignored path — must NOT appear in mount.
      write(path.join(projectDir, 'secrets/api-key.txt'), 'shhh');
      // File appearing in mount under an ignored path — must NOT leak back.
      write(path.join(handle.mountDir, 'secrets/planted.txt'), 'evil');

      await new Promise((r) => setTimeout(r, 300));
      await auto.reconcile();

      expect(existsSync(path.join(handle.mountDir, 'secrets/api-key.txt'))).toBe(false);
      expect(existsSync(path.join(projectDir, 'secrets/planted.txt'))).toBe(false);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('directory-only ignore patterns do not swallow like-named files', async () => {
    // Pattern `cache/` means "ignore the cache directory" — a *file* whose
    // path happens to include a segment of the same name must still sync.
    write(path.join(projectDir, 'docs/cache'), 'this is a file, not a dir');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: ['cache/'],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(path.join(handle.mountDir, 'docs/cache'), 'edited', 'utf8');
      await waitFor(() =>
        readFileSync(path.join(projectDir, 'docs/cache'), 'utf8') === 'edited'
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('periodic full scan catches changes even if watcher events are missed', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    // Start autosync but we'll rely on the explicit reconcile() call rather
    // than waiting for the watcher, to simulate a missed event.
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 100 });
    try {
      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'edited', 'utf8');
      // Forcing a reconcile should find the change regardless of whether
      // the watcher already fired.
      await auto.reconcile();
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('edited');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('stop({ signal }) skips the draining reconcile when already aborted, but still closes watchers', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();

    writeFileSync(path.join(handle.mountDir, 'file.txt'), 'changed-before-stop', 'utf8');

    const controller = new AbortController();
    controller.abort();

    try {
      await auto.stop({ signal: controller.signal });
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('original');

      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'changed-after-stop', 'utf8');
      await new Promise((r) => setTimeout(r, 200));
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('original');
    } finally {
      handle.cleanup();
    }
  });

  it('includeGit: project-side .git edits flow into the mount', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref: refs/heads/main\n');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(
        path.join(projectDir, '.git/HEAD'),
        'ref: refs/heads/feature\n',
        'utf8'
      );
      await waitFor(() =>
        readFileSync(path.join(handle.mountDir, '.git/HEAD'), 'utf8') ===
          'ref: refs/heads/feature\n'
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('includeGit: mount-side .git edits do NOT flow back to the project', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref: refs/heads/main\n');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(
        path.join(handle.mountDir, '.git/HEAD'),
        'ref: refs/heads/feature\n',
        'utf8'
      );
      writeFileSync(path.join(handle.mountDir, '.git/COMMIT_EDITMSG'), 'wip\n', 'utf8');

      // Give autosync time to notice and choose not to propagate.
      await new Promise((r) => setTimeout(r, 300));
      await auto.reconcile();

      expect(readFileSync(path.join(projectDir, '.git/HEAD'), 'utf8')).toBe(
        'ref: refs/heads/main\n'
      );
      expect(existsSync(path.join(projectDir, '.git/COMMIT_EDITMSG'))).toBe(false);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('does not sync the _MOUNT_README.md or marker files', async () => {
    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      writeFileSync(path.join(handle.mountDir, '_MOUNT_README.md'), 'mutated', 'utf8');
      await new Promise((r) => setTimeout(r, 300));
      await auto.reconcile();
      expect(existsSync(path.join(projectDir, '_MOUNT_README.md'))).toBe(false);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });
});

describe('startAutoSync initial-state seeding', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('project edits made between createMount and startAutoSync win over the stale mount copy', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    // Host-side edit lands after the mount was populated but before autosync
    // starts. The population-seeded state knows the mount copy matches
    // "original", so reconcile must classify this as a project-side change —
    // not fall into the no-history tiebreak that lets the stale mount copy
    // overwrite the newer project content.
    await new Promise((r) => setTimeout(r, 20));
    write(path.join(projectDir, 'file.txt'), 'edited on host');

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      await auto.reconcile();
      expect(readFileSync(path.join(handle.mountDir, 'file.txt'), 'utf8')).toBe('edited on host');
      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('edited on host');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('mount-side .git config written before startAutoSync survives reconcile (includeGit)', async () => {
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, '.git/HEAD'), 'ref: refs/heads/main\n');
    write(path.join(projectDir, '.git/info/exclude'), '# base\n');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
    });

    // Session setup appends to the mount's .git/info/exclude before autosync
    // starts (e.g. hiding materialized config files from git status). With
    // seeded state this is a mount-only change on a no-sync-back path: it
    // must neither flow back nor be clobbered by the project's copy.
    writeFileSync(
      path.join(handle.mountDir, '.git/info/exclude'),
      '# base\n# session additions\n',
      'utf8'
    );

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      await auto.reconcile();
      expect(readFileSync(path.join(handle.mountDir, '.git/info/exclude'), 'utf8')).toBe(
        '# base\n# session additions\n'
      );
      expect(readFileSync(path.join(projectDir, '.git/info/exclude'), 'utf8')).toBe('# base\n');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });
});

describe('attachMount warm reuse', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  const mountOptions = {
    ignoredPatterns: [],
    readonlyPatterns: [],
    excludeDirs: [],
  };

  it('refuses a directory without the mount marker', async () => {
    mkdirSync(mountDir, { recursive: true });
    await expect(attachMount(projectDir, mountDir, mountOptions)).rejects.toThrow(/marker/);
  });

  it('round-trip: exportState → attachMount propagates project-side deletes instead of resurrecting', async () => {
    write(path.join(projectDir, 'keep.txt'), 'keep');
    write(path.join(projectDir, 'gone.txt'), 'doomed');

    // Session 1: create, capture state, keep the mount dir.
    const first = await createMount(projectDir, mountDir, mountOptions);
    const auto1 = first.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto1.ready();
    await auto1.reconcile();
    const state = auto1.exportState();
    await auto1.stop();
    expect(Object.keys(state)).toContain('gone.txt');
    // Intentionally no cleanup(): the mount dir is being kept warm.

    // Between sessions: the file disappears from the project.
    rmSync(path.join(projectDir, 'gone.txt'));

    // Session 2: reattach with the exported state.
    const second = await attachMount(projectDir, mountDir, {
      ...mountOptions,
      initialState: state,
    });
    expect(second.population).toBe('reattach');
    const auto2 = second.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto2.ready();
    try {
      await auto2.reconcile();
      // With state: recognized as a project-side delete → removed from mount.
      expect(existsSync(path.join(second.mountDir, 'gone.txt'))).toBe(false);
      expect(existsSync(path.join(projectDir, 'gone.txt'))).toBe(false);
      expect(readFileSync(path.join(second.mountDir, 'keep.txt'), 'utf8')).toBe('keep');
    } finally {
      await auto2.stop();
      second.cleanup();
    }
  });

  it('reattach without exported state resurrects deletes (documents why state matters)', async () => {
    write(path.join(projectDir, 'gone.txt'), 'doomed');

    const first = await createMount(projectDir, mountDir, mountOptions);
    // No autosync, no state export — simulate a naive reuse.
    rmSync(path.join(projectDir, 'gone.txt'));

    const second = await attachMount(projectDir, mountDir, mountOptions);
    const auto = second.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      await auto.reconcile();
      // Mount-only file with no history → treated as created in the mount.
      expect(existsSync(path.join(projectDir, 'gone.txt'))).toBe(true);
    } finally {
      await auto.stop();
      second.cleanup();
      void first;
    }
  });

  it('reattach picks up project-side edits made while the mount sat idle', async () => {
    write(path.join(projectDir, 'file.txt'), 'v1');

    const first = await createMount(projectDir, mountDir, mountOptions);
    const auto1 = first.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto1.ready();
    await auto1.reconcile();
    const state = auto1.exportState();
    await auto1.stop();

    await new Promise((r) => setTimeout(r, 20));
    write(path.join(projectDir, 'file.txt'), 'v2 from host');
    write(path.join(projectDir, 'new-file.txt'), 'brand new');

    const second = await attachMount(projectDir, mountDir, {
      ...mountOptions,
      initialState: state,
    });
    const auto2 = second.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto2.ready();
    try {
      await auto2.reconcile();
      expect(readFileSync(path.join(second.mountDir, 'file.txt'), 'utf8')).toBe('v2 from host');
      expect(readFileSync(path.join(second.mountDir, 'new-file.txt'), 'utf8')).toBe('brand new');
    } finally {
      await auto2.stop();
      second.cleanup();
    }
  });
});

describe('git population sync semantics', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  function git(...args: string[]): void {
    const res = spawnSync('git', ['-C', projectDir, ...args]);
    if (res.status !== 0) {
      throw new Error(`git ${args.join(' ')} failed: ${res.stderr?.toString()}`);
    }
  }

  it('reconcile does not import gitignored trees into a git-populated mount', async () => {
    git('init', '--quiet');
    git('config', 'user.email', 'test@example.com');
    git('config', 'user.name', 'Test');
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, '.gitignore'), 'junk/\n');
    git('add', '-A');
    git('commit', '--quiet', '-m', 'init');
    // Gitignored tree that only the gitignore knows about (not a default
    // exclude, not a caller pattern).
    write(path.join(projectDir, 'junk/worktrees/big.txt'), 'x'.repeat(500));

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'git',
    });
    expect(existsSync(path.join(handle.mountDir, 'junk'))).toBe(false);

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      const changes = await auto.reconcile();
      // The gitignored tree must not be pulled into the mount by the
      // project-side reconcile walk.
      expect(existsSync(path.join(handle.mountDir, 'junk'))).toBe(false);
      expect(changes).toBe(0);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('syncBack ignores gitignored files an agent created inside the mount', async () => {
    git('init', '--quiet');
    git('config', 'user.email', 'test@example.com');
    git('config', 'user.name', 'Test');
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, '.gitignore'), 'scratch/\n');
    git('add', '-A');
    git('commit', '--quiet', '-m', 'init');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'git',
    });

    write(path.join(handle.mountDir, 'scratch/tmp.txt'), 'session-local');
    write(path.join(handle.mountDir, 'src/new.ts'), 'real work');
    const synced = await handle.syncBack();

    expect(synced).toBe(1);
    expect(existsSync(path.join(projectDir, 'scratch'))).toBe(false);
    expect(readFileSync(path.join(projectDir, 'src/new.ts'), 'utf8')).toBe('real work');

    handle.cleanup();
  });

  it('tracked-but-gitignored files stay mounted and synced (exception to gitignore rules)', async () => {
    git('init', '--quiet');
    git('config', 'user.email', 'test@example.com');
    git('config', 'user.name', 'Test');
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, 'forced.log'), 'tracked despite ignore');
    write(path.join(projectDir, '.gitignore'), '*.log\n');
    git('add', '-A');
    git('add', '-f', 'forced.log');
    git('commit', '--quiet', '-m', 'init');
    // Untracked file the same rule ignores — must stay out of the mount.
    write(path.join(projectDir, 'scratch.log'), 'untracked junk');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'git',
    });

    expect(handle.population).toBe('git');
    // Git syncs forced-added files regardless of gitignore; so do we.
    expect(existsSync(path.join(handle.mountDir, 'forced.log'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'scratch.log'))).toBe(false);

    // And edits to it inside the mount flow back on syncBack.
    writeFileSync(path.join(handle.mountDir, 'forced.log'), 'edited in mount', 'utf8');
    const synced = await handle.syncBack();
    expect(synced).toBe(1);
    expect(readFileSync(path.join(projectDir, 'forced.log'), 'utf8')).toBe('edited in mount');

    handle.cleanup();
  });

  it('falls back to walk when caller patterns contain negations', async () => {
    git('init', '--quiet');
    git('config', 'user.email', 'test@example.com');
    git('config', 'user.name', 'Test');
    write(path.join(projectDir, 'src/code.ts'), 'code');
    git('add', '-A');
    git('commit', '--quiet', '-m', 'init');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: ['/*', '!src'],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'auto',
    });

    expect(handle.population).toBe('walk');
    handle.cleanup();
  });
});

describe('external teardown safety', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('a mount torn down externally never mass-deletes the project', async () => {
    write(path.join(projectDir, 'src/a.ts'), 'a');
    write(path.join(projectDir, 'src/b.ts'), 'b');
    write(path.join(projectDir, 'README.md'), 'readme');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      // Simulate a crash-cleanup / manual rm of the live session's mount.
      rmSync(handle.mountDir, { recursive: true, force: true });
      await auto.reconcile();

      // Every project file must survive: the vanished mount is a teardown,
      // not an agent deleting each file.
      expect(readFileSync(path.join(projectDir, 'src/a.ts'), 'utf8')).toBe('a');
      expect(readFileSync(path.join(projectDir, 'src/b.ts'), 'utf8')).toBe('b');
      expect(readFileSync(path.join(projectDir, 'README.md'), 'utf8')).toBe('readme');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('a project tree that vanishes never empties the mount', async () => {
    write(path.join(projectDir, 'src/a.ts'), 'agent work in progress');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      rmSync(projectDir, { recursive: true, force: true });
      await auto.reconcile();

      expect(readFileSync(path.join(handle.mountDir, 'src/a.ts'), 'utf8')).toBe(
        'agent work in progress'
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('single-file deletes still propagate while both roots are intact', async () => {
    write(path.join(projectDir, 'keep.txt'), 'keep');
    write(path.join(projectDir, 'gone.txt'), 'doomed');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      // Agent deletes one file inside the live mount → propagates.
      rmSync(path.join(handle.mountDir, 'gone.txt'));
      await auto.reconcile();
      expect(existsSync(path.join(projectDir, 'gone.txt'))).toBe(false);
      expect(existsSync(path.join(projectDir, 'keep.txt'))).toBe(true);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });
});

describe('review follow-ups', () => {
  let projectDir: string;
  let mountParent: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountParent = tmpDir();
    mountDir = path.join(mountParent, 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountParent, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  function git(...args: string[]): void {
    const res = spawnSync('git', ['-C', projectDir, ...args]);
    if (res.status !== 0) {
      throw new Error(`git ${args.join(' ')} failed: ${res.stderr?.toString()}`);
    }
  }

  function initGitProject(): void {
    git('init', '--quiet');
    git('config', 'user.email', 'test@example.com');
    git('config', 'user.name', 'Test');
  }

  it('symlinked project files survive the first reconcile', async () => {
    write(path.join(projectDir, 'real.txt'), 'target content');
    const { symlinkSync } = await import('node:fs');
    symlinkSync(path.join(projectDir, 'real.txt'), path.join(projectDir, 'link.txt'));

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    // The symlink was dereferenced into a regular file in the mount.
    expect(readFileSync(path.join(handle.mountDir, 'link.txt'), 'utf8')).toBe('target content');

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      await auto.reconcile();
      // Auto-sync's symlink-rejecting stat sees no project-side file; the
      // copy must not be treated as a propagatable delete.
      expect(readFileSync(path.join(handle.mountDir, 'link.txt'), 'utf8')).toBe('target content');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('nested .gitignore rules keep their trees out of git-populated sync', async () => {
    initGitProject();
    write(path.join(projectDir, 'pkg/src/code.ts'), 'code');
    write(path.join(projectDir, 'pkg/.gitignore'), 'cache/\n');
    git('add', '-A');
    git('commit', '--quiet', '-m', 'init');
    write(path.join(projectDir, 'pkg/cache/blob.bin'), 'x'.repeat(200));

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'git',
    });
    expect(existsSync(path.join(handle.mountDir, 'pkg/cache'))).toBe(false);

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      const changes = await auto.reconcile();
      expect(existsSync(path.join(handle.mountDir, 'pkg/cache'))).toBe(false);
      expect(changes).toBe(0);
    } finally {
      await auto.stop();
      handle.cleanup();
    }

    // syncBack must agree too: agent-created files under the nested-ignored
    // tree stay session-local.
    const second = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      population: 'git',
    });
    write(path.join(second.mountDir, 'pkg/cache/session.tmp'), 'local');
    const synced = await second.syncBack();
    expect(synced).toBe(0);
    expect(existsSync(path.join(projectDir, 'pkg/cache/session.tmp'))).toBe(false);
    second.cleanup();
  });

  it('git population + includeGit: pre-autosync mount .git edits survive, project .git deletions propagate', async () => {
    initGitProject();
    write(path.join(projectDir, 'src/code.ts'), 'code');
    git('add', '-A');
    git('commit', '--quiet', '-m', 'init');
    // A loose file the "project" will delete between clone and reconcile,
    // mimicking pack-refs/gc.
    write(path.join(projectDir, '.git/loose-marker'), 'about to be packed');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
      population: 'git',
    });
    expect(existsSync(path.join(handle.mountDir, '.git/loose-marker'))).toBe(true);

    // Session setup edit before autosync starts (configureGitForMount-style).
    const excludePath = path.join(handle.mountDir, '.git/info/exclude');
    const baseExclude = readFileSync(excludePath, 'utf8');
    writeFileSync(excludePath, `${baseExclude}# session additions\n`, 'utf8');

    // Host-side .git deletion while the session runs.
    rmSync(path.join(projectDir, '.git/loose-marker'));

    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      await auto.reconcile();
      // Deletion propagated (state was seeded for the cloned file)…
      expect(existsSync(path.join(handle.mountDir, '.git/loose-marker'))).toBe(false);
      // …and the mount-side setup write survived (mount-changed + no-sync-back).
      expect(readFileSync(excludePath, 'utf8')).toContain('# session additions');
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('exportState returns detached snapshots', async () => {
    write(path.join(projectDir, 'file.txt'), 'content');
    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    try {
      const first = auto.exportState();
      expect(first['file.txt']).toBeDefined();
      first['file.txt'].mountMtimeMs = -1;
      const second = auto.exportState();
      expect(second['file.txt'].mountMtimeMs).not.toBe(-1);
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });

  it('attachMount refuses a marked directory overlapping the project', async () => {
    write(path.join(projectDir, 'src/code.ts'), 'code');
    const insideProject = path.join(projectDir, 'nested-mount');
    mkdirSync(insideProject, { recursive: true });
    writeFileSync(
      path.join(insideProject, '.relayfile-local-mount'),
      'marker',
      'utf8'
    );
    await expect(
      attachMount(projectDir, insideProject, {
        ignoredPatterns: [],
        readonlyPatterns: [],
        excludeDirs: [],
      })
    ).rejects.toThrow(/overlaps/);
  });

  it("attachMount with explicit population 'git' throws for non-git projects", async () => {
    write(path.join(projectDir, 'src/code.ts'), 'code');
    mkdirSync(mountDir, { recursive: true });
    writeFileSync(path.join(mountDir, '.relayfile-local-mount'), 'marker', 'utf8');
    await expect(
      attachMount(projectDir, mountDir, {
        ignoredPatterns: [],
        readonlyPatterns: [],
        excludeDirs: [],
        population: 'git',
      })
    ).rejects.toThrow(/git checkout/);
  });
});
