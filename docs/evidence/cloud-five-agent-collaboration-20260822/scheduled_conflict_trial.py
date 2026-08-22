#!/usr/bin/env python3
"""Clock-schedule one same-path contender inside a mounted sandbox."""

import argparse
import hashlib
import json
import os
import time

from scheduled_fanout_trial import calibrate_clock, corrected_epoch_ms, wait_until


def atomic_save(root, relative, content):
    destination = os.path.join(root, relative)
    os.makedirs(os.path.dirname(destination), exist_ok=True)
    temporary = f"{destination}.writer-tmp-{os.getpid()}"
    started_ns = time.time_ns()
    with open(temporary, "wb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, destination)
    completed_ns = time.time_ns()
    return started_ns, completed_ns


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--release-epoch-ms", type=float, required=True)
    parser.add_argument("--clock-url", required=True)
    args = parser.parse_args()

    relative = f"testdata/cloud-five-agent/{args.run_id}/conflict/shared.txt"
    content = f"{args.run_id}-agent-{args.role}-conflict".encode()
    pre = calibrate_clock(args.clock_url)
    ready_epoch_ms = corrected_epoch_ms(time.time_ns(), pre["offset_ms"])
    if ready_epoch_ms > args.release_epoch_ms:
        raise RuntimeError(
            f"agent started {ready_epoch_ms - args.release_epoch_ms:.3f} ms after release"
        )
    wait_until(args.release_epoch_ms, pre["offset_ms"])
    started_ns, completed_ns = atomic_save(args.root, relative, content)
    post = calibrate_clock(args.clock_url)
    offset_ms = (pre["offset_ms"] + post["offset_ms"]) / 2
    print(
        json.dumps(
            {
                "run_id": args.run_id,
                "role": args.role,
                "relative_path": relative,
                "content": content.decode(),
                "sha256": hashlib.sha256(content).hexdigest(),
                "ready_lead_ms": args.release_epoch_ms - ready_epoch_ms,
                "write_started_epoch_ms": corrected_epoch_ms(started_ns, offset_ms),
                "write_completed_epoch_ms": corrected_epoch_ms(
                    completed_ns, offset_ms
                ),
                "local_write_ms": (completed_ns - started_ns) / 1e6,
                "clock": {
                    "pre": pre,
                    "post": post,
                    "offset_delta_ms": post["offset_ms"] - pre["offset_ms"],
                    "uncertainty_ms": max(
                        pre["uncertainty_ms"], post["uncertainty_ms"]
                    ),
                },
            },
            separators=(",", ":"),
        ),
        flush=True,
    )


if __name__ == "__main__":
    main()
