import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { launchOnMount } from './launch.js';
import * as mountModule from './mount.js';

describe('launchOnMount', () => {
  let projectDir: string;
  let mountDir: string;

  beforeEach(() => {
    projectDir = mkdtempSync(path.join(os.tmpdir(), 'local-mount-launch-'));
    mountDir = path.join(
      mkdtempSync(path.join(os.tmpdir(), 'local-mount-launch-mount-')),
      'mount'
    );
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
    try { rmSync(path.dirname(mountDir), { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('runs the CLI in the mount, writes are synced back to projectDir, mount is cleaned up', async () => {
    writeFileSync(path.join(projectDir, 'seed.txt'), 'seed', 'utf8');

    let syncedSeen = -1;
    const result = await launchOnMount({
      cli: '/bin/sh',
      projectDir,
      mountDir,
      args: ['-c', 'echo hi > file.txt'],
      onAfterSync: (n) => {
        syncedSeen = n;
      },
    });

    expect(result.exitCode).toBe(0);
    expect(existsSync(path.join(projectDir, 'file.txt'))).toBe(true);
    expect(readFileSync(path.join(projectDir, 'file.txt'), 'utf8').trim()).toBe('hi');
    // The seed file should remain untouched (unchanged → not counted)
    expect(readFileSync(path.join(projectDir, 'seed.txt'), 'utf8')).toBe('seed');
    // file.txt was net-new from the mount, so it counts as 1 sync.
    expect(syncedSeen).toBe(1);
    // Mount dir has been cleaned up.
    expect(existsSync(mountDir)).toBe(false);
  });

  it('propagates a non-zero exit code from the child', async () => {
    const result = await launchOnMount({
      cli: '/bin/sh',
      projectDir,
      mountDir,
      args: ['-c', 'exit 42'],
    });
    expect(result.exitCode).toBe(42);
    expect(existsSync(mountDir)).toBe(false);
  });

  it('invokes onBeforeLaunch with the mount directory before spawning', async () => {
    let sawMountDir: string | undefined;
    const result = await launchOnMount({
      cli: '/bin/sh',
      projectDir,
      mountDir,
      args: ['-c', 'test -f marker.txt && exit 0 || exit 7'],
      onBeforeLaunch: (md) => {
        sawMountDir = md;
        writeFileSync(path.join(md, 'marker.txt'), 'here', 'utf8');
      },
    });
    expect(sawMountDir).toBeTruthy();
    expect(result.exitCode).toBe(0);
  });

  it('fires onAfterSync with a partial count and still cleans up when shutdownSignal aborts after child exit', async () => {
    mkdirSync(mountDir, { recursive: true });

    const controller = new AbortController();
    let cleanedUp = false;
    let stopCalled = false;

    const createSpy = vi.spyOn(mountModule, 'createMount').mockReturnValue({
      mountDir,
      startAutoSync: () => ({
        stop: async () => {
          stopCalled = true;
        },
        reconcile: async () => 0,
        totalChanges: () => 0,
        ready: async () => {},
      }),
      syncBack: async ({ signal } = {}) => {
        let synced = 0;
        for (let i = 0; i < 3; i += 1) {
          if (signal?.aborted) {
            break;
          }
          synced += 1;
          if (i === 0) {
            controller.abort();
          }
          await new Promise((resolve) => setImmediate(resolve));
        }
        return synced;
      },
      cleanup: () => {
        cleanedUp = true;
      },
    });

    let syncedSeen = -1;
    try {
      const result = await launchOnMount({
        cli: '/bin/sh',
        projectDir,
        mountDir,
        args: ['-c', 'exit 0'],
        shutdownSignal: controller.signal,
        onAfterSync: (count) => {
          syncedSeen = count;
        },
      });

      expect(result.exitCode).toBe(0);
      expect(stopCalled).toBe(true);
      expect(syncedSeen).toBe(1);
      expect(cleanedUp).toBe(true);
    } finally {
      createSpy.mockRestore();
    }
  });
});
