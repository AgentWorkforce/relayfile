#!/usr/bin/env python3
"""Aggregate the three qualifying runs without averaging percentiles."""

import argparse
import glob
import json
import os

from orchestrate import distribution


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence-root", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    run_names = ("large-r1", "large-r2", "large-r3")
    run_summaries = []
    results = []
    for run_name in run_names:
        run_root = os.path.join(args.evidence_root, "raw", run_name)
        with open(os.path.join(run_root, "summary.json")) as handle:
            summary = json.load(handle)
        run_summaries.append(
            {
                "run_id": run_name,
                "status": summary["status"],
                "saves": summary["saves"],
                "pairwise_deliveries": summary["pairwise_deliveries"],
                "content_hashes_verified": summary["content_hashes_verified"],
                "small_pairwise_p95_ms": summary["shapes"]["small"]["pairwise_deliveries"]["p95_ms"],
                "small_all_peer_p95_ms": summary["shapes"]["small"]["all_peer_convergence"]["p95_ms"],
                "repo_pairwise_p95_ms": summary["shapes"]["repo"]["pairwise_deliveries"]["p95_ms"],
                "repo_all_peer_p95_ms": summary["shapes"]["repo"]["all_peer_convergence"]["p95_ms"],
            }
        )
        for path in glob.glob(os.path.join(run_root, "raw", "*.json")):
            if path.endswith("-failure.json"):
                continue
            with open(path) as handle:
                result = json.load(handle)
            if "role" in result and "receivers" in result:
                results.append(result)

    shapes = {}
    transport_errors = 0
    control_plane_retries = 0
    for shape in ("small", "repo"):
        selected = [item for item in results if item["shape"] == shape]
        pairwise = [receiver["latency_ms"] for item in selected for receiver in item["receivers"]]
        all_peer = [item["all_peer_latency_ms"] for item in selected]
        local_write = [item["local_write_ms"] for item in selected]
        shapes[shape] = {
            "saves": len(selected),
            "pairwise_deliveries": distribution(pairwise),
            "all_peer_convergence": distribution(all_peer),
            "local_write": distribution(local_write),
        }
        transport_errors += sum(
            receiver["transport_errors"] for item in selected for receiver in item["receivers"]
        )
        control_plane_retries += sum(
            max(0, item["execution"].get("control_plane_attempts", 1) - 1) for item in selected
        )

    aggregate = {
        "qualifying_runs": run_summaries,
        "runs_passed": sum(1 for item in run_summaries if item["status"] == "pass"),
        "saves": sum(item["saves"] for item in run_summaries),
        "pairwise_deliveries": sum(item["pairwise_deliveries"] for item in run_summaries),
        "content_hashes_verified": sum(item["content_hashes_verified"] for item in run_summaries),
        "probe_transport_errors": transport_errors,
        "pre_execution_control_plane_retries": control_plane_retries,
        "shapes": shapes,
    }
    aggregate["status"] = "pass" if (
        len(results) == 900
        and aggregate["runs_passed"] == 3
        and aggregate["pairwise_deliveries"] == 3_600
        and aggregate["content_hashes_verified"] == 9_600
    ) else "fail"
    with open(args.output, "w") as handle:
        json.dump(aggregate, handle, indent=2)
        handle.write("\n")
    print(json.dumps(aggregate), flush=True)
    if aggregate["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
