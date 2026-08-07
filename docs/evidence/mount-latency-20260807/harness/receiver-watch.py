#!/usr/bin/env python3
"""In-box resident watcher: records file arrival times on the RECEIVER host.

Runs on the receiver, inside the box, and timestamps with the receiver's own
`CLOCK_REALTIME` (`time.time_ns()`). Nothing is timestamped over ssh, because
an ssh round trip would be added to every sample and would dwarf the signal.

The mount is a synced mirror of real local files (poll mode writes bytes to
disk via `writeFileAtomic`, `internal/mountsync/syncer.go:6033-6051`), so each
probe below is a purely local syscall with no network hop. That is what makes
an in-box watcher a measure of propagation rather than of its own fetch.

Because the daemon publishes each file with an atomic rename, a path becoming
visible already has its complete content -- there is no torn-read window. The
recorded size is emitted anyway so the analyser can assert it.

Output is one JSON line per newly observed file, appended and flushed
IMMEDIATELY so that a host or tool failure mid-run leaves usable raw evidence
rather than nothing.

Usage:
    receiver-watch.py WATCH_DIR OUTPUT_JSONL DURATION_SECONDS [POLL_SECONDS] [WATCH_ALIAS]
"""

import json
import os
import sys
import time


def percentile(values, fraction):
    """Linear-interpolated percentile over an already sorted list."""
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def scan(root, seen, output):
    """Record every path under `root` not already in `seen`."""
    stack = [root]
    while stack:
        current = stack.pop()
        try:
            with os.scandir(current) as entries:
                for entry in entries:
                    try:
                        if entry.is_dir(follow_symlinks=False):
                            # .relay holds mount bookkeeping, not workspace
                            # content; watching it would time our own daemon's
                            # state writes instead of the payload.
                            if entry.name != ".relay":
                                stack.append(entry.path)
                            continue
                        if entry.path in seen:
                            continue
                        try:
                            observed_ns = time.time_ns()
                            size = entry.stat(follow_symlinks=False).st_size
                        except OSError:
                            # Do not mark the path seen: a rename can make a
                            # directory entry vanish between enumeration and
                            # stat, and the next scan must be allowed to retry.
                            continue
                        seen.add(entry.path)
                        record = {
                            "path": os.path.relpath(entry.path, root),
                            "observed_ns": observed_ns,
                            "size": size,
                            "host": "receiver",
                            "clock": "CLOCK_REALTIME",
                        }
                        output.write(json.dumps(record) + "\n")
                        output.flush()
                    except OSError:
                        # A file can vanish or be mid-rename between the
                        # directory listing and the stat; skip and re-see it
                        # on the next pass.
                        continue
        except OSError:
            continue


def main():
    watch_dir = sys.argv[1]
    output_path = sys.argv[2]
    duration = float(sys.argv[3])
    poll_seconds = float(sys.argv[4]) if len(sys.argv) > 4 else 0.001
    watch_alias = sys.argv[5] if len(sys.argv) > 5 else "receiver-watch-root"

    os.makedirs(watch_dir, exist_ok=True)
    seen = set()
    loop_periods = []

    # Prime `seen` with whatever already exists, so pre-existing content is not
    # reported as an arrival.
    with open(os.devnull, "w") as sink:
        scan(watch_dir, seen, sink)

    started = time.monotonic()
    with open(output_path, "a") as output:
        output.write(
            json.dumps(
                {
                    "kind": "watcher_started",
                    "watch_dir": watch_alias,
                    "poll_seconds": poll_seconds,
                    "primed_paths": len(seen),
                    "started_ns": time.time_ns(),
                }
            )
            + "\n"
        )
        output.flush()

        while time.monotonic() - started < duration:
            loop_start = time.monotonic()
            scan(watch_dir, seen, output)
            loop_periods.append(time.monotonic() - loop_start)
            time.sleep(poll_seconds)

        # The scan cost bounds detection granularity; report it rather than
        # assume the poll interval alone is the quantisation.
        loop_periods.sort()
        output.write(
            json.dumps(
                {
                    "kind": "watcher_finished",
                    "scans": len(loop_periods),
                    "scan_ms_median": percentile(loop_periods, 0.50) * 1e3
                    if loop_periods else None,
                    "scan_ms_p95": percentile(loop_periods, 0.95) * 1e3
                    if loop_periods else None,
                    "scan_ms_max": loop_periods[-1] * 1e3 if loop_periods else None,
                    "finished_ns": time.time_ns(),
                }
            )
            + "\n"
        )
        output.flush()


if __name__ == "__main__":
    main()
