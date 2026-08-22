#!/usr/bin/env python3
"""Analyze clock-corrected disjoint convergence and same-path preservation."""

import json
import os
import sys


def integer_median(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return (values[middle - 1] + values[middle]) // 2


def load_json(path):
    with open(path) as handle:
        return json.loads(handle.read())


def load_jsonl(path):
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


def anchor(path):
    samples = [row for row in load_jsonl(path) if "t0_client_ns" in row]
    if not samples:
        raise ValueError(f"clock anchor has no samples: {path}")
    offsets = [row["offset_ns"] for row in samples]
    delays = [row["delay_ns"] for row in samples]
    offset = integer_median(offsets)
    return {
        "midpoint_ns": integer_median([(row["t0_client_ns"] + row["t3_client_ns"]) // 2 for row in samples]),
        "offset_ns": offset,
        "uncertainty_ms": max(
            min(delays) // 2,
            abs(min(offsets) - offset),
            abs(max(offsets) - offset),
        ) / 1e6,
    }


def corrected_latency(write, visible, path, pre, post):
    completed = write["write_completed_ns"]
    denominator = post["midpoint_ns"] - pre["midpoint_ns"]
    fraction = (completed - pre["midpoint_ns"]) / denominator
    offset = pre["offset_ns"] + (post["offset_ns"] - pre["offset_ns"]) * fraction
    observed = visible["first_match_ns"][path]
    return (observed - offset - completed) / 1e6, fraction


def main():
    if len(sys.argv) != 8:
        raise SystemExit(
            "usage: analyze-concurrency.py RUN_DIR RUN_ID DISJOINT_PREFIX "
            "CLOCK_PREFIX DISJOINT_PATH_ROOT CONFLICT_PREFIX OUTPUT"
        )
    run_dir, run_id, disjoint_prefix, clock_prefix, path_root, conflict_prefix, output = sys.argv[1:]
    read = lambda name: load_json(os.path.join(run_dir, name))

    a_write = read(f"{disjoint_prefix}-a-write.json")
    b_write = read(f"{disjoint_prefix}-b-write.json")
    a_visible = read(f"{disjoint_prefix}-a-visible.json")
    b_visible = read(f"{disjoint_prefix}-b-visible.json")
    a2b_pre = anchor(os.path.join(run_dir, f"{clock_prefix}-a2b-clock-pre.jsonl"))
    a2b_post = anchor(os.path.join(run_dir, f"{clock_prefix}-a2b-clock-post.jsonl"))
    b2a_pre = anchor(os.path.join(run_dir, f"{clock_prefix}-b2a-clock-pre.jsonl"))
    b2a_post = anchor(os.path.join(run_dir, f"{clock_prefix}-b2a-clock-post.jsonl"))

    a_path = f"{path_root}/a.txt"
    b_path = f"{path_root}/b.txt"
    a2b_ms, a2b_fraction = corrected_latency(a_write, b_visible, a_path, a2b_pre, a2b_post)
    b2a_ms, b2a_fraction = corrected_latency(b_write, a_visible, b_path, b2a_pre, b2a_post)
    expected_hashes = {a_path: a_write["sha256"], b_path: b_write["sha256"]}
    disjoint_hashes_correct = all(
        visible.get("matched_sha256", {}).get(path) == digest
        for visible in (a_visible, b_visible)
        for path, digest in expected_hashes.items()
    )
    disjoint_pass = (
        0 <= a2b_fraction <= 1
        and 0 <= b2a_fraction <= 1
        and 0 <= a2b_ms <= 2000
        and 0 <= b2a_ms <= 2000
        and disjoint_hashes_correct
    )

    outcomes = [
        read(f"{conflict_prefix}-a-outcome.json"),
        read(f"{conflict_prefix}-b-outcome.json"),
    ]
    canonical_values = {outcome["canonical_sha256"] for outcome in outcomes}
    canonical_on_both = len(canonical_values) == 1
    canonical = next(iter(canonical_values)) if canonical_on_both else None
    contenders = {
        __import__("hashlib").sha256(f"{run_id}-agent-a-conflict".encode()).hexdigest(),
        __import__("hashlib").sha256(f"{run_id}-agent-b-conflict".encode()).hexdigest(),
    }
    losing = next(iter(contenders - {canonical}), None) if canonical in contenders else None
    losing_artifacts = [
        artifact
        for outcome in outcomes
        for artifact in outcome["artifacts"]
        if artifact["sha256"] == losing
    ]
    all_artifacts = [artifact for outcome in outcomes for artifact in outcome["artifacts"]]
    loser_once = len(losing_artifacts) == 1 and len(all_artifacts) == 1
    same_path_pass = canonical_on_both and canonical in contenders and loser_once

    result = {
        "run_id": run_id,
        "disjoint": {
            "a_to_b_ms": a2b_ms,
            "b_to_a_ms": b2a_ms,
            "max_ms": max(a2b_ms, b2a_ms),
            "write_start_delta_ms": abs(a_write["write_started_ns"] - b_write["write_started_ns"]) / 1e6,
            "clock_fractions": [a2b_fraction, b2a_fraction],
            "anchor_uncertainty_ms": {
                "a_to_b": [a2b_pre["uncertainty_ms"], a2b_post["uncertainty_ms"]],
                "b_to_a": [b2a_pre["uncertainty_ms"], b2a_post["uncertainty_ms"]],
            },
            "both_hash_correct": disjoint_hashes_correct,
            "pass": disjoint_pass,
        },
        "same_path": {
            "canonical": canonical,
            "canonical_on_both": canonical_on_both,
            "losing_content": losing,
            "loser_preserved_exactly_once": loser_once,
            "artifact": losing_artifacts[0] if loser_once else None,
            "silent_overwrite": not loser_once,
            "pass": same_path_pass,
        },
        "pass": disjoint_pass and same_path_pass,
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
