import { utimesSync } from 'node:fs';
import type { Stats } from 'node:fs';

/**
 * Window inside which two mtimes are considered "the same write".
 *
 * Mount population preserves source mtimes onto copies via utimes, which
 * round-trips through fractional-second floats with sub-microsecond error —
 * and Node's own `cpSync({ preserveTimestamps: true })` truncates to whole
 * milliseconds. 2ms absorbs both while staying far below the pace at which
 * real writes to the same path land.
 */
export const MTIME_QUICK_EQUAL_TOLERANCE_MS = 2;

/**
 * Files written within this window are never quick-checked: a just-written
 * file is exactly the case where a same-size rewrite could land inside the
 * mtime tolerance and masquerade as unchanged. Stale mtimes (anything older
 * than a few seconds) can only be within tolerance of each other because one
 * side is a preserved-mtime copy of the other.
 */
export const RECENT_WRITE_GUARD_MS = 5_000;

/**
 * rsync-style quick check: equal sizes plus (near-)equal mtimes imply equal
 * content, letting sync paths skip a full byte comparison. Only meaningful
 * for regular files whose copies were made with mtime preservation; callers
 * fall back to byte comparison when this returns false.
 *
 * Guarded two ways: mtimes must agree within the tolerance (absorbing utimes
 * float round-trips and `cpSync({ preserveTimestamps })` millisecond
 * truncation), and neither side may have been written inside the recency
 * window — fresh writes always take the byte-comparison path.
 */
export function statsImplySameContent(a: Stats, b: Stats): boolean {
  if (a.size !== b.size) return false;
  if (Math.abs(a.mtimeMs - b.mtimeMs) > MTIME_QUICK_EQUAL_TOLERANCE_MS) return false;
  const newest = Math.max(a.mtimeMs, b.mtimeMs);
  return Date.now() - newest > RECENT_WRITE_GUARD_MS;
}

/**
 * Carry a source file's mtime onto its copy so the two sides stat as "the
 * same write" and the quick check applies. Best-effort: when it fails, the
 * quick check simply degrades to a byte comparison.
 */
export function preserveMtime(targetPath: string, sourceStat: Stats): void {
  try {
    utimesSync(targetPath, sourceStat.atimeMs / 1000, sourceStat.mtimeMs / 1000);
  } catch {
    /* quick check falls back to content comparison */
  }
}
