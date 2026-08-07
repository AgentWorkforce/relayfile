#!/usr/bin/env python3
"""Named assertions gating the 2026-08-07 hosted latency result.

Run from this evidence directory. Every headline statistic is regenerated
from append-only raw records. Failed and incomplete attempts are also gated so
they cannot disappear when the successful population is refreshed.
"""

import json
import os
import statistics
import subprocess
import sys

RESULTS = []


def check(name, condition, detail):
    RESULTS.append((name, bool(condition), detail))


def load(path):
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


def percentile(values, fraction):
    values = sorted(values)
    position = fraction * (len(values) - 1)
    lower = int(position)
    upper = min(lower + 1, len(values) - 1)
    weight = position - lower
    return values[lower] * (1 - weight) + values[upper] * weight


def analyse(sends, arrivals, label, run_id, committed):
    result = subprocess.run(
        [
            sys.executable,
            "harness/analyse.py",
            "raw/clock-offset-pre.jsonl",
            "raw/clock-offset-post.jsonl",
            sends,
            arrivals,
            label,
            run_id,
            "trials",
        ],
        capture_output=True,
        text=True,
    )
    check(
        f"{label}_analysis_exited_cleanly",
        result.returncode == 0,
        f"exit {result.returncode}; stderr={result.stderr[:240]!r}",
    )
    if result.returncode:
        return None
    summary = json.loads(result.stdout)
    check(
        f"{label}_committed_analysis_matches_raw",
        summary == json.load(open(committed)),
        committed,
    )
    return summary


