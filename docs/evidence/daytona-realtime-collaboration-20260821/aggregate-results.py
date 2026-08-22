#!/usr/bin/env python3
"""Apply every acceptance gate across the three clean Daytona runs."""

import json
import os
import sys


def load(path):
    with open(path) as handle:
        return json.load(handle)


def percentile(values, fraction):
    values = sorted(values)
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def distribution(values):
    return {
        "samples": len(values),
        "p50_ms": percentile(values, 0.50),
        "p95_ms": percentile(values, 0.95),
        "p99_ms": percentile(values, 0.99),
        "max_ms": max(values),
    }


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: aggregate-results.py EVIDENCE_ROOT OUTPUT")
    root, output = sys.argv[1:]
    rows = []
    pooled_small = []
    pooled_repo = []
    # clean-r2 is intentionally excluded: an invalid expected digest made its
    # first concurrency watcher incapable of producing a sample. The strict
    # consecutive acceptance sequence therefore starts after that invalid run.
    for number in (3, 4, 5):
        run_id = f"clean-r{number}"
        run_dir = os.path.join(root, "raw", run_id)
        latency = load(os.path.join(run_dir, "combined-summary.json"))
        concurrency = load(os.path.join(run_dir, "concurrency-summary.json"))
        validation = load(os.path.join(run_dir, "validation.json"))
        for direction in ("a2b", "b2a"):
            for shape, destination in (("small", pooled_small), ("repo", pooled_repo)):
                detail = load(os.path.join(run_dir, f"{direction}-{shape}-summary.json"))
                destination.extend(row["save_to_visible_ms"] for row in detail["per_trial"])
        small = latency["small_300_bytes"]
        repo = latency["repo_save_11_files_13992_bytes"]
        integrity = latency["integrity"]
        small_pass = all(
            small[direction]["samples"] == 100
            and small[direction]["p95_ms"] <= 500
            and small[direction]["p99_ms"] <= 1000
            and small[direction]["max_ms"] <= 2000
            for direction in ("a_to_b", "b_to_a")
        )
        repo_pass = all(
            repo[direction]["samples"] == 30
            and repo[direction]["p95_ms"] <= 2000
            and repo[direction]["max_ms"] <= 5000
            for direction in ("a_to_b", "b_to_a")
        )
        integrity_pass = integrity == {
            "trials_sent": 260,
            "trials_complete": 260,
            "incomplete": 0,
            "ambiguous": 0,
            "clock_negative": 0,
            "extrapolated": 0,
        }
        passed = small_pass and repo_pass and integrity_pass and concurrency["pass"] and validation["status"] == "pass"
        rows.append(
            {
                "run_id": run_id,
                "small_300_bytes": {
                    direction: {key: small[direction][key] for key in ("p50_ms", "p95_ms", "p99_ms", "max_ms")}
                    for direction in ("a_to_b", "b_to_a")
                },
                "repo_save_11_files_13992_bytes": {
                    direction: {key: repo[direction][key] for key in ("p50_ms", "p95_ms", "max_ms")}
                    for direction in ("a_to_b", "b_to_a")
                },
                "disjoint_max_ms": concurrency["disjoint"]["max_ms"],
                "same_path_loser_preserved": concurrency["same_path"]["loser_preserved_exactly_once"],
                "saves_verified": validation["total_saves"],
                "content_hashes_verified": validation["total_content_hashes_verified"],
                "pass": passed,
            }
        )

    directions = ("a_to_b", "b_to_a")
    consecutive_clean_runs = 0
    for row in rows:
        consecutive_clean_runs = consecutive_clean_runs + 1 if row["pass"] else 0
    result = {
        "status": "pass" if all(row["pass"] for row in rows) else "fail",
        "consecutive_clean_runs": consecutive_clean_runs,
        "runs": rows,
        "cross_run": {
            "total_saves_verified": sum(row["saves_verified"] for row in rows),
            "total_content_hashes_verified": sum(row["content_hashes_verified"] for row in rows),
            "small_worst_directional_p95_ms": max(
                row["small_300_bytes"][direction]["p95_ms"] for row in rows for direction in directions
            ),
            "small_worst_directional_p99_ms": max(
                row["small_300_bytes"][direction]["p99_ms"] for row in rows for direction in directions
            ),
            "small_worst_max_ms": max(
                row["small_300_bytes"][direction]["max_ms"] for row in rows for direction in directions
            ),
            "repo_worst_directional_p95_ms": max(
                row["repo_save_11_files_13992_bytes"][direction]["p95_ms"]
                for row in rows
                for direction in directions
            ),
            "repo_worst_max_ms": max(
                row["repo_save_11_files_13992_bytes"][direction]["max_ms"]
                for row in rows
                for direction in directions
            ),
            "disjoint_worst_max_ms": max(row["disjoint_max_ms"] for row in rows),
            "same_path_runs_preserved": sum(row["same_path_loser_preserved"] for row in rows),
            "pooled_all_runs": {
                "small_300_bytes": distribution(pooled_small),
                "repo_save_11_files_13992_bytes": distribution(pooled_repo),
            },
        },
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print(json.dumps(result))
    if result["status"] != "pass" or result["consecutive_clean_runs"] != 3:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
