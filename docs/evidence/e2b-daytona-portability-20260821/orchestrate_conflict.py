#!/usr/bin/env python3
"""Drive and retain one E2B/Daytona atomic same-path collision."""

import argparse
import concurrent.futures
import json
import os
import subprocess
import time


def run(command, timeout=180):
    completed = subprocess.run(command, text=True, capture_output=True, timeout=timeout)
    start = completed.stdout.find("{")
    end = completed.stdout.rfind("}")
    encoded = completed.stdout[start : end + 1] if start >= 0 and end >= start else ""
    return {
        "returncode": completed.returncode,
        "payload": json.loads(encoded) if encoded else None,
        "stderr_tail": completed.stderr[-1000:],
    }


def write_json(path, value):
    with open(path, "w") as handle:
        json.dump(value, handle, indent=2)
        handle.write("\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--e2b-id", required=True)
    parser.add_argument("--daytona-id", required=True)
    parser.add_argument("--barrier-url", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--e2b-root", default="/home/user/shared-repo")
    parser.add_argument("--daytona-root", default="/home/daytona/shared-repo")
    args = parser.parse_args()
    os.makedirs(args.output, exist_ok=True)

    relative = f"testdata/cross-provider/{args.run_id}/conflict/shared.txt"
    commands = {
        "e2b": [
            "e2b",
            "sandbox",
            "exec",
            args.e2b_id,
            "--",
            "python3",
            "/home/user/benchmark/conflict_atomic_commit.py",
            "--root",
            args.e2b_root,
            "--run-id",
            args.run_id,
            "--role",
            "e2b",
            "--barrier-url",
            args.barrier_url,
        ],
        "daytona": [
            "daytona",
            "sandbox",
            "exec",
            args.daytona_id,
            "--",
            "python3",
            "/home/daytona/benchmark/conflict_atomic_commit.py",
            "--root",
            args.daytona_root,
            "--run-id",
            args.run_id,
            "--role",
            "daytona",
            "--barrier-url",
            args.barrier_url,
        ],
    }
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        futures = {role: pool.submit(run, command) for role, command in commands.items()}
        writers = {role: future.result() for role, future in futures.items()}
    for role, result in writers.items():
        write_json(os.path.join(args.output, f"writer-{role}.json"), result)

    time.sleep(3)
    capture_commands = {
        "e2b": [
            "e2b",
            "sandbox",
            "exec",
            args.e2b_id,
            "--",
            "sh",
            "-c",
            "python3 /home/user/benchmark/capture_conflict.py "
            f"{args.e2b_root} {relative} e2b "
            "/home/user/results/certification-conflict-e2b.json && "
            "cat /home/user/results/certification-conflict-e2b.json",
        ],
        "daytona": [
            "daytona",
            "sandbox",
            "exec",
            args.daytona_id,
            "--",
            "sh",
            "-c",
            "python3 /home/daytona/benchmark/capture_conflict.py "
            f"{args.daytona_root} {relative} daytona "
            "/home/daytona/results/certification-conflict-daytona.json && "
            "cat /home/daytona/results/certification-conflict-daytona.json",
        ],
    }
    captures = {role: run(command) for role, command in capture_commands.items()}
    for role, result in captures.items():
        write_json(os.path.join(args.output, f"capture-{role}.json"), result)

    writer_payloads = [result["payload"] for result in writers.values()]
    capture_payloads = [result["payload"] for result in captures.values()]
    writer_hashes = {payload["sha256"] for payload in writer_payloads if payload}
    artifact_hashes = {
        artifact["sha256"]
        for payload in capture_payloads
        if payload
        for artifact in payload.get("artifacts", [])
    }
    canonical_hashes = {
        payload["canonical_sha256"] for payload in capture_payloads if payload
    }
    releases = {
        payload["barrier_released_ns"] for payload in writer_payloads if payload
    }
    summary = {
        "run_id": args.run_id,
        "writer_hashes": sorted(writer_hashes),
        "canonical_hashes": sorted(canonical_hashes),
        "artifact_hashes": sorted(artifact_hashes),
        "one_barrier_release": len(releases) == 1,
        "writers_succeeded": all(
            not result["returncode"] and result["payload"] for result in writers.values()
        ),
        "captures_succeeded": all(
            not result["returncode"] and result["payload"] for result in captures.values()
        ),
    }
    summary["canonical_converged"] = len(canonical_hashes) == 1
    summary["losing_bytes_preserved"] = bool(
        artifact_hashes and artifact_hashes == writer_hashes - canonical_hashes
    )
    summary["status"] = "pass" if all(
        (
            summary["writers_succeeded"],
            summary["captures_succeeded"],
            summary["one_barrier_release"],
            len(writer_hashes) == 2,
            summary["canonical_converged"],
            summary["losing_bytes_preserved"],
        )
    ) else "fail"
    write_json(os.path.join(args.output, "summary.json"), summary)
    print(json.dumps(summary, separators=(",", ":")))
    if summary["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
