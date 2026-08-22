#!/usr/bin/env python3
"""Pair local saves with mirror arrivals using interpolated clock offsets."""

import json
import sys


def integer_median(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return (values[middle - 1] + values[middle]) // 2


def load(path):
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


def percentile(values, fraction):
    if not values:
        return None
    values = sorted(values)
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def anchor(path):
    samples = [row for row in load(path) if "t0_client_ns" in row]
    if not samples:
        raise ValueError(f"clock anchor has no samples: {path}")
    midpoint = integer_median([(row["t0_client_ns"] + row["t3_client_ns"]) // 2 for row in samples])
    offset = integer_median([row["offset_ns"] for row in samples])
    delays = [row["delay_ns"] for row in samples]
    offsets = [row["offset_ns"] for row in samples]
    uncertainty = max(
        min(delays) // 2,
        abs(min(offsets) - offset),
        abs(max(offsets) - offset),
    )
    return midpoint, offset, min(delays), uncertainty


def summary(values):
    return {
        "min": min(values) if values else None,
        "p50": percentile(values, 0.50),
        "p95": percentile(values, 0.95),
        "p99": percentile(values, 0.99),
        "max": max(values) if values else None,
    }


def main():
    if len(sys.argv) != 7:
        raise SystemExit("usage: analyse-local.py PRE POST SENDS ARRIVALS RUN_ID SHAPE")
    pre, post = anchor(sys.argv[1]), anchor(sys.argv[2])
    sends = [row for row in load(sys.argv[3]) if row.get("run_id") == sys.argv[5] and row.get("shape") == sys.argv[6]]
    arrivals = {}
    for row in load(sys.argv[4]):
        if not row.get("kind"):
            arrivals.setdefault(row["path"], []).append(row)
    completed, incomplete, ambiguous, negative, extrapolated = [], [], [], [], []
    for send in sends:
        if len(send["paths"]) != len(set(send["paths"])):
            ambiguous.append({"correlation_id": send["correlation_id"], "paths": send["paths"], "reason": "duplicate send path"})
            continue
        missing = [path for path in send["paths"] if path not in arrivals]
        duplicate = [path for path in send["paths"] if path in arrivals and len(arrivals[path]) > 1]
        if missing:
            incomplete.append({"correlation_id": send["correlation_id"], "missing": missing})
            continue
        if duplicate:
            ambiguous.append({"correlation_id": send["correlation_id"], "paths": duplicate})
            continue
        t_send = send["write_completed_ns"]
        fraction = (t_send - pre[0]) / (post[0] - pre[0]) if post[0] != pre[0] else 0
        if fraction < 0 or fraction > 1:
            extrapolated.append(send["correlation_id"])
            continue
        offset = pre[1] + (post[1] - pre[1]) * fraction
        observed = max(arrivals[path][0]["observed_ns"] for path in send["paths"])
        save_ms = (observed - offset - send["write_completed_ns"]) / 1e6
        start_ms = (observed - offset - send["write_started_ns"]) / 1e6
        if save_ms < 0:
            negative.append({"correlation_id": send["correlation_id"], "save_to_visible_ms": save_ms})
            continue
        completed.append(
            {
                "correlation_id": send["correlation_id"],
                "files": len(send["paths"]),
                "bytes": send["expected_bytes"],
                "local_write_ms": send["local_write_ms"],
                "save_to_visible_ms": save_ms,
                "write_start_to_visible_ms": start_ms,
            }
        )
    result = {
        "run_id": sys.argv[5],
        "shape": sys.argv[6],
        "trials_sent": len(sends),
        "trials_complete": len(completed),
        "trials_incomplete": len(incomplete),
        "trials_ambiguous": len(ambiguous),
        "trials_clock_negative": len(negative),
        "trials_extrapolated": extrapolated,
        "clock": {
            "offset_pre_ms": pre[1] / 1e6,
            "offset_post_ms": post[1] / 1e6,
            "drift_ms": (post[1] - pre[1]) / 1e6,
            "pre_min_rtt_ms": pre[2] / 1e6,
            "post_min_rtt_ms": post[2] / 1e6,
            "anchor_uncertainty_ms": [pre[3] / 1e6, post[3] / 1e6],
            "model_error": "HTTPS path asymmetry and nonlinear clock change between anchors are unbounded",
        },
        "save_to_visible_ms": summary([row["save_to_visible_ms"] for row in completed]),
        "write_start_to_visible_ms": summary([row["write_start_to_visible_ms"] for row in completed]),
        "local_write_ms": summary([row["local_write_ms"] for row in completed]),
        "incomplete": incomplete,
        "ambiguous": ambiguous,
        "clock_negative": negative,
        "per_trial": completed,
    }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
