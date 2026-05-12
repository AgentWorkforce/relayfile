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
import { createMount } from './mount.js';

function tmpDir(): string {
  return mkdtempSync(path.join(os.tmpdir(), 'local-mount-test-'));
}

function write(file: string, body: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, body, 'utf8');
}

describe('createMount', () => {
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

  it('happy path: copies non-ignored files, chmods readonly to 0o444, skips ignored', async () => {
    write(path.join(projectDir, 'src/code.ts'), 'code');
    write(path.join(projectDir, 'secrets/api-key.txt'), 'shhh');
    write(path.join(projectDir, 'docs/guide.md'), 'guide');
    write(path.join(projectDir, 'config.ro'), 'frozen');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: ['secrets/'],
      readonlyPatterns: ['*.ro', 'docs/**'],
      excludeDirs: [],
    });

    expect(existsSync(path.join(handle.mountDir, 'src/code.ts'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'docs/guide.md'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'config.ro'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'secrets/api-key.txt'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'secrets'))).toBe(false);

    const roPath = path.join(handle.mountDir, 'config.ro');
    const roMode = statSync(roPath).mode & 0o777;
    expect(roMode).toBe(0o444);
    expect(() => writeFileSync(roPath, 'should fail', 'utf8')).toThrow();
    expect(readFileSync(path.join(projectDir, 'config.ro'), 'utf8')).toBe('frozen');

    const guideMode = statSync(path.join(handle.mountDir, 'docs/guide.md')).mode & 0o777;
    expect(guideMode).toBe(0o444);

    // Writable file retains something other than 0o444
    const writableMode = statSync(path.join(handle.mountDir, 'src/code.ts')).mode & 0o777;
    expect(writableMode).not.toBe(0o444);

    handle.cleanup();
  });

  it('refuses mountDir === projectDir', async () => {
    await expect(
      createMount(projectDir, projectDir, {
        ignoredPatterns: [],
        readonlyPatterns: [],
        excludeDirs: [],
      })
    ).rejects.toThrow(/mountDir must be different from projectDir/);
  });

  it('excludes common cache and build output paths by default, but NOT .relay', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref');
    write(path.join(projectDir, 'node_modules/dep/index.js'), '1');
    write(path.join(projectDir, 'packages/web/node_modules/dep/index.js'), 'nested dep');
    write(path.join(projectDir, '.npm-cache/_cacache/content-v2/sha512/aa/bb/blob'), 'cached');
    write(path.join(projectDir, 'target/debug/app'), 'rust');
    write(path.join(projectDir, '.next/cache/entry'), 'next');
    write(path.join(projectDir, 'dist/bundle.js'), 'dist');
    write(path.join(projectDir, 'build/output.js'), 'build');
    write(path.join(projectDir, 'out/page.html'), 'out');
    write(path.join(projectDir, '__pycache__/module.pyc'), 'python');
    write(path.join(projectDir, '.pytest_cache/v/cache/nodeids'), 'pytest');
    write(path.join(projectDir, '.mypy_cache/meta.json'), 'mypy');
    write(path.join(projectDir, '.ruff_cache/CACHEDIR.TAG'), 'ruff');
    write(path.join(projectDir, '.venv/bin/python'), 'venv');
    write(path.join(projectDir, 'venv/bin/python'), 'venv');
    write(path.join(projectDir, 'env/bin/python'), 'venv');
    write(path.join(projectDir, '.gradle/caches/modules-2/files'), 'gradle');
    write(path.join(projectDir, 'coverage/lcov.info'), 'coverage');
    write(path.join(projectDir, '.nyc_output/out.json'), 'nyc');
    write(path.join(projectDir, '.turbo/cache'), 'turbo');
    write(path.join(projectDir, '.cache/tool/state'), 'cache');
    write(path.join(projectDir, '.DS_Store'), 'metadata');
    write(path.join(projectDir, '.relay/state.json'), '{}');
    write(path.join(projectDir, 'src/build/helper.ts'), 'nested build source');
    write(path.join(projectDir, 'packages/env/config.ts'), 'nested env source');
    write(path.join(projectDir, 'lib/out/formatter.ts'), 'nested out source');
    write(path.join(projectDir, 'src/target/arm64.rs'), 'nested target source');
    write(path.join(projectDir, 'test/coverage/reporter.ts'), 'nested coverage source');
    write(path.join(projectDir, 'keep.txt'), 'yes');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    expect(existsSync(path.join(handle.mountDir, 'keep.txt'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, '.git'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'node_modules'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'packages/web/node_modules'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.npm-cache'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'target'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.next'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'dist'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'build'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'out'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '__pycache__'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.pytest_cache'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.mypy_cache'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.ruff_cache'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.venv'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'venv'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'env'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.gradle'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'coverage'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.nyc_output'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.turbo'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.cache'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, '.DS_Store'))).toBe(false);
    // Regression: .relay must NOT be excluded by default anymore.
    expect(existsSync(path.join(handle.mountDir, '.relay/state.json'))).toBe(true);
    // Generic build/cache names are root-level defaults, not any-depth matches.
    expect(existsSync(path.join(handle.mountDir, 'src/build/helper.ts'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'packages/env/config.ts'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'lib/out/formatter.ts'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'src/target/arm64.rs'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'test/coverage/reporter.ts'))).toBe(true);

    handle.cleanup();
  });

  it('can opt out of broad default excludes while keeping .git excluded by default', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref');
    write(path.join(projectDir, 'node_modules/dep/index.js'), '1');
    write(path.join(projectDir, 'dist/bundle.js'), 'dist');
    write(path.join(projectDir, '.cache/tool/state'), 'cache');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeDefaultExcludeDirs: false,
    });

    expect(existsSync(path.join(handle.mountDir, '.git'))).toBe(false);
    expect(existsSync(path.join(handle.mountDir, 'node_modules/dep/index.js'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, 'dist/bundle.js'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, '.cache/tool/state'))).toBe(true);

    handle.cleanup();
  });

  it('includeGit: copies .git into the mount and leaves it writable', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref: refs/heads/main\n');
    write(path.join(projectDir, '.git/refs/heads/main'), 'deadbeef\n');
    write(path.join(projectDir, 'src/code.ts'), 'code');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
    });

    expect(existsSync(path.join(handle.mountDir, '.git/HEAD'))).toBe(true);
    expect(existsSync(path.join(handle.mountDir, '.git/refs/heads/main'))).toBe(true);

    // Mount-side .git files must be writable so tools (git itself) can mutate
    // them locally — the noSyncBack guard, not 0o444, is what keeps changes
    // out of the project.
    const headMode = statSync(path.join(handle.mountDir, '.git/HEAD')).mode & 0o777;
    expect(headMode).not.toBe(0o444);

    handle.cleanup();
  });

  it('includeGit: syncBack does not propagate .git mount edits to the project', async () => {
    write(path.join(projectDir, '.git/HEAD'), 'ref: refs/heads/main\n');
    write(path.join(projectDir, 'src/code.ts'), 'code');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
      includeGit: true,
    });

    // Simulate a git command in the mount mutating .git internals AND a normal
    // source-file edit. Only the source edit should reach the project.
    writeFileSync(path.join(handle.mountDir, '.git/HEAD'), 'ref: refs/heads/feature\n', 'utf8');
    writeFileSync(path.join(handle.mountDir, '.git/COMMIT_EDITMSG'), 'wip\n', 'utf8');
    writeFileSync(path.join(handle.mountDir, 'src/code.ts'), 'edited', 'utf8');

    const synced = await handle.syncBack();

    expect(synced).toBe(1);
    expect(readFileSync(path.join(projectDir, '.git/HEAD'), 'utf8')).toBe(
      'ref: refs/heads/main\n'
    );
    expect(existsSync(path.join(projectDir, '.git/COMMIT_EDITMSG'))).toBe(false);
    expect(readFileSync(path.join(projectDir, 'src/code.ts'), 'utf8')).toBe('edited');

    handle.cleanup();
  });

  it('syncBack: writes back writable changes, skips readonly, skips _MOUNT_README.md, returns count', async () => {
    write(path.join(projectDir, 'writable.txt'), 'original');
    write(path.join(projectDir, 'readonly.txt'), 'original-ro');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: ['readonly.txt'],
      excludeDirs: [],
    });

    // Modify writable file in mount
    writeFileSync(path.join(handle.mountDir, 'writable.txt'), 'changed', 'utf8');
    // Modify "readonly" file directly (bypass chmod for test purposes)
    // by writing it with write permissions first.
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

  it('syncBack: skips files under default excluded paths', async () => {
    write(path.join(projectDir, 'src/code.ts'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    write(path.join(handle.mountDir, 'src/code.ts'), 'changed');
    write(path.join(handle.mountDir, 'dist/generated.js'), 'dist');
    write(path.join(handle.mountDir, 'build/generated.js'), 'build');
    write(path.join(handle.mountDir, 'node_modules/dep/index.js'), 'dep');
    write(path.join(handle.mountDir, 'packages/web/node_modules/dep/index.js'), 'nested dep');
    write(path.join(handle.mountDir, 'src/build/helper.ts'), 'source build');

    const synced = await handle.syncBack();

    expect(synced).toBe(2);
    expect(readFileSync(path.join(projectDir, 'src/code.ts'), 'utf8')).toBe('changed');
    expect(readFileSync(path.join(projectDir, 'src/build/helper.ts'), 'utf8')).toBe('source build');
    expect(existsSync(path.join(projectDir, 'dist'))).toBe(false);
    expect(existsSync(path.join(projectDir, 'build'))).toBe(false);
    expect(existsSync(path.join(projectDir, 'node_modules'))).toBe(false);
    expect(existsSync(path.join(projectDir, 'packages/web/node_modules'))).toBe(false);

    handle.cleanup();
  });

  it('syncBack: returns immediately when already aborted', async () => {
    write(path.join(projectDir, 'writable.txt'), 'original');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    writeFileSync(path.join(handle.mountDir, 'writable.txt'), 'changed', 'utf8');

    const controller = new AbortController();
    controller.abort();

    const synced = await handle.syncBack({ signal: controller.signal });

    expect(synced).toBe(0);
    expect(readFileSync(path.join(projectDir, 'writable.txt'), 'utf8')).toBe('original');

    handle.cleanup();
  });

  it('syncBack: returns a partial count when aborted mid-walk', async () => {
    write(path.join(projectDir, 'a.txt'), 'a0');
    write(path.join(projectDir, 'b.txt'), 'b0');
    write(path.join(projectDir, 'c.txt'), 'c0');

    const handle = await createMount(projectDir, mountDir, {
      ignoredPatterns: [],
      readonlyPatterns: [],
      excludeDirs: [],
    });

    writeFileSync(path.join(handle.mountDir, 'a.txt'), 'a1', 'utf8');
    writeFileSync(path.join(handle.mountDir, 'b.txt'), 'b1', 'utf8');
    writeFileSync(path.join(handle.mountDir, 'c.txt'), 'c1', 'utf8');

    const controller = new AbortController();
    setTimeout(() => controller.abort(), 0);

    const synced = await handle.syncBack({ signal: controller.signal });

    expect(synced).toBeGreaterThanOrEqual(1);
    expect(synced).toBeLessThan(3);

    const values = [
      readFileSync(path.join(projectDir, 'a.txt'), 'utf8'),
      readFileSync(path.join(projectDir, 'b.txt'), 'utf8'),
      readFileSync(path.join(projectDir, 'c.txt'), 'utf8'),
    ];
    expect(values.filter((value) => value.endsWith('1'))).toHaveLength(synced);

    handle.cleanup();
  });

  // Regression for #104: a synchronous walker froze the consumer's event loop
  // for the entire init window, so things like an `ora` spinner driven by
  // setInterval would not animate. Drive a setInterval counter while
  // createMount runs over a synthetic many-file tree and assert it advanced.
  it('yields the event loop during init so consumer setInterval can fire', async () => {
    const dirs = 50;
    const filesPerDir = 100;
    for (let d = 0; d < dirs; d += 1) {
      for (let f = 0; f < filesPerDir; f += 1) {
        write(path.join(projectDir, `d${d}`, `f${f}.txt`), 'x');
      }
    }

    let ticks = 0;
    const timer = setInterval(() => {
      ticks += 1;
    }, 5);

    let handle;
    try {
      handle = await createMount(projectDir, mountDir, {
        ignoredPatterns: [],
        readonlyPatterns: [],
        excludeDirs: [],
      });
    } finally {
      clearInterval(timer);
    }

    expect(ticks).toBeGreaterThanOrEqual(2);
    handle.cleanup();
  }, 10_000);
});
