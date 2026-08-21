#!/usr/bin/env python3
"""Measure same-host mount-to-mount replication without a network probe.

This harness is deliberately limited to a controlled core-path benchmark: the
sender and receiver roots must be visible to this process on one host. It times
from the sender's completed atomic rename to hash-correct bytes on the receiver
using CLOCK_MONOTONIC. Cross-provider results use one_way_trial.py instead.
"""

import argparse
import hashlib
import json
import os
import statistics
import time


SMALL_BYTES = 300


def percentile(values, fraction):
    ordered = sorted(values)
    position = fraction * (len(ordered) - 1)
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    weight = position - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def distribution(values):
    return {
        "count": len(values),
        "p50_ms": percentile(values, 0.50),
        "p95_ms": percentile(values, 0.95),
        "p99_ms": percentile(values, 0.99),
        "max_ms": max(values),
        "mean_ms": statistics.fmean(values),
    }


def atomic_save(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    temporary = f"{path}.writer-tmp-{os.getpid()}"
    started_ns = time.monotonic_ns()
    with open(temporary, "wb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, path)
    completed_ns = time.monotonic_ns()
    return started_ns, completed_ns


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sender-root", required=True)
    parser.add_argument("--receiver-root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--rounds", type=int, default=100)
    parser.add_argument("--poll-s", type=float, default=0.001)
    parser.add_argument("--timeout-s", type=float, default=5.0)
    parser.add_argument("--prepare-wait-s", type=float, default=0.25)
    args = parser.parse_args()

    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    # Pre-create the parent directories outside the timed region. This keeps
    # the benchmark focused on replication through already-established
    # filesystem watches; first discovery of a brand-new directory correctly
    # uses Relayfile's conservative stable-file path instead.
    run_directory = os.path.join(
        args.sender_root, "testdata", "core-replication", args.run_id
    )
    os.makedirs(run_directory, exist_ok=True)
    if args.prepare_wait_s > 0:
        time.sleep(args.prepare_wait_s)

    results = []
    with open(args.output, "w", encoding="utf-8") as raw:
        for trial in range(1, args.rounds + 1):
            relative = f"testdata/core-replication/{args.run_id}/probe-{trial:03d}.txt"
            sender = os.path.join(args.sender_root, relative)
            receiver = os.path.join(args.receiver_root, relative)
            content = f"run={args.run_id} trial={trial:03d} ".encode().ljust(
                SMALL_BYTES, b"x"
            )
            expected = hashlib.sha256(content).hexdigest()
            write_started_ns, write_completed_ns = atomic_save(sender, content)
            deadline = time.monotonic() + args.timeout_s
            attempts = 0
            visible_ns = None
            while time.monotonic() < deadline:
                attempts += 1
                try:
                    with open(receiver, "rb") as handle:
                        observed = hashlib.sha256(handle.read()).hexdigest()
                except OSError:
                    observed = ""
                if observed == expected:
                    visible_ns = time.monotonic_ns()
                    break
                time.sleep(args.poll_s)
            result = {
                "run_id": args.run_id,
                "trial": trial,
                "path": relative,
                "bytes": len(content),
                "sha256": expected,
                "attempts": attempts,
                "status": "visible" if visible_ns is not None else "timeout",
                "local_write_ms": (write_completed_ns - write_started_ns) / 1e6,
                "latency_ms": (
                    None
                    if visible_ns is None
                    else (visible_ns - write_completed_ns) / 1e6
                ),
                "clock": "sender CLOCK_MONOTONIC; same-host receiver read",
            }
            raw.write(json.dumps(result, separators=(",", ":")) + "\n")
            raw.flush()
            if visible_ns is None:
                raise SystemExit(1)
            results.append(result)

    visibility = distribution([item["latency_ms"] for item in results])
    local_write = distribution([item["local_write_ms"] for item in results])
    gates = {
        "all_hashes_visible": len(results) == args.rounds,
        "visibility_p95_le_9_ms": visibility["p95_ms"] <= 9.0,
    }
    summary = {
        "run_id": args.run_id,
        "saves": len(results),
        "shape": {"files": 1, "bytes": SMALL_BYTES},
        "visibility": visibility,
        "local_write": local_write,
        "gates": gates,
        "status": "pass" if all(gates.values()) else "fail",
        "scope": "controlled same-host core path; not cross-provider Internet latency",
    }
    summary_path = os.path.splitext(args.output)[0] + "-summary.json"
    with open(summary_path, "w", encoding="utf-8") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary, separators=(",", ":")), flush=True)
    if summary["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
