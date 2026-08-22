#!/usr/bin/env python3
"""Recompute embedded HTTP clock summaries without changing raw observations."""

import json
import sys


def integer_median(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return (values[middle - 1] + values[middle]) // 2


def summarize(samples):
    offsets = [row["offset_ns"] for row in samples]
    delays = [row["delay_ns"] for row in samples]
    offset_ns = integer_median(offsets)
    mad_ns = integer_median([abs(value - offset_ns) for value in offsets])
    min_delay_ns = min(delays)
    uncertainty_ns = max(
        min_delay_ns // 2,
        abs(min(offsets) - offset_ns),
        abs(max(offsets) - offset_ns),
    )
    return {
        "kind": "clock_offset_summary",
        "samples": len(samples),
        "estimator": "median_offset",
        "offset_ms": offset_ns / 1e6,
        "offset_min_ms": min(offsets) / 1e6,
        "offset_max_ms": max(offsets) / 1e6,
        "offset_mad_ms": mad_ns / 1e6,
        "min_delay_ms": min_delay_ns / 1e6,
        "uncertainty_ms": uncertainty_ns / 1e6,
    }


def recompute(path):
    with open(path) as handle:
        lines = handle.readlines()
    samples = []
    summary_index = None
    for index, line in enumerate(lines):
        if not line.strip():
            continue
        row = json.loads(line)
        if row.get("kind") == "clock_offset_summary":
            summary_index = index
            continue
        if "t0_client_ns" not in row:
            continue
        expected_delay = (row["t3_client_ns"] - row["t0_client_ns"]) - (
            row["t2_server_send_ns"] - row["t1_server_recv_ns"]
        )
        expected_offset = (
            (row["t1_server_recv_ns"] - row["t0_client_ns"])
            + (row["t2_server_send_ns"] - row["t3_client_ns"])
        ) // 2
        if row["delay_ns"] != expected_delay or row["offset_ns"] != expected_offset:
            raise ValueError(f"inconsistent derived clock fields in {path}, sample {row.get('sample')}")
        samples.append(row)
    if not samples or summary_index is None:
        raise ValueError(f"missing clock samples or summary in {path}")
    lines[summary_index] = json.dumps(summarize(samples)) + "\n"
    with open(path, "w") as handle:
        handle.writelines(lines)


def main():
    if len(sys.argv) < 2:
        raise SystemExit("usage: recompute-clock-summaries.py CLOCK_JSONL [CLOCK_JSONL ...]")
    for path in sys.argv[1:]:
        recompute(path)


if __name__ == "__main__":
    main()
