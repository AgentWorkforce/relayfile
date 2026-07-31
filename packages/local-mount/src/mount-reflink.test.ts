import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>();
  return {
    ...actual,
    copyFileSync: vi.fn(actual.copyFileSync),
  };
});

import {
  constants as fsConstants,
  copyFileSync,
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

const copyFileSyncMock = vi.mocked(copyFileSync);

function tmpDir(): string {
  return mkdtempSync(path.join(os.tmpdir(), 'local-mount-reflink-test-'));
}

function write(file: string, body: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, body, 'utf8');
}

async function waitFor(check: () => boolean, timeoutMs = 5000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms`);
}

describe('createMount reflink copies', () => {
  let projectDir: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountDir = path.join(tmpDir(), 'mount');
    copyFileSyncMock.mockClear();
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(path.dirname(mountDir), { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('requests non-forcing filesystem reflinks for initial mount files', async () => {
    write(path.join(projectDir, 'src/code.ts'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    expect(copyFileSyncMock).toHaveBeenCalledWith(
      expect.stringMatching(/src[/\\]code\.ts$/),
      expect.stringMatching(/src[/\\]code\.ts$/),
      fsConstants.COPYFILE_FICLONE
    );
    expect(existsSync(path.join(handle.mountDir, 'src/code.ts'))).toBe(true);
    expect(handle.initialFileCount).toBe(1);
    expect(handle.initialMountDurationMs).toBeGreaterThanOrEqual(0);

    writeFileSync(path.join(handle.mountDir, 'src/code.ts'), 'mount-only edit', 'utf8');
    expect(readFileSync(path.join(projectDir, 'src/code.ts'), 'utf8')).toBe('original');

    handle.cleanup();
  });

  it('requests non-forcing filesystem reflinks for auto-sync copies', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });
    const auto = handle.startAutoSync({ debounceMs: 50, scanIntervalMs: 10_000 });
    await auto.ready();
    copyFileSyncMock.mockClear();

    try {
      writeFileSync(path.join(handle.mountDir, 'file.txt'), 'edited-in-mount', 'utf8');
      await waitFor(() => readFileSync(path.join(projectDir, 'file.txt'), 'utf8') === 'edited-in-mount');
      expect(copyFileSyncMock).toHaveBeenCalledWith(
        expect.stringMatching(/file\.txt$/),
        // Auto-sync copies into a temporary sibling and renames it over the
        // target, so the write never goes *through* whatever the target
        // currently names (a hardlink, or a symlink swapped in mid-operation).
        // The reflink request itself is unchanged — COPYFILE_FICLONE is still
        // what the copy asks for, which is what this test is about. COPYFILE_EXCL
        // is paired with it so the create fails if anything already occupies the
        // temporary name, which is what stops a planted symlink being followed.
        expect.stringMatching(/\.rfsync-[0-9a-f]+$/),
        fsConstants.COPYFILE_FICLONE | fsConstants.COPYFILE_EXCL
      );

      copyFileSyncMock.mockClear();
      writeFileSync(path.join(projectDir, 'file.txt'), 'edited-in-project', 'utf8');
      await waitFor(() =>
        readFileSync(path.join(handle.mountDir, 'file.txt'), 'utf8') === 'edited-in-project'
      );
      expect(copyFileSyncMock).toHaveBeenCalledWith(
        expect.stringMatching(/file\.txt$/),
        // Auto-sync copies into a temporary sibling and renames it over the
        // target, so the write never goes *through* whatever the target
        // currently names (a hardlink, or a symlink swapped in mid-operation).
        // The reflink request itself is unchanged — COPYFILE_FICLONE is still
        // what the copy asks for, which is what this test is about. COPYFILE_EXCL
        // is paired with it so the create fails if anything already occupies the
        // temporary name, which is what stops a planted symlink being followed.
        expect.stringMatching(/\.rfsync-[0-9a-f]+$/),
        fsConstants.COPYFILE_FICLONE | fsConstants.COPYFILE_EXCL
      );
    } finally {
      await auto.stop();
      handle.cleanup();
    }
  });
});
