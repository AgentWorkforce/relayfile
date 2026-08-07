#!/usr/bin/env python3
"""Pair sender records with receiver arrivals and emit the latency summary.

Latency for one trial is:

    (arrival_on_receiver - clock_offset) - send_on_sender

where `clock_offset` is receiver_clock - sender_clock, measured separately
(`clock-offset.py`). Without that correction the two hosts' clocks differ by
several milliseconds and the subtraction is meaningless at this scale.

For a multi-file change set the trial is complete only when its LAST file has
arrived, so completion uses max(arrival) across the trial's paths. Ordering
across files is not assumed.

Rules this script enforces rather than papers over:
  - A trial with any missing arrival is reported as INCOMPLETE and excluded
    from percentiles, but is always counted and named in the output. Losses
    are never silently dropped.
  - The daemon's atomic-write temp files (`.name.tmp-<pid>`) are not arrivals.
  - Percentiles are computed with linear interpolation, and p95 on n=20 is
    reported with an explicit note that it rests on the top few samples.

The offset is NOT a constant. Measured before and after the trial block, it
moved by ~8 ms -- comparable to the signal itself, because the two hosts' clocks
drift relative to each other at roughly 8 ppm. Pinning a single offset would
therefore bias every trial by up to the full drift. Instead the offset is
linearly interpolated to each trial's own send time between the two anchor
measurements. Trials outside the anchor span are extrapolated and flagged.

Usage:
    analyse.py OFFSET_PRE_JSONL OFFSET_POST_JSONL SENDS_JSONL ARRIVALS_JSONL [LABEL] [RUN_ID] [ARRIVAL_PREFIX]
"""

import bisect
import json
import os
import sys


def percentile(values, fraction):
    """Linear-interpolated percentile over a sorted list."""
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def load_arrivals(path, arrival_prefix=""):
    """Map workspace path -> sorted observations, ignoring daemon temp files."""
    arrivals = {}
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        record = json.loads(line)
        if record.get("kind"):
            continue
        relative = record["path"]
        base = os.path.basename(relative)
        # writeFileAtomic publishes through `.<name>.tmp-<pid>`; the temp
        # becoming visible is not the file arriving.
        if ".tmp-" in base or base.endswith(".tmp"):
            continue
        # A scoped mount of remote /trials presents its contents at the local
        # mount root. Restore that declared remote prefix for pairing without
        # rewriting the append-only receiver record.
        key = "/" + "/".join(
            part for part in (arrival_prefix.strip("/"), relative) if part
        )
        arrivals.setdefault(key, []).append(record["observed_ns"])
    for observations in arrivals.values():
        observations.sort()
    return arrivals


def first_arrival_at_or_after(arrivals, path, lower_bound_ns):
    """Select this trial's arrival, never a stale observation from an older run."""
    observations = arrivals.get(path, [])
    index = bisect.bisect_left(observations, lower_bound_ns)
    return observations[index] if index < len(observations) else None


def anchor(path):
    """Return (reference_time_ns, offset_ns) from the least-queued exchange."""
    # The trailing summary record also mentions delay (`min_delay_ns`), so
    # select on the parsed per-sample shape rather than on the raw text.
    samples = [
        record
        for record in (json.loads(line) for line in open(path) if line.strip())
        if "t0_client_ns" in record and not record.get("kind")
    ]
    best = min(samples, key=lambda record: record["delay_ns"])
    # Midpoint of the exchange is the instant the offset describes.
    return (best["t0_client_ns"] + best["t3_client_ns"]) // 2, best["offset_ns"]


def offset_at(send_ns, pre, post):
    """Linearly interpolate the clock offset to a trial's own send time."""
    (t_pre, offset_pre), (t_post, offset_post) = pre, post
    if t_post == t_pre:
        return offset_pre, False
    fraction = (send_ns - t_pre) / (t_post - t_pre)
    extrapolated = fraction < 0 or fraction > 1
    return offset_pre + (offset_post - offset_pre) * fraction, extrapolated


