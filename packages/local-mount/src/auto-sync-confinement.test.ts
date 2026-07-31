/**
 * Adversarial confinement matrix for the project↔mount sync path.
 *
 * Relayfile materializes agent-visible files onto real disk, so the mount
 * boundary is a real filesystem boundary: an agent that can influence what
 * exists inside the mount can potentially influence what gets written outside
 * it. `resolveSafeWriteTarget` is what stands in the way.
 *
 * This suite drives the **real resolver and the real caller sequence** from
 * `doProjectToMount`:
 *
 *     const target = resolveSafeWriteTarget(ctx.realMountDir, mountAbs);
 *     if (!target) return false;
 *     if (isSymlinkTarget(target)) return false;
 *     copyFileSync(projectAbs, target, COPYFILE_FICLONE);
 *
 * Two contracts are asserted separately, because conflating them is what hid
 * the worst defect in the equivalent code elsewhere — a refusal that deleted
 * the file it was protecting:
 *
 *   C1  A refusal makes no observable change outside the mount. Every negative
 *       case asserts the refusal *and* that outside state is byte-identical
 *       afterwards. Asserting only the return value passes against an
 *       implementation that damages data and then reports failure.
 *
 *   C2  A completed write is all-or-nothing.
 *
 * Provenance: this matrix comes from the Agent Relay × Ratify design-partner
 * spike, where it found four distinct defects in equivalent code — three found
 * by us, one by Identities AI. Every one was platform- or timing-dependent and
 * none would have been caught by asserting on a return value. It is applied here
 * to find out empirically which cases this implementation handles, rather than
 * reasoning about it from a reading.
 */

import { afterEach, describe, expect, it } from 'vitest';
import {
  constants as fsConstants,
  copyFileSync,
  existsSync,
  linkSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { isSymlinkTarget, resolveSafeWriteTarget } from './auto-sync.js';

const VICTIM = 'IMPORTANT PRE-EXISTING CONTENT';
const PAYLOAD = 'PAYLOAD-FROM-INSIDE-THE-MOUNT';

interface Sandbox {
  base: string;
  mount: string;
  outside: string;
  victim: string;
  source: string;
}

const dirs: string[] = [];

afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

function sandbox(): Sandbox {
  const base = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'rf-confine-')));
  const mount = path.join(base, 'mount');
  const outside = path.join(base, 'outside');
  const project = path.join(base, 'project');
  mkdirSync(mount);
  mkdirSync(outside);
  mkdirSync(project);
  const victim = path.join(outside, 'secret.txt');
  writeFileSync(victim, VICTIM);
  const source = path.join(project, 'source.txt');
  writeFileSync(source, PAYLOAD);
  dirs.push(base);
  return { base, mount, outside, victim, source };
}

/** relative path -> content, for everything under `dir`. */
function snapshot(dir: string): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (p: string, rel: string): void => {
    for (const name of readdirSync(p).sort()) {
      const full = path.join(p, name);
      const key = rel ? `${rel}/${name}` : name;
      const st = statSync(full, { throwIfNoEntry: false });
      if (!st) out[key] = '<broken>';
      else if (st.isDirectory()) {
        out[key] = '<dir>';
        walk(full, key);
      } else if (st.isFile()) out[key] = readFileSync(full, 'utf8');
      else out[key] = '<special>';
    }
  };
  walk(dir, '');
  return out;
}

/**
 * The real caller sequence, verbatim from `doProjectToMount`.
 * Returns 'refused' or 'written'. `between` fires in the race window that
 * exists between the symlink check and the copy.
 */
function syncAttempt(s: Sandbox, mountAbs: string, between?: () => void): 'refused' | 'written' {
  const target = resolveSafeWriteTarget(s.mount, mountAbs);
  if (!target) return 'refused';
  if (isSymlinkTarget(target)) return 'refused';
  between?.();
  copyFileSync(s.source, target, fsConstants.COPYFILE_FICLONE);
  return 'written';
}

/** Assert C1: refused, and nothing that existed outside the mount changed. */
function expectContained(s: Sandbox, run: () => 'refused' | 'written'): void {
  const before = snapshot(s.outside);
  let outcome: 'refused' | 'written' | 'threw' = 'threw';
  try {
    outcome = run();
  } catch {
    outcome = 'threw';
  }
  const after = snapshot(s.outside);

  for (const [key, value] of Object.entries(before)) {
    expect(after[key], `pre-existing outside entry ${key} was mutated or deleted`).toBe(value);
  }
  for (const [key, value] of Object.entries(after)) {
    expect(value, `payload escaped the mount into ${key}`).not.toBe(PAYLOAD);
  }
  expect(outcome, 'the write should not have completed').not.toBe('written');
}

