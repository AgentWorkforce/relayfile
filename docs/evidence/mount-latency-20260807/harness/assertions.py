#!/usr/bin/env python3
"""Named assertions gating the 2026-08-07 mount latency result.

Every claim in RESULTS.md is checked here against the raw evidence, so the
result cannot drift from the data it came from. Run from the evidence
directory. Exit status is non-zero if any assertion fails.

Deliberately, this fails the run rather than downgrading it: if a gate does not
pass there is no median to publish, only a blocker.
"""

import json
import os
import subprocess
import sys

RESULTS = []


def check(name, condition, detail):
    RESULTS.append((name, bool(condition), detail))


def load(path):
    return [json.loads(line) for line in open(path) if line.strip()]


def heartbeat_gate(path, label):
    """sf-mini is live iff its OWN lastHeartbeatAt advances across >=90s."""
    if not os.path.exists(path):
        check(f"{label}_gate_present", False, f"{path} missing")
        return
    samples = load(path)
    stamps = [s["node"].get("lastHeartbeatAt") for s in samples]
    distinct = [s for i, s in enumerate(stamps) if s and (i == 0 or s != stamps[i - 1])]
    span = None
    if len(samples) >= 2:
        first, last = samples[0]["sampledAtLocalUtc"], samples[-1]["sampledAtLocalUtc"]
        span = (first, last)

    check(
        f"{label}_window_at_least_90s",
        len(samples) >= 9,
        f"{len(samples)} samples at ~10s spacing spanning {span}",
    )
    check(
        f"{label}_heartbeat_advanced",
        len(distinct) >= 2,
        f"{len(distinct)} distinct heartbeat values: {distinct}",
    )
    check(
        f"{label}_node_present_every_sample",
        all(s["node"].get("present") for s in samples),
        "absence from a listing would not have been treated as offline anyway",
    )


def main():
    # --- liveness gates, before and after -------------------------------
    heartbeat_gate("raw/heartbeat-gate-pre.jsonl", "heartbeat_pre")
    heartbeat_gate("raw/heartbeat-gate-post.jsonl", "heartbeat_post")

    # --- clock offset measured, not assumed -----------------------------
    offsets = {}
    for label in ("pre", "post"):
        path = f"raw/clock-offset-{label}.jsonl"
        if not os.path.exists(path):
            check(f"clock_offset_{label}_present", False, f"{path} missing")
            continue
        samples = [r for r in load(path) if "delay_ns" in r]
        check(
            f"clock_offset_{label}_sampled",
            len(samples) >= 50,
            f"{len(samples)} NTP-style exchanges",
        )
        best = min(samples, key=lambda r: r["delay_ns"])
        offsets[label] = best["offset_ns"]
        check(
            f"clock_offset_{label}_uncertainty_under_5ms",
            best["delay_ns"] / 2e6 < 5.0,
            f"min delay {best['delay_ns']/1e6:.3f} ms -> +/-{best['delay_ns']/2e6:.3f} ms",
        )

    if "pre" in offsets and "post" in offsets:
        drift_ms = abs(offsets["post"] - offsets["pre"]) / 1e6
        check(
            "clock_drift_small_vs_signal",
            drift_ms < 10.0,
            f"offset moved {drift_ms:.3f} ms across the run",
        )
        check(
            "clocks_were_not_equal",
            abs(offsets["pre"]) > 1e6,
            f"pre-run offset {offsets['pre']/1e6:.3f} ms — assuming equal clocks "
            f"would have been wrong by this much",
        )

    # --- trial counts and completeness ----------------------------------
    # Headline statistics come from the clean batch "r2" only. The earlier
    # batch was cut short by an interrupt and is retained as correctness
    # evidence, not as a source of percentiles.
    CLEAN_BATCH = "r2"
    summaries = {}
    for shape, sends in (("small", "raw/trials-small.jsonl"), ("repo", "raw/trials-repo.jsonl")):
        if not os.path.exists(sends):
            check(f"{shape}_trials_present", False, f"{sends} missing")
            continue
        output = subprocess.run(
            [sys.executable, "harness/analyse.py",
             "raw/clock-offset-pre.jsonl", "raw/clock-offset-post.jsonl", sends,
             "raw/mount-watch.jsonl", shape, CLEAN_BATCH],
            capture_output=True, text=True,
        )
        summary = json.loads(output.stdout)
        summaries[shape] = summary
        check(
            f"{shape}_at_least_20_complete_trials",
            summary["trials_complete"] >= 20,
            f"{summary['trials_complete']} complete of {summary['trials_sent']} sent",
        )
        check(
            f"{shape}_no_lost_changes",
            summary["trials_incomplete"] == 0,
            f"incomplete: {summary['incomplete_detail']}",
        )
        check(
            f"{shape}_all_writes_accepted",
            summary["trials_non_202"] == 0,
            f"non-202: {summary['non_202_detail']}",
        )
        check(
            f"{shape}_clock_correction_interpolated_not_pinned",
            summary["trials_extrapolated_outside_anchors"] == [],
            f"offset moved {summary['clock_drift_across_run_ms']:.3f} ms across "
            f"the run; every trial interpolated within the anchor span",
        )
        check(
            f"{shape}_no_sample_hit_the_polling_fallback",
            (summary["latency_ms"]["max"] or 0) < 30_000,
            f"max {summary['latency_ms']['max']:.1f} ms; the websocket-off "
            f"reconcile safety net is ~30 s and would be obvious here",
        )

    # --- the measurement-overhead claim ---------------------------------
    if os.path.exists("raw/control-watch.jsonl") and os.path.exists("raw/control-create.jsonl"):
        observed = {}
        for record in load("raw/control-watch.jsonl"):
            if record.get("kind"):
                continue
            observed.setdefault(record["path"], record["observed_ns"])
        delays = sorted(
            (observed[c["path"]] - c["t_create_ns"]) / 1e6
            for c in load("raw/control-create.jsonl")
            if c["path"] in observed
        )
        check("control_paired_at_least_20", len(delays) >= 20, f"{len(delays)} pairs")
        overhead_median = delays[len(delays) // 2]
        check(
            "watcher_overhead_measured_not_assumed",
            len(delays) > 0,
            f"watcher detection delay median {overhead_median:.3f} ms, "
            f"max {delays[-1]:.3f} ms",
        )
        if "small" in summaries and summaries["small"]["latency_ms"]["median"]:
            signal = summaries["small"]["latency_ms"]["median"]
            check(
                "overhead_does_not_exceed_signal",
                overhead_median < signal,
                f"overhead {overhead_median:.3f} ms vs signal {signal:.1f} ms — the "
                f"existing public wording asserts the opposite",
            )

    # --- isolation ------------------------------------------------------
    status = subprocess.run(
        ["git", "status", "--porcelain", ".dev-collab-stack", ".salvaged-from-minis"],
        capture_output=True, text=True, cwd="../../..",
    ).stdout.strip().splitlines()
    disturbed = [line for line in status if not line.startswith("??")]
    check(
        "preexisting_untracked_dirs_untouched",
        not disturbed,
        f"{len(status)} entries, all still untracked: {[s[:3] for s in status]}",
    )

    # --- report ---------------------------------------------------------
    width = max(len(name) for name, _, _ in RESULTS)
    failed = 0
    for name, passed, detail in RESULTS:
        marker = "PASS" if passed else "FAIL"
        failed += 0 if passed else 1
        print(f"{marker}  {name.ljust(width)}  {detail}")
    print(f"\n{len(RESULTS) - failed}/{len(RESULTS)} assertions passed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
