#!/usr/bin/env python3
"""Atomically write deterministic content at a shared wall-clock deadline."""

import hashlib
import json
import os
import sys
import time


def main():
    if len(sys.argv) != 6:
        raise SystemExit(
            "usage: scheduled-write.py MOUNT RELATIVE_PATH CONTENT ROLE TARGET_NS"
        )
    mount, relative_path, content, role, target_raw = sys.argv[1:]
    if relative_path.startswith("/") or ".." in relative_path.split("/"):
        raise SystemExit("relative path must stay within the mount")
    target_ns = int(target_raw)
    destination = os.path.join(mount, relative_path)
    os.makedirs(os.path.dirname(destination), exist_ok=True)
    while True:
        remaining_ns = target_ns - time.time_ns()
        if remaining_ns <= 0:
            break
        time.sleep(min(remaining_ns / 1e9, 0.005))
    started_ns = time.time_ns()
    temporary = f"{destination}.scheduled-{role}-{os.getpid()}"
    payload = content.encode()
    with open(temporary, "wb") as handle:
        handle.write(payload)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, destination)
    completed_ns = time.time_ns()
    print(
        json.dumps(
            {
                "role": role,
                "relative_path": relative_path,
                "target_ns": target_ns,
                "write_started_ns": started_ns,
                "write_completed_ns": completed_ns,
                "target_error_ms": (started_ns - target_ns) / 1e6,
                "write_ms": (completed_ns - started_ns) / 1e6,
                "sha256": hashlib.sha256(payload).hexdigest(),
            }
        ),
        flush=True,
    )


if __name__ == "__main__":
    main()
