#!/usr/bin/env python3
"""Run one cross-provider direction repeatedly and summarize its latency."""

import argparse
import json
import os
import statistics
import subprocess
import sys


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


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--trial-script", required=True)
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set", required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--receiver-role", required=True)
    parser.add_argument("--receiver-url", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--small-rounds", type=int, default=50)
    parser.add_argument("--repo-rounds", type=int, default=10)
    args = parser.parse_args()

    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    results = []
    with open(args.output, "w") as raw:
        for shape, rounds in (("small", args.small_rounds), ("repo", args.repo_rounds)):
            for trial in range(1, rounds + 1):
                command = [
                    sys.executable,
                    args.trial_script,
                    "--root",
                    args.root,
                    "--run-id",
                    args.run_id,
                    "--path-set",
                    args.path_set,
                    "--role",
                    args.role,
                    "--receiver-role",
                    args.receiver_role,
                    "--receiver-url",
                    args.receiver_url,
                    "--shape",
                    shape,
                    "--trial",
                    str(trial),
                ]
                completed = subprocess.run(command, text=True, capture_output=True)
                line = next(
                    (
                        candidate
                        for candidate in reversed(completed.stdout.splitlines())
                        if candidate.strip().startswith("{")
                    ),
                    "",
                )
                if not line:
                    failure = {
                        "shape": shape,
                        "trial": trial,
                        "returncode": completed.returncode,
                        "stderr": completed.stderr[-1000:],
                    }
                    raw.write(json.dumps(failure, separators=(",", ":")) + "\n")
                    raw.flush()
                    print(json.dumps(failure), flush=True)
                    raise SystemExit(1)
                result = json.loads(line)
                raw.write(json.dumps(result, separators=(",", ":")) + "\n")
                raw.flush()
                print(
                    json.dumps(
                        {
                            "shape": shape,
                            "trial": trial,
                            "latency_ms": result.get("latency_ms"),
                            "status": result.get("status"),
                        },
                        separators=(",", ":"),
                    ),
                    flush=True,
                )
                if completed.returncode or result.get("status") != "visible":
                    raise SystemExit(1)
                results.append(result)

    shapes = {}
    for shape in ("small", "repo"):
        selected = [item for item in results if item["shape"] == shape]
        shapes[shape] = {
            "saves": len(selected),
            "visibility": distribution([item["latency_ms"] for item in selected]),
            "local_write": distribution([item["local_write_ms"] for item in selected]),
            "transport_errors": sum(item["transport_errors"] for item in selected),
        }
    expected_saves = args.small_rounds + args.repo_rounds
    gates = {
        "all_saves_visible": len(results) == expected_saves,
        "small_p95_le_1500_ms": shapes["small"]["visibility"]["p95_ms"] <= 1500,
        "repo_p95_le_4000_ms": shapes["repo"]["visibility"]["p95_ms"] <= 4000,
    }
    summary = {
        "run_id": args.run_id,
        "path_set": args.path_set,
        "role": args.role,
        "receiver": args.receiver_role,
        "saves": len(results),
        "shapes": shapes,
        "gates": gates,
        "status": "pass" if all(gates.values()) else "fail",
    }
    summary_path = os.path.splitext(args.output)[0] + "-summary.json"
    with open(summary_path, "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary, separators=(",", ":")), flush=True)
    if summary["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
