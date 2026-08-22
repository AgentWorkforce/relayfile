#!/usr/bin/env python3
"""Prepare bytes before a barrier, then race only the final atomic renames."""

import argparse
import hashlib
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
from fanout_trial import JSONConnection  # noqa: E402


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--barrier-url", required=True)
    parser.add_argument("--parties", type=int, default=2)
    parser.add_argument("--barrier-timeout-s", type=float, default=120)
    args = parser.parse_args()

    relative = f"testdata/cross-provider/{args.run_id}/conflict/shared.txt"
    content = f"{args.run_id}-agent-{args.role}-conflict".encode()
    destination = os.path.join(args.root, relative)
    os.makedirs(os.path.dirname(destination), exist_ok=True)
    temporary = f"{destination}.writer-tmp-{os.getpid()}"
    with open(temporary, "wb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())

    barrier = JSONConnection(args.barrier_url, timeout_s=args.barrier_timeout_s + 10)
    try:
        release = barrier.request(
            "POST",
            "/barrier",
            {
                "key": f"{args.run_id}:atomic-commit",
                "role": args.role,
                "parties": args.parties,
                "timeout_s": args.barrier_timeout_s,
            },
        )
    finally:
        barrier.close()

    started_ns = time.time_ns()
    os.replace(temporary, destination)
    completed_ns = time.time_ns()
    print(
        json.dumps(
            {
                "run_id": args.run_id,
                "role": args.role,
                "relative_path": relative,
                "content": content.decode(),
                "sha256": hashlib.sha256(content).hexdigest(),
                "barrier_released_ns": release["released_ns"],
                "rename_started_ns": started_ns,
                "rename_completed_ns": completed_ns,
                "atomic_rename_ms": (completed_ns - started_ns) / 1e6,
            },
            separators=(",", ":"),
        ),
        flush=True,
    )


if __name__ == "__main__":
    main()
