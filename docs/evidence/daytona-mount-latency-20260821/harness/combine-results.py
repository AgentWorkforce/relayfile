#!/usr/bin/env python3
"""Combine the two directional analysis artifacts without dropping tail samples."""

import json
import sys


def percentile(values, fraction):
    values = sorted(values)
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def distribution(rows):
    values = [row["save_to_visible_ms"] for row in rows]
    if not values:
        return None
    thresholds = [500, 1000, 2000, 10000, 30000, 60000]
    return {
        "samples": len(values),
        "min_ms": min(values),
        "p50_ms": percentile(values, 0.50),
        "p95_ms": percentile(values, 0.95),
        "p99_ms": percentile(values, 0.99),
        "max_ms": max(values),
        "threshold_counts": {
            f"lte_{threshold}_ms": sum(value <= threshold for value in values)
            for threshold in thresholds
        },
        "slowest": sorted(
            (
                {
                    "correlation_id": row["correlation_id"],
                    "save_to_visible_ms": row["save_to_visible_ms"],
                }
                for row in rows
            ),
            key=lambda row: row["save_to_visible_ms"],
            reverse=True,
        )[:5],
    }


def main():
    if len(sys.argv) != 5:
        raise SystemExit(
            "usage: combine-results.py A2B_SMALL B2A_SMALL A2B_REPO B2A_REPO"
        )
    artifacts = []
    for path in sys.argv[1:]:
        with open(path) as handle:
            artifacts.append(json.load(handle))
    a2b_small, b2a_small, a2b_repo, b2a_repo = artifacts
    result = {
        "small_300_bytes": {
            "a_to_b": distribution(a2b_small["per_trial"]),
            "b_to_a": distribution(b2a_small["per_trial"]),
            "pooled": distribution(a2b_small["per_trial"] + b2a_small["per_trial"]),
        },
        "repo_save_11_files_13992_bytes": {
            "a_to_b": distribution(a2b_repo["per_trial"]),
            "b_to_a": distribution(b2a_repo["per_trial"]),
            "pooled": distribution(a2b_repo["per_trial"] + b2a_repo["per_trial"]),
        },
        "integrity": {
            "trials_sent": sum(artifact["trials_sent"] for artifact in artifacts),
            "trials_complete": sum(
                artifact["trials_complete"] for artifact in artifacts
            ),
            "incomplete": sum(
                artifact["trials_incomplete"] for artifact in artifacts
            ),
            "ambiguous": sum(
                artifact["trials_ambiguous"] for artifact in artifacts
            ),
            "clock_negative": sum(
                artifact["trials_clock_negative"] for artifact in artifacts
            ),
            "extrapolated": sum(
                len(artifact["trials_extrapolated"]) for artifact in artifacts
            ),
        },
    }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
