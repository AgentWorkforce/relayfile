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
import path from 'node:path';
import { createMount } from './mount.js';

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

    const handle = createMount(projectDir, mountDir, {
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

  it('propagates project→mount external edits', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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

    const handle = createMount(projectDir, mountDir, {
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
    const handle = createMount(projectDir, mountDir, {
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
