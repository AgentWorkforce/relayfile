import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const watcherMock = vi.hoisted(() => ({
  subscribe: vi.fn(),
}));

vi.mock('@parcel/watcher', () => ({
  default: watcherMock,
}));

import {
  startAutoSync,
  type AutoSyncContext,
  type AutoSyncHandle,
} from './auto-sync.js';

function tmpDir(): string {
  return mkdtempSync(path.join(os.tmpdir(), 'local-mount-scheduler-'));
}

function write(file: string, body: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, body, 'utf8');
}

async function waitFor(check: () => boolean, timeoutMs = 5000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (check()) return;
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms`);
}

function syncContext(realMountDir: string, realProjectDir: string): AutoSyncContext {
  return {
    realMountDir: realpathSync(realMountDir),
    realProjectDir: realpathSync(realProjectDir),
    isExcluded: () => false,
    excludedAnyDepthNames: [],
    excludedRootPrefixes: [],
    isIgnored: () => false,
    isReadonly: () => false,
    isNoSyncBack: () => false,
    isReservedFile: () => false,
  };
}

async function stopAborted(auto: AutoSyncHandle): Promise<void> {
  const controller = new AbortController();
  controller.abort();
  await auto.stop({ signal: controller.signal });
}

describe('startAutoSync scheduler policy', () => {
  let projectDir: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = tmpDir();
    mountDir = tmpDir();
    watcherMock.subscribe.mockReset();
    watcherMock.subscribe.mockImplementation(async () => ({
      unsubscribe: vi.fn(async () => { /* noop */ }),
    }));
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(mountDir, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it.each([0, Infinity])(
    'does not run periodic reconcile when scanIntervalMs is %s',
    async (scanIntervalMs) => {
      write(path.join(projectDir, 'file.txt'), 'original');
      write(path.join(mountDir, 'file.txt'), 'original');

      const auto = startAutoSync(syncContext(mountDir, projectDir), {
        scanIntervalMs,
        debounceMs: 10_000,
      });
      await auto.ready();
      try {
        writeFileSync(path.join(mountDir, 'file.txt'), 'changed', 'utf8');
        await new Promise((r) => setTimeout(r, 250));

        expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('original');
      } finally {
        await stopAborted(auto);
      }
    }
  );

  it('rejects scan intervals that exceed the setTimeout maximum', () => {
    const ctx = syncContext(mountDir, projectDir);

    expect(() =>
      startAutoSync(ctx, { scanIntervalMs: 2_147_483_648 })
    ).toThrow(RangeError);
    expect(() =>
      startAutoSync(ctx, { healthyScanIntervalMs: 2_147_483_648 })
    ).toThrow(RangeError);
  });

  it('uses the longer healthy scan interval once watcher subscriptions are ready', async () => {
    write(path.join(projectDir, 'file.txt'), 'original');
    write(path.join(mountDir, 'file.txt'), 'original');

    const auto = startAutoSync(syncContext(mountDir, projectDir), {
      scanIntervalMs: 50,
      healthyScanIntervalMs: 10_000,
      debounceMs: 10_000,
    });
    await auto.ready();
    try {
      expect(auto.watchersHealthy()).toBe(true);

      writeFileSync(path.join(mountDir, 'file.txt'), 'changed', 'utf8');
      await new Promise((r) => setTimeout(r, 250));

      expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8')).toBe('original');
    } finally {
      await stopAborted(auto);
    }
  });

  it('falls back to the degraded scan interval when watcher setup fails', async () => {
    const realProjectDir = realpathSync(projectDir);
    watcherMock.subscribe.mockImplementation(async (root: string) => {
      if (root === realProjectDir) {
        throw new Error('project watcher failed');
      }
      return { unsubscribe: vi.fn(async () => { /* noop */ }) };
    });
    write(path.join(mountDir, 'file.txt'), 'from-mount');

    const errors: Error[] = [];
    const auto = startAutoSync(syncContext(mountDir, projectDir), {
      scanIntervalMs: 50,
      healthyScanIntervalMs: 10_000,
      debounceMs: 10_000,
      onError: (err) => errors.push(err),
    });

    try {
      await expect(auto.ready()).rejects.toThrow('project watcher failed');
      expect(auto.watchersHealthy()).toBe(false);

      const projectFile = path.join(projectDir, 'file.txt');
      await waitFor(() =>
        existsSync(projectFile) && readFileSync(projectFile, 'utf8') === 'from-mount'
      );
      expect(errors).toHaveLength(1);
    } finally {
      await auto.stop();
    }
  });
});