def main():
    if not 5 <= len(sys.argv) <= 8:
        sys.stderr.write(__doc__)
        return 2

    pre = anchor(sys.argv[1])
    post = anchor(sys.argv[2])
    sends_path, arrivals_path = sys.argv[3], sys.argv[4]
    label = sys.argv[5] if len(sys.argv) > 5 else os.path.basename(sends_path)
    # Optional run-id filter. The raw files are append-only and hold every
    # batch that was ever run, including a batch cut short by an interrupt;
    # headline statistics must come from one clean batch, so select rather
    # than edit the raw evidence.
    run_filter = sys.argv[6] if len(sys.argv) > 6 else None
    arrival_prefix = sys.argv[7] if len(sys.argv) > 7 else ""

    arrivals = load_arrivals(arrivals_path, arrival_prefix)
    latencies = []
    extrapolated_trials = []
    incomplete = []
    non_202 = []

    for line in open(sends_path):
        line = line.strip()
        if not line:
            continue
        send = json.loads(line)
        if run_filter and send.get("run_id") != run_filter:
            continue
        if send.get("http_status") != 202:
            non_202.append((send["correlation_id"], send.get("http_status"), send.get("error")))
            continue

        trial_offset_ns, extrapolated = offset_at(send["t_send_ns"], pre, post)
        # Receiver timestamps before this modeled instant cannot belong to this
        # send. This prevents an append-only arrival file from pairing a rerun
        # with an older observation of the same path.
        receiver_send_floor_ns = send["t_send_ns"] + trial_offset_ns
        observed = [
            first_arrival_at_or_after(arrivals, path, receiver_send_floor_ns)
            for path in send["paths"]
        ]
        if any(value is None for value in observed):
            missing = [p for p, v in zip(send["paths"], observed) if v is None]
            incomplete.append((send["correlation_id"], len(missing), len(send["paths"])))
            continue

        # Change set is complete when its last file lands.
        completion_ns = max(observed)
        # Leg A: sender -> server acknowledgement. Leg B: server
        # acknowledgement -> last receiver-side file observation.
        if extrapolated:
            extrapolated_trials.append(send["correlation_id"])
        corrected_arrival_ns = completion_ns - trial_offset_ns
        latencies.append(
            {
                "correlation_id": send["correlation_id"],
                "files": len(send["paths"]),
                "bytes": send["expected_bytes"],
                "offset_applied_ms": trial_offset_ns / 1e6,
                "leg_a_sender_to_server_ms": send["ack_ms"],
                "leg_b_server_to_receiver_ms": (corrected_arrival_ns - send["t_ack_ns"]) / 1e6,
                "latency_ms": (corrected_arrival_ns - send["t_send_ns"]) / 1e6,
            }
        )

    values = sorted(item["latency_ms"] for item in latencies)
    acks = sorted(item["leg_a_sender_to_server_ms"] for item in latencies)
    legs_b = sorted(item["leg_b_server_to_receiver_ms"] for item in latencies)
    summary = {
        "label": label,
        "arrival_path_prefix": arrival_prefix,
        "clock_offset_pre_ms": pre[1] / 1e6,
        "clock_offset_post_ms": post[1] / 1e6,
        "clock_drift_across_run_ms": (post[1] - pre[1]) / 1e6,
        "clock_correction": "linearly interpolated to each trial's send time",
        "clock_model_error_bound": (
            "unbounded between anchors; endpoint samples cannot exclude a clock step"
        ),
        "trials_extrapolated_outside_anchors": extrapolated_trials,
        "trials_sent": len(latencies) + len(incomplete) + len(non_202),
        "trials_complete": len(values),
        "trials_incomplete": len(incomplete),
        "trials_non_202": len(non_202),
        "incomplete_detail": incomplete,
        "non_202_detail": non_202,
        "latency_ms": {
            "min": values[0] if values else None,
            "median": percentile(values, 0.50),
            "p95": percentile(values, 0.95),
            "max": values[-1] if values else None,
        },
        "p95_note": (
            "n=20: interpolated p95 depends on the two largest observations"
            if len(values) == 20
            else None
        ),
        "leg_a_sender_to_server_ms": {
            "median": percentile(acks, 0.50),
            "p95": percentile(acks, 0.95),
        },
        "leg_b_server_to_receiver_ms": {
            "median": percentile(legs_b, 0.50),
            "p95": percentile(legs_b, 0.95),
        },
        "per_trial": latencies,
    }
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
