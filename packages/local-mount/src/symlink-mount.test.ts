import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
  existsSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createSymlinkMount } from './symlink-mount.js';

function tmpDir(): string {
  return mkdtempSync(path.join(os.tmpdir(), 'local-mount-test-'));
}

function write(file: string, body: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, body, 'utf8');
}

describe('createSymlinkMount', () => {
  let projectDir: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountDir = path.join(tmpDir(), 'mount');
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(path.dirname(mountDir), { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('happy path: copies non-ignored files, chmods readonly to 0o444, skips ignored', () => {
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, 'secrets/api-key.txt'), 'shhh');
    write(path.join(projectDir, 'docs/guide.md'), 'guide');
    write(path.join(projectDir, 'config.ro'), 'frozen');

    const handle = createSymlinkMount(projectDir, mountDir, {
      ignoredPatterns: ['secrets/'],
      readonlyPatterns: ['*.ro', 'docs/**'],
      excludeDirs: [],
    });

    expect(existsSync(path.join(handle.mountDir, 'src/code.ts'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'docs/guide.md'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'config.ro'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'secrets/api-key.txt'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'secrets'))).toBe(false);

    const roMode = statSync(path.join(handle.mountDir, 'config.ro')).mode & 0o777;
    expect(roMode).toBe(0o444);

    const guideMode = statSync(path.join(handle.mountDir, 'docs/guide.md')).mode & 0o777;
    expect(guideMode).toBe(0o444);

    // Writable file retains something other than 0o444
    const writableMode = statSync(path.join(handle.mountDir, 'src/code.ts')).mode & 0o777;
    expect(writableMode).not.toBe(0o444);

    handle.cleanup();
  });

  it('refuses mountDir === projectDir', () => {
    expect(() =>
      createSymlinkMount(projectDir, projectDir, {
        ignoredPatterns: [],
        readonlyPatterns: [],
        excludeDirs: [],
      })
    ).toThrow(/mountDir must be different from projectDir/);
  });

  it('excludes .git and node_modules by default, but NOT .relay', () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref');
    write(path.join(projectDir, 'node_modules/dep/index.js'), '1');
    write(path.join(projectDir, '.relay/state.json'), '{}');
    write(path.join(projectDir, 'keep.txt'), 'yes');

    const handle = createSymlinkMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    expect(existsSync(path.join(handle.mountDir, 'keep.txt'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, '.git'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'node_modules'))).toBe(false);
    // Regression: .relay must NOT be excluded by default anymore.
    expect(existsSync(path.join(handle.mountDir, '.relay/state.json'))).toBe(true);

    handle.cleanup();
  });

  it('syncBack: writes back writable changes, skips readonly, skips _MOUNT_README.md, returns count', async () => {
    write(path.join(projectDir, 'writable.txt'), 'original');
    write(path.join(projectDir, 'readonly.txt'), 'original-ro');

    const handle = createSymlinkMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: ['readonly.txt'],
      excludeDirs: [],
    });

    // Modify writable file in mount
    writeFileSync(path.join(handle.mountDir, 'writable.txt'), 'changed', 'utf8');
    // Modify "readonly" file directly (bypass chmod for test purposes)
    // by writing it with write permissions first
    const roPath = path.join(handle.mountDir, 'readonly.txt');
    chmodSync(roPath, 0o644);
    writeFileSync(roPath, 'tampered', 'utf8');

    // Add a net-new writable file in the mount
    writeFileSync(path.join(handle.mountDir, 'new.txt'), 'net-new', 'utf8');

    // _MOUNT_README.md must never sync back
    writeFileSync(path.join(handle.mountDir, '_MOUNT_README.md'), 'mutated readme', 'utf8');

    const synced = await handle.syncBack();

    // writable.txt (changed) + new.txt (added) = 2 syncs. readonly and readme are skipped.
    expect(synced).toBe(2);
    expect(readFileSync(path.join(projectDir, 'writable.txt'), 'utf8')).toBe('changed');
    expect(readFileSync(path.join(projectDir, 'new.txt'), 'utf8')).toBe('net-new');
    expect(readFileSync(path.join(projectDir, 'readonly.txt'), 'utf8')).toBe('original-ro');
    expect(existsSync(path.join(projectDir, '_MOUNT_README.md'))).toBe(false);

    handle.cleanup();
  });
});