describe('relayfile mount confinement — escapes', () => {
  it('refuses traversal above the mount', () => {
    const s = sandbox();
    expectContained(s, () => syncAttempt(s, path.join(s.mount, '..', 'outside', 'secret.txt')));
  });

  it('refuses an absolute path outside the mount', () => {
    const s = sandbox();
    expectContained(s, () => syncAttempt(s, s.victim));
  });

  it('refuses a final component that is a symlink pointing outside', () => {
    const s = sandbox();
    symlinkSync(s.victim, path.join(s.mount, 'link.txt'));
    expectContained(s, () => syncAttempt(s, path.join(s.mount, 'link.txt')));
  });

  it('refuses an intermediate directory that is a symlink pointing outside', () => {
    const s = sandbox();
    symlinkSync(s.outside, path.join(s.mount, 'docs'));
    expectContained(s, () => syncAttempt(s, path.join(s.mount, 'docs', 'secret.txt')));
  });

  it('refuses a hardlink inside the mount pointing at a file outside it', () => {
    // A hardlink is path-indistinguishable from a real file and realpath cannot
    // resolve it — it has no target. Link count is the only signal.
    const s = sandbox();
    linkSync(s.victim, path.join(s.mount, 'hard.txt'));
    expectContained(s, () => syncAttempt(s, path.join(s.mount, 'hard.txt')));
  });

  it('refuses when the target is swapped for a symlink after the check', () => {
    // The window between `isSymlinkTarget(target)` and `copyFileSync(target)`.
    const s = sandbox();
    const target = path.join(s.mount, 'notes.md');
    writeFileSync(target, 'placeholder');
    expectContained(s, () =>
      syncAttempt(s, target, () => {
        rmSync(target, { force: true });
        symlinkSync(s.victim, target);
      }),
    );
  });
});

describe('relayfile mount confinement — refusals must not mutate', () => {
  it('does not create directories outside the mount while refusing', () => {
    // resolveSafeWriteTarget runs mkdirSync(parent, { recursive: true }) before
    // it validates the resolved parent. If the parent traverses a symlink out
    // of the mount, directories are created outside and only then is the write
    // refused — a refusal with a side effect.
    const s = sandbox();
    symlinkSync(s.outside, path.join(s.mount, 'docs'));
    const before = snapshot(s.outside);

    syncAttempt(s, path.join(s.mount, 'docs', 'deep', 'nested', 'file.txt'));

    const after = snapshot(s.outside);
    const created = Object.keys(after).filter((k) => !(k in before));
    expect(created, 'refusal created entries outside the mount').toEqual([]);
  });
});

describe('relayfile mount confinement — positive controls', () => {
  // A confinement fix that refuses everything is as broken as one that permits
  // escapes. These must keep passing.

  it('writes a new file inside the mount', () => {
    const s = sandbox();
    const outcome = syncAttempt(s, path.join(s.mount, 'new.txt'));
    expect(outcome).toBe('written');
    expect(readFileSync(path.join(s.mount, 'new.txt'), 'utf8')).toBe(PAYLOAD);
  });

  it('creates nested directories inside the mount', () => {
    const s = sandbox();
    const outcome = syncAttempt(s, path.join(s.mount, 'a', 'b', 'c.txt'));
    expect(outcome).toBe('written');
    expect(readFileSync(path.join(s.mount, 'a/b/c.txt'), 'utf8')).toBe(PAYLOAD);
  });

  it('replaces an existing file inside the mount', () => {
    const s = sandbox();
    writeFileSync(path.join(s.mount, 'exists.txt'), 'OLD');
    const outcome = syncAttempt(s, path.join(s.mount, 'exists.txt'));
    expect(outcome).toBe('written');
    expect(readFileSync(path.join(s.mount, 'exists.txt'), 'utf8')).toBe(PAYLOAD);
  });

  it('supports a mount root reached through a symlink (as the caller resolves it)', () => {
    // `resolveSafeWriteTarget` has an undocumented precondition: `root` must
    // already be realpath'd. Its only caller satisfies it — mount.ts:195 does
    // `realpathSync(resolvedMountDir)` before building the context — so passing
    // an unresolved root is not a production path. Asserted the way the caller
    // actually uses it; worth a doc comment on the function so the next caller
    // does not discover the precondition the hard way.
    const s = sandbox();
    const linked = path.join(s.base, 'mount-link');
    symlinkSync(s.mount, linked);
    const rootAsCallerPassesIt = realpathSync(linked);
    const target = resolveSafeWriteTarget(rootAsCallerPassesIt, path.join(rootAsCallerPassesIt, 'ok.txt'));
    expect(target, 'a symlinked mount root must remain usable').not.toBeNull();
    if (target) {
      copyFileSync(s.source, target);
      expect(existsSync(path.join(s.mount, 'ok.txt'))).toBe(true);
    }
  });
});
