#!/usr/bin/env python3
"""Control experiment: how much of the measured latency is the watcher itself?

Runs entirely on the RECEIVER host. It creates files locally -- same clock,
same watcher code, same filesystem, no network anywhere -- and records the
creation timestamp. Pairing those against the watcher's observation timestamps
gives the watcher's own detection delay distribution.

This is what turns "measurement overhead that exceeds the signal" from an
assumption into a measured quantity that can be subtracted or dismissed.

Files are published with an atomic rename, matching how the mount daemon
materialises remote content (`writeFileAtomic`, syncer.go:8285), so the
control exercises the same visibility transition the real trials do.

Usage:
    control-local.py CONTROL_DIR RUN_ID COUNT SPACING_SECONDS OUT_JSONL
"""

import json
import os
import re
import sys
import time


def main():
    control_dir, run_id, count, spacing, out_path = sys.argv[1:6]
    count = int(count)
    spacing = float(spacing)
    os.makedirs(control_dir, exist_ok=True)
    if not re.fullmatch(r"[A-Za-z0-9._-]+", run_id):
        raise SystemExit("RUN_ID must contain only letters, digits, '.', '_' or '-'")
    run_dir = os.path.join(control_dir, run_id)
    if os.path.isdir(run_dir) and os.listdir(run_dir):
        raise SystemExit(f"control run already exists: {run_id}")
    os.makedirs(run_dir, exist_ok=True)

    with open(out_path, "a") as raw:
        for trial in range(1, count + 1):
            directory = os.path.join(run_dir, f"control-{trial:03d}")
            os.makedirs(directory, exist_ok=True)
            final_path = os.path.join(directory, "probe.txt")
            temporary_path = final_path + ".tmp"
            content = (f"control={run_id}-{trial:03d} ").ljust(300, "x")

            with open(temporary_path, "w") as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())

            # Visibility occurs during this rename syscall, by the same
            # mechanism the mount daemon uses. Record both bounds.
            t_publish_start_ns = time.time_ns()
            os.rename(temporary_path, final_path)
            t_publish_end_ns = time.time_ns()

            record = {
                "kind": "control_create",
                "run_id": run_id,
                "trial": trial,
                "path": os.path.relpath(final_path, control_dir),
                # Visibility occurs within this interval. The analysis reports
                # an interval rather than pretending the rename is instant.
                "t_publish_start_ns": t_publish_start_ns,
                "t_publish_end_ns": t_publish_end_ns,
                "host": "receiver",
                "clock": "CLOCK_REALTIME",
            }
            raw.write(json.dumps(record) + "\n")
            raw.flush()
            if trial < count:
                time.sleep(spacing)


if __name__ == "__main__":
    main()
