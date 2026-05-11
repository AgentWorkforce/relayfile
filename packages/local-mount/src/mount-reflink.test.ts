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

    writeFileSync(path.join(handle.mountDir, 'src/code.ts'), 'mount-only edit', 'utf8');
    expect(readFileSync(path.join(projectDir, 'src/code.ts'), 'utf8')).toBe('original');

    handle.cleanup();
  });
});
