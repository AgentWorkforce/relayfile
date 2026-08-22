#!/usr/bin/env python3
"""Record first visibility of unique benchmark paths in a local mirror."""

import json
import os
import sys
import time


def percentile(values, fraction):
    if not values:
        return None
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def scan(root, seen, output):
    started = time.monotonic_ns()
    try:
        for current, directories, files in os.walk(root):
            directories[:] = [name for name in directories if name != ".relay"]
            for name in files:
                if ".tmp-" in name or name.endswith(".tmp") or name.endswith(".writer-tmp"):
                    continue
                path = os.path.join(current, name)
                if path in seen:
                    continue
                try:
                    with open(path, "rb") as handle:
                        observed_ns = time.time_ns()
                        content = handle.read()
                except OSError:
                    continue
                seen.add(path)
                output.write(
                    json.dumps(
                        {
                            "path": "/" + os.path.relpath(path, root).replace(os.sep, "/"),
                            "observed_ns": observed_ns,
                            "size": len(content),
                            "content_hash": __import__("hashlib").sha256(content).hexdigest(),
                            "clock": "CLOCK_REALTIME",
                        }
                    )
                    + "\n"
                )
                output.flush()
    except OSError:
        pass
    return (time.monotonic_ns() - started) / 1e6


def main():
    if len(sys.argv) not in {5, 6, 7}:
        raise SystemExit("usage: mirror-watch.py ROOT OUTPUT DURATION_S POLL_S [HOST] [STOP_FILE]")
    root, output_path, duration, poll = sys.argv[1:5]
    host = sys.argv[5] if len(sys.argv) == 6 else "receiver"
    if len(sys.argv) >= 6:
        host = sys.argv[5]
    stop_file = sys.argv[6] if len(sys.argv) == 7 else ""
    duration, poll = float(duration), float(poll)
    os.makedirs(root, exist_ok=True)
    seen, scans = set(), []
    # Existing repository contents are baseline state, not arrivals. Prime the
    # set before the timed watcher starts so the raw file contains only changes
    # made during this run.
    with open(os.devnull, "w") as sink:
        scan(root, seen, sink)
    with open(output_path, "a", buffering=1) as output:
        output.write(
            json.dumps(
                {
                    "kind": "watcher_started",
                    "host": host,
                    "primed_paths": len(seen),
                    "started_ns": time.time_ns(),
                }
            )
            + "\n"
        )
        started = time.monotonic()
        while time.monotonic() - started < duration:
            if stop_file and os.path.exists(stop_file):
                scans.append(scan(root, seen, output))
                break
            scans.append(scan(root, seen, output))
            time.sleep(poll)
        scans.sort()
        output.write(
            json.dumps(
                {
                    "kind": "watcher_finished",
                    "host": host,
                    "observations": len(seen),
                    "scans": len(scans),
                    "scan_ms_median": percentile(scans, 0.50),
                    "scan_ms_p95": percentile(scans, 0.95),
                    "scan_ms_max": scans[-1] if scans else None,
                    "finished_ns": time.time_ns(),
                }
            )
            + "\n"
        )


if __name__ == "__main__":
    main()
