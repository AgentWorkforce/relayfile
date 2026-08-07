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
import sys
import time


def main():
    control_dir, run_id, count, spacing, out_path = sys.argv[1:6]
    count = int(count)
    spacing = float(spacing)
    os.makedirs(control_dir, exist_ok=True)

    with open(out_path, "a") as raw:
        for trial in range(1, count + 1):
            directory = os.path.join(control_dir, f"control-{trial:03d}")
            os.makedirs(directory, exist_ok=True)
            final_path = os.path.join(directory, "probe.txt")
            temporary_path = final_path + ".tmp"
            content = (f"control={run_id}-{trial:03d} ").ljust(300, "x")

            with open(temporary_path, "w") as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())

            # The instant the path becomes visible, by the same mechanism the
            # mount daemon uses.
            t_create_ns = time.time_ns()
            os.rename(temporary_path, final_path)

            record = {
                "kind": "control_create",
                "run_id": run_id,
                "trial": trial,
                "path": os.path.relpath(final_path, control_dir),
                "t_create_ns": t_create_ns,
                "host": "sf-mini",
                "clock": "CLOCK_REALTIME",
            }
            raw.write(json.dumps(record) + "\n")
            raw.flush()
            if trial < count:
                time.sleep(spacing)


if __name__ == "__main__":
    main()
