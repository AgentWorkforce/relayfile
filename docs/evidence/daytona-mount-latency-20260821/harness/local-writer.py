#!/usr/bin/env python3
"""Generate timestamped local saves inside a Relayfile materialized mount."""

import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request

SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000


def atomic_write(path, content):
    tmp = os.path.join(os.path.dirname(path), "." + os.path.basename(path) + ".writer-tmp")
    with open(tmp, "wb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(tmp, path)


def trial_files(shape, run_id, direction, trial):
    root = f"testdata/daytona-sync-benchmark/{run_id}/{direction}/{shape}-{trial:03d}"
    if shape == "small":
        relative = f"{root}/probe.txt"
        content = f"run={run_id} direction={direction} trial={trial:03d} ".encode()
        return [(relative, content.ljust(SMALL_BYTES, b"x"))]
    if shape != "repo":
        raise ValueError("shape must be small or repo")
    per_file = REPO_TOTAL_BYTES // REPO_FILE_COUNT
    result = []
    for index in range(REPO_FILE_COUNT):
        relative = f"{root}/src/module_{index:02d}.go"
        header = (
            f"// run={run_id} direction={direction} trial={trial:03d} file={index:02d}\n"
            "package benchmark\n"
        ).encode()
        result.append((relative, (header + b"// deterministic filler\n" * 1000)[:per_file]))
    return result


def main():
    if len(sys.argv) not in {9, 10, 11}:
        raise SystemExit(
            "usage: local-writer.py MOUNT SHAPE RUN_ID DIRECTION COUNT SPACING_S OUTPUT HOST [RECEIVER_URL] [START_TRIAL]"
        )
    mount, shape, run_id, direction, count, spacing, output, host = sys.argv[1:9]
    receiver_url = sys.argv[9].rstrip("/") if len(sys.argv) == 10 else ""
    if len(sys.argv) >= 10:
        receiver_url = sys.argv[9].rstrip("/")
    start_trial = int(sys.argv[10]) if len(sys.argv) == 11 else 1
    if not re.fullmatch(r"[A-Za-z0-9._-]+", run_id):
        raise SystemExit("unsafe run id")
    if not re.fullmatch(r"[A-Za-z0-9._-]+", direction):
        raise SystemExit("unsafe direction")
    count, spacing = int(count), float(spacing)
    with open(output, "a", buffering=1) as raw:
        for trial in range(start_trial, start_trial + count):
            files = trial_files(shape, run_id, direction, trial)
            for relative, _ in files:
                os.makedirs(os.path.dirname(os.path.join(mount, relative)), exist_ok=True)
            started_ns = time.time_ns()
            for relative, content in files:
                atomic_write(os.path.join(mount, relative), content)
            completed_ns = time.time_ns()
            record = {
                "kind": "local_save",
                "shape": shape,
                "run_id": run_id,
                "direction": direction,
                "trial": trial,
                "correlation_id": f"{run_id}-{direction}-{shape}-{trial:03d}",
                "paths": ["/" + relative for relative, _ in files],
                "expected_bytes": sum(len(content) for _, content in files),
                "write_started_ns": started_ns,
                "write_completed_ns": completed_ns,
                "local_write_ms": (completed_ns - started_ns) / 1e6,
                "host": host,
                "clock": "CLOCK_REALTIME",
            }
            raw.write(json.dumps(record) + "\n")
            if receiver_url:
                deadline = time.monotonic() + 120
                while True:
                    all_visible = True
                    for path, content in [("/" + relative, content) for relative, content in files]:
                        url = receiver_url + "/probe?path=" + urllib.parse.quote(path, safe="")
                        try:
                            with urllib.request.urlopen(url, timeout=10) as response:
                                probe = json.load(response)
                            if not probe.get("exists") or probe.get("size") != len(content):
                                all_visible = False
                                break
                        except Exception:
                            all_visible = False
                            break
                    if all_visible:
                        break
                    if time.monotonic() >= deadline:
                        raise TimeoutError(f"receiver did not expose {record['correlation_id']} within 30s")
                    time.sleep(0.02)
            print(
                f"{direction} {shape} {trial}/{count} local_write={record['local_write_ms']:.3f}ms",
                flush=True,
            )
            if trial < count:
                time.sleep(spacing)


if __name__ == "__main__":
    main()