def main():
    deployment = json.load(open("analysis/deployment.json"))
    check(
        "hosted_deployment_named",
        deployment.get("deployment_url") == "https://api.relayfile.dev",
        deployment.get("deployment_url"),
    )
    check(
        "deployed_build_honestly_undetermined",
        deployment.get("application_build") == "undetermined",
        deployment.get("application_build"),
    )

    # Clock offset is measured on both sides of the complete trial block.
    offsets = {}
    for label in ("pre", "post"):
        path = f"raw/clock-offset-{label}.jsonl"
        records = load(path)
        samples = [record for record in records if "t0_client_ns" in record]
        summaries = [record for record in records if record.get("kind") == "clock_offset_summary"]
        check(f"clock_{label}_n_at_least_200", len(samples) >= 200, f"n={len(samples)}")
        check(f"clock_{label}_one_summary", len(summaries) == 1, f"summaries={len(summaries)}")
        best = min(samples, key=lambda record: record["delay_ns"])
        offsets[label] = best["offset_ns"]
        check(
            f"clock_{label}_symmetry_bound_under_5ms",
            best["delay_ns"] / 2e6 < 5,
            f"min delay {best['delay_ns']/1e6:.3f} ms; +/-{best['delay_ns']/2e6:.3f} ms",
        )
    check(
        "clock_anchor_change_recorded",
        offsets["pre"] != offsets["post"],
        f"change={(offsets['post']-offsets['pre'])/1e6:.3f} ms",
    )

    small = analyse(
        "raw/trials-small.jsonl",
        "raw/mount-watch-small-r1.jsonl",
        "small",
        "hosted20260807",
        "analysis/small.json",
    )
    repo = analyse(
        "raw/trials-repo-r4.jsonl",
        "raw/mount-watch-r4.jsonl",
        "repo",
        "hosted20260807r4",
        "analysis/repo-r4.json",
    )
    for label, summary, minimum in (("small", small, 20), ("repo", repo, 20)):
        if summary is None:
            continue
        check(
            f"{label}_complete_population",
            summary["trials_complete"] >= minimum,
            f"{summary['trials_complete']} complete of {summary['trials_sent']}",
        )
        check(f"{label}_no_incomplete", summary["trials_incomplete"] == 0, summary["incomplete_detail"])
        check(f"{label}_all_accepted", summary["trials_non_202"] == 0, summary["non_202_detail"])
        check(
            f"{label}_inside_clock_anchors",
            summary["trials_extrapolated_outside_anchors"] == [],
            summary["trials_extrapolated_outside_anchors"],
        )
        check(
            f"{label}_clock_model_limit_disclosed",
            summary["clock_model_error_bound"].startswith("unbounded"),
            summary["clock_model_error_bound"],
        )
        check(
            f"{label}_scoped_path_mapping_declared",
            summary["arrival_path_prefix"] == "trials",
            summary["arrival_path_prefix"],
        )
        check(
            f"{label}_hosted_leg_names",
            all(
                "leg_a_sender_to_server_ms" in trial
                and "leg_b_server_to_receiver_ms" in trial
                and "leg_a_loopback_ms" not in trial
                and "leg_b_lan_ms" not in trial
                for trial in summary["per_trial"]
            ),
            "topology-neutral per-trial keys",
        )
    if small:
        check("small_n20_p95_caveat", small["p95_note"] is not None, small["p95_note"])
    if repo:
        check("repo_n_exceeds_20", repo["trials_complete"] == 22, f"n={repo['trials_complete']}")

    # Failed and incomplete attempts are first-class evidence, not deleted
    # setup noise. Their exact regenerated summaries must stay committed.
    attempts = [
        ("repo-r1", "raw/trials-repo-r1.jsonl", "raw/mount-watch-small-r1.jsonl", "hosted20260807", "analysis/repo-r1.json", 20, 14, 6, 0),
        ("repo-r2", "raw/trials-repo-r2.jsonl", "raw/mount-watch-r2.jsonl", "hosted20260807r2", "analysis/repo-r2.json", 20, 12, 7, 1),
        ("repo-r3", "raw/trials-repo-r3.jsonl", "raw/mount-watch-r3.jsonl", "hosted20260807r3", "analysis/repo-r3.json", 25, 15, 10, 0),
    ]
    for label, sends, arrivals, run_id, committed, sent, complete, incomplete, non_202 in attempts:
        summary = analyse(sends, arrivals, label, run_id, committed)
        if summary is None:
            continue
        actual = (
            summary["trials_sent"],
            summary["trials_complete"],
            summary["trials_incomplete"],
            summary["trials_non_202"],
        )
        expected = (sent, complete, incomplete, non_202)
        check(f"{label}_attempt_retained", actual == expected, f"actual={actual}; expected={expected}")

    warmup = load("raw/trials-warmup.jsonl")
    check("warmup_failures_retained", len(warmup) == 2, f"records={len(warmup)}")
    check(
        "cloudflare_1010_retained",
        warmup[0].get("http_status") == 403 and "1010" in (warmup[0].get("error") or ""),
        f"status={warmup[0].get('http_status')}; error={warmup[0].get('error')!r}",
    )
    r2_non_202 = [record for record in load("raw/trials-repo-r2.jsonl") if record.get("http_status") != 202]
    check("hosted_500_retained", len(r2_non_202) == 1 and r2_non_202[0]["http_status"] == 500, r2_non_202)

    # Receiver-local control: pair the exact 25 creates with exact paths.
    creates = {
        record["path"]: record
        for record in load("raw/control-create.jsonl")
        if record.get("run_id") == "hostedctrl20260807"
    }
    arrivals = {
        record["path"]: record
        for record in load("raw/control-watch.jsonl")
        if record.get("path") in creates
    }
    paired = sorted(set(creates) & set(arrivals))
    upper = [(arrivals[path]["observed_ns"] - creates[path]["t_publish_start_ns"]) / 1e6 for path in paired]
    lower = [(arrivals[path]["observed_ns"] - creates[path]["t_publish_end_ns"]) / 1e6 for path in paired]
    publish = [(creates[path]["t_publish_end_ns"] - creates[path]["t_publish_start_ns"]) / 1e6 for path in paired]
    calculated = {
        "run_id": "hostedctrl20260807",
        "created": len(creates),
        "paired": len(paired),
        "detection_delay_upper_ms": {"min": min(upper), "median": statistics.median(upper), "p95": percentile(upper, .95), "max": max(upper)},
        "detection_delay_lower_ms": {"min": min(lower), "median": statistics.median(lower), "p95": percentile(lower, .95), "max": max(lower)},
        "publish_interval_ms": {"min": min(publish), "median": statistics.median(publish), "p95": percentile(publish, .95), "max": max(publish)},
        "interpretation": "visibility occurs between publish start and publish end; detection delay is bounded by the reported lower/upper distributions",
    }
    check("control_25_of_25_paired", len(paired) == 25, f"paired={len(paired)}")
    check("control_analysis_matches_raw", calculated == json.load(open("analysis/control.json")), "analysis/control.json")
    if small:
        check(
            "control_upper_median_below_small_signal",
            statistics.median(upper) < small["latency_ms"]["median"],
            f"control={statistics.median(upper):.3f} ms; small={small['latency_ms']['median']:.3f} ms",
        )

    # Interrupted watchers are deliberately present; successful headline and
    # control watchers must include their terminal scan statistics.
    for path in ("raw/mount-watch-r4.jsonl", "raw/control-watch.jsonl"):
        records = load(path)
        check(
            f"{os.path.basename(path)}_finished",
            any(record.get("kind") == "watcher_finished" for record in records),
            path,
        )
    for path in ("raw/mount-watch-warmup.jsonl", "raw/mount-watch-r3.jsonl"):
        records = load(path)
        check(
            f"{os.path.basename(path)}_interruption_retained",
            not any(record.get("kind") == "watcher_finished" for record in records),
            path,
        )

    # Raw aliases must not leak machine identity or absolute home paths.
    raw_text = "".join(open(os.path.join("raw", name)).read() for name in os.listdir("raw"))
    check("no_absolute_home_paths", "/Users/" not in raw_text and "/home/" not in raw_text, "raw JSONL")
    hosts = {
        record["host"]
        for name in os.listdir("raw")
        if name.endswith(".jsonl")
        for record in load(os.path.join("raw", name))
        if record.get("host")
    }
    check("only_safe_host_aliases", hosts <= {"sender", "receiver"}, sorted(hosts))

    width = max(len(name) for name, _, _ in RESULTS)
    failed = 0
    for name, passed, detail in RESULTS:
        marker = "PASS" if passed else "FAIL"
        failed += 0 if passed else 1
        print(f"{marker}  {name.ljust(width)}  {detail}")
    print(f"\n{len(RESULTS)-failed}/{len(RESULTS)} assertions passed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
