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

  it('syncs files written by onBeforeLaunch when auto-sync is enabled', async () => {
    let syncedSeen = -1;
    const result = await launchOnMount({
      cli: '/bin/sh',
      projectDir,
      mountDir,
      args: ['-c', 'exit 0'],
      onBeforeLaunch: (md) => {
        writeFileSync(path.join(md, 'prelaunch.txt'), 'ready', 'utf8');
      },
      onAfterSync: (n) => {
        syncedSeen = n;
      },
    });

    expect(result.exitCode).toBe(0);
    expect(readFileSync(path.join(projectDir, 'prelaunch.txt'), 'utf8')).toBe('ready');
    expect(syncedSeen).toBe(1);
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

  it('waits for auto-sync readiness before invoking onBeforeLaunch', async () => {
    mkdirSync(mountDir, { recursive: true });

    let readyResolved = false;
    let beforeLaunchSawReady = false;
    let syncBackPaths: string[] | undefined;

    const createSpy = vi.spyOn(mountModule, 'createMount').mockResolvedValue({
      mountDir,
      startAutoSync: () => ({
        stop: async () => {},
        flushPending: async () => 0,
        reconcile: async () => 0,
        getDirtyPaths: () => ['tracked.txt'][Symbol.iterator](),
        watchersHealthy: () => true,
        totalChanges: () => 0,
        ready: async () => {
          await new Promise((resolve) => setTimeout(resolve, 10));
          readyResolved = true;
        },
      }),
      syncBack: async ({ paths } = {}) => {
        syncBackPaths = paths ? Array.from(paths) : undefined;
        return 0;
      },
      cleanup: () => {},
    });

    try {
      const result = await launchOnMount({
        cli: '/bin/sh',
        projectDir,
        mountDir,
        args: ['-c', 'exit 0'],
        onBeforeLaunch: () => {
          beforeLaunchSawReady = readyResolved;
        },
      });

      expect(result.exitCode).toBe(0);
      expect(beforeLaunchSawReady).toBe(true);
      expect(syncBackPaths).toEqual(['tracked.txt']);
    } finally {
      createSpy.mockRestore();
    }
  });

  it('falls back to full syncBack when watchers are healthy but no dirty paths were captured', async () => {
    // Repro for the dropped-edits race flagged in #138 review: when the
    // watcher stays healthy but doesn't observe any path before stop()
    // (e.g. short-lived child writes that race past the watcher, or
    // edits the watcher missed), getDirtyPaths returns empty. Passing
    // paths: [] to syncBack would skip the walk entirely and silently
    // drop those edits. The fix is to fall through to the full walk
    // when the dirty set is empty.
    mkdirSync(mountDir, { recursive: true });

    let syncBackPaths: string[] | undefined;
    let syncBackCalled = false;

    const createSpy = vi.spyOn(mountModule, 'createMount').mockResolvedValue({
      mountDir,
      startAutoSync: () => ({
        stop: async () => {},
        flushPending: async () => 0,
        reconcile: async () => 0,
        getDirtyPaths: () => [][Symbol.iterator](),
        watchersHealthy: () => true,
        totalChanges: () => 0,
        ready: async () => {},
      }),
      syncBack: async ({ paths } = {}) => {
        syncBackCalled = true;
        syncBackPaths = paths ? Array.from(paths) : undefined;
        return 0;
      },
      cleanup: () => {},
    });

    try {
      const result = await launchOnMount({
        cli: '/bin/sh',
        projectDir,
        mountDir,
        args: ['-c', 'exit 0'],
      });

      expect(result.exitCode).toBe(0);
      expect(syncBackCalled).toBe(true);
      // paths must NOT be passed when the dirty set is empty — otherwise
      // syncBack({ paths: [] }) iterates zero files and skips the walk.
      expect(syncBackPaths).toBeUndefined();
    } finally {
      createSpy.mockRestore();
    }
  });

  it('falls back to full syncBack when auto-sync readiness fails', async () => {
    mkdirSync(mountDir, { recursive: true });

    let syncBackPaths: string[] | undefined;

    const createSpy = vi.spyOn(mountModule, 'createMount').mockResolvedValue({
      mountDir,
      startAutoSync: () => ({
        stop: async () => {},
        flushPending: async () => 0,
        reconcile: async () => 0,
        getDirtyPaths: () => ['tracked.txt'][Symbol.iterator](),
        watchersHealthy: () => true,
        totalChanges: () => 0,
        ready: async () => {
          throw new Error('watcher failed');
        },
      }),
      syncBack: async ({ paths } = {}) => {
        syncBackPaths = paths ? Array.from(paths) : undefined;
        return 0;
      },
      cleanup: () => {},
    });

    try {
      const result = await launchOnMount({
        cli: '/bin/sh',
        projectDir,
        mountDir,
        args: ['-c', 'exit 0'],
      });

      expect(result.exitCode).toBe(0);
      expect(syncBackPaths).toBeUndefined();
    } finally {
      createSpy.mockRestore();
    }
  });

  it('fires onAfterSync with a partial count and still cleans up when shutdownSignal aborts after child exit', async () => {
    mkdirSync(mountDir, { recursive: true });

    const controller = new AbortController();
    let cleanedUp = false;
    let stopCalled = false;

    const createSpy = vi.spyOn(mountModule, 'createMount').mockResolvedValue({
      mountDir,
      startAutoSync: () => ({
        stop: async () => {
          stopCalled = true;
        },
        flushPending: async () => 0,
        reconcile: async () => 0,
        getDirtyPaths: () => [][Symbol.iterator](),
        watchersHealthy: () => true,
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
