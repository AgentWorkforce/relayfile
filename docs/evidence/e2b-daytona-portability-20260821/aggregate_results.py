#!/usr/bin/env python3
"""Validate and aggregate the frozen E2B/Daytona acceptance evidence."""

import glob
import json
import os
import statistics


ROOT = os.path.dirname(os.path.abspath(__file__))


def read_json(path):
    with open(path) as handle:
        return json.load(handle)


def read_jsonl(path):
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


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


def aggregate_cross_provider_runs(path_pattern, suite_name):
    paths = sorted(glob.glob(os.path.join(ROOT, path_pattern)))
    if len(paths) != 6:
        raise SystemExit(
            f"{suite_name}: expected six qualifying direction files, found {len(paths)}"
        )

    directions = {"e2b-to-daytona": [], "daytona-to-e2b": []}
    run_summaries = []
    for path in paths:
        direction = os.path.basename(os.path.dirname(path))
        samples = read_jsonl(path)
        if direction not in directions:
            raise SystemExit(f"{suite_name}: unexpected direction: {direction}")
        if len(samples) != 60:
            raise SystemExit(
                f"{suite_name}: {path}: expected 60 samples, found {len(samples)}"
            )
        if sum(item["shape"] == "small" for item in samples) != 50:
            raise SystemExit(f"{suite_name}: {path}: small sample count changed")
        if sum(item["shape"] == "repo" for item in samples) != 10:
            raise SystemExit(f"{suite_name}: {path}: repository sample count changed")
        if any(item["status"] != "visible" or item["latency_ms"] < 0 for item in samples):
            raise SystemExit(
                f"{suite_name}: {path}: missing, timed-out, or negative sample"
            )
        directions[direction].extend(samples)
        summary = read_json(os.path.join(os.path.dirname(path), "summary.json"))
        if summary["status"] != "pass" or not all(summary["gates"].values()):
            raise SystemExit(f"{suite_name}: {path}: run summary failed")
        run_summaries.append(summary)

    aggregates = {}
    all_samples = []
    for direction, samples in directions.items():
        if len(samples) != 180:
            raise SystemExit(f"{suite_name}: {direction}: expected 180 pooled samples")
        all_samples.extend(samples)
        aggregates[direction] = {}
        for shape in ("small", "repo"):
            selected = [item for item in samples if item["shape"] == shape]
            aggregates[direction][shape] = {
                "visibility": distribution([item["latency_ms"] for item in selected]),
                "local_write": distribution([item["local_write_ms"] for item in selected]),
                "transport_errors": sum(item["transport_errors"] for item in selected),
                "hashes_verified": sum(item["expected_hashes"] for item in selected),
            }

    pooled = {}
    for shape in ("small", "repo"):
        selected = [item for item in all_samples if item["shape"] == shape]
        pooled[shape] = {
            "visibility": distribution([item["latency_ms"] for item in selected]),
            "local_write": distribution([item["local_write_ms"] for item in selected]),
            "transport_errors": sum(item["transport_errors"] for item in selected),
            "hashes_verified": sum(item["expected_hashes"] for item in selected),
        }

    return {
        "qualifying_runs": len(run_summaries) // 2,
        "direction_runs": len(run_summaries),
        "directions": list(directions),
        "saves": len(all_samples),
        "hashes_verified": sum(item["expected_hashes"] for item in all_samples),
        "transport_errors": sum(item["transport_errors"] for item in all_samples),
        "aggregates": aggregates,
        "pooled": pooled,
    }


def main():
    baseline = aggregate_cross_provider_runs(
        os.path.join("raw", "qualified-r*", "*", "samples.jsonl"),
        "baseline",
    )
    candidate = aggregate_cross_provider_runs(
        os.path.join("raw", "final-r*", "*", "samples.jsonl"),
        "candidate",
    )

    conflict = read_json(
        os.path.join(ROOT, "raw", "certification-conflict-r4", "summary.json")
    )
    candidate_conflict = read_json(
        os.path.join(ROOT, "raw", "final-conflict-r1", "summary.json")
    )
    restart = read_json(os.path.join(ROOT, "raw", "restart", "daytona.json"))
    final_inspection = read_json(
        os.path.join(ROOT, "raw", "final-inspection", "summary.json")
    )

    core_paths = sorted(
        glob.glob(os.path.join(ROOT, "raw", "core-qualifying-r*.jsonl"))
    )
    if len(core_paths) != 3:
        raise SystemExit(f"expected three qualifying core files, found {len(core_paths)}")
    core_samples = []
    core_run_summaries = []
    for path in core_paths:
        samples = read_jsonl(path)
        if len(samples) != 100:
            raise SystemExit(f"{path}: expected 100 core samples, found {len(samples)}")
        if any(
            item["status"] != "visible" or item["latency_ms"] < 0
            for item in samples
        ):
            raise SystemExit(f"{path}: missing, timed-out, or negative core sample")
        summary = read_json(os.path.splitext(path)[0] + "-summary.json")
        if summary["status"] != "pass" or not all(summary["gates"].values()):
            raise SystemExit(f"{path}: core run summary failed")
        core_samples.extend(samples)
        core_run_summaries.append(summary)
    core = {
        "qualifying_runs": len(core_run_summaries),
        "saves": len(core_samples),
        "visibility": distribution([item["latency_ms"] for item in core_samples]),
        "local_write": distribution([item["local_write_ms"] for item in core_samples]),
        "all_run_p95_le_9_ms": all(
            summary["visibility"]["p95_ms"] <= 9.0
            for summary in core_run_summaries
        ),
        "scope": "dedicated 4-vCPU same-host volatile core path with pre-watched directories and immediate atomic/batch timers; not durable or cross-provider latency",
    }
    result = {
        "qualifying_runs": baseline["qualifying_runs"],
        "directions": baseline["directions"],
        "saves": baseline["saves"],
        "hashes_verified": baseline["hashes_verified"],
        "transport_errors": baseline["transport_errors"],
        "aggregates": baseline["aggregates"],
        "pooled": baseline["pooled"],
        "candidate_cross_provider": candidate,
        "conflict": conflict,
        "candidate_conflict": candidate_conflict,
        "restart": restart,
        "final_inspection": final_inspection,
        "core": core,
    }
    result["gates"] = {
        "six_direction_runs_passed": baseline["direction_runs"] == 6,
        "all_360_saves_visible": result["saves"] == 360,
        "all_960_hashes_verified": result["hashes_verified"] == 960,
        "zero_transport_errors": result["transport_errors"] == 0,
        "six_candidate_direction_runs_passed": candidate["direction_runs"] == 6,
        "all_360_candidate_saves_visible": candidate["saves"] == 360,
        "all_960_candidate_hashes_verified": candidate["hashes_verified"] == 960,
        "zero_candidate_transport_errors": candidate["transport_errors"] == 0,
        "conflict_passed": conflict["status"] == "pass",
        "candidate_conflict_passed": candidate_conflict["status"] == "pass",
        "restart_passed": restart["status"] == "pass",
        "final_inspection_passed": final_inspection["status"] == "pass",
        "three_core_runs_passed": core["qualifying_runs"] == 3,
        "all_300_core_saves_visible": core["saves"] == 300,
        "core_run_p95_le_9_ms": core["all_run_p95_le_9_ms"],
    }
    result["status"] = "pass" if all(result["gates"].values()) else "fail"
    output = os.path.join(ROOT, "aggregate-summary.json")
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print(json.dumps(result, separators=(",", ":")))
    if result["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
