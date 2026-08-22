#!/usr/bin/env python3
"""Run and verify one clock-calibrated five-agent cloud conflict."""

import argparse
import concurrent.futures
import json
import os
import subprocess
import time

from orchestrate_calibrated import clock_epoch_ms, parse_json_output


def run(command, timeout):
    completed = subprocess.run(command, text=True, capture_output=True, timeout=timeout)
    try:
        result = parse_json_output(completed.stdout)
    except Exception as error:
        raise RuntimeError(
            json.dumps(
                {
                    "returncode": completed.returncode,
                    "parse_error": str(error),
                    "stdout": completed.stdout[-2000:],
                    "stderr": completed.stderr[-2000:],
                }
            )
        )
    if completed.returncode != 0:
        raise RuntimeError(json.dumps(result))
    result["execution"] = {
        "daytona_returncode": completed.returncode,
        "stderr_tail": completed.stderr[-1000:],
    }
    return result


def writer(agent, config, run_id, release_epoch_ms):
    return run(
        [
            "daytona",
            "sandbox",
            "exec",
            agent["id"],
            "--timeout",
            "180",
            "--",
            "python3",
            config.get(
                "scheduled_conflict_script",
                "/home/daytona/rfbench/scheduled_conflict_trial.py",
            ),
            "--root",
            config.get("mount_root", "/home/daytona/shared-repo"),
            "--run-id",
            run_id,
            "--role",
            agent["role"],
            "--release-epoch-ms",
            str(release_epoch_ms),
            "--clock-url",
            config["clock_url"],
        ],
        210,
    )


def inspect(agent, config, relative_path):
    return run(
        [
            "daytona",
            "sandbox",
            "exec",
            agent["id"],
            "--timeout",
            "180",
            "--",
            "python3",
            config.get(
                "conflict_inspection_script",
                "/home/daytona/rfbench/inspect_conflict.py",
            ),
            "--root",
            config.get("mount_root", "/home/daytona/shared-repo"),
            "--role",
            agent["role"],
            "--relative-path",
            relative_path,
        ],
        210,
    )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--release-lead-ms", type=float, default=3000)
    parser.add_argument("--settle-seconds", type=float, default=20)
    args = parser.parse_args()
    with open(args.config) as handle:
        config = json.load(handle)
    agents = config["agents"]
    if len(agents) != 5 or len({item["role"] for item in agents}) != 5:
        raise SystemExit("config must contain five uniquely named agents")
    os.makedirs(args.output, exist_ok=True)

    release_epoch_ms = clock_epoch_ms(config["clock_url"]) + args.release_lead_ms
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        futures = {
            item["role"]: pool.submit(
                writer, item, config, args.run_id, release_epoch_ms
            )
            for item in agents
        }
        writers = [future.result() for future in futures.values()]
    writers.sort(key=lambda item: item["role"])
    for item in writers:
        with open(
            os.path.join(args.output, f"writer-{item['role']}.json"), "w"
        ) as handle:
            json.dump(item, handle, indent=2)
            handle.write("\n")

    time.sleep(args.settle_seconds)
    relative_path = writers[0]["relative_path"]
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        futures = {
            item["role"]: pool.submit(inspect, item, config, relative_path)
            for item in agents
        }
        inspections = [future.result() for future in futures.values()]
    inspections.sort(key=lambda item: item["role"])
    for item in inspections:
        with open(
            os.path.join(args.output, f"inspection-{item['role']}.json"), "w"
        ) as handle:
            json.dump(item, handle, indent=2)
            handle.write("\n")

    expected = {item["content"] for item in writers}
    canonicals = {item["conflict"]["canonical"] for item in inspections}
    canonical_hashes = {
        item["conflict"]["canonical_sha256"] for item in inspections
    }
    artifacts = [
        {"agent": item["role"], **artifact}
        for item in inspections
        for artifact in item["conflict"]["artifacts"]
    ]
    artifact_contents = {item["content"] for item in artifacts}
    manifests = {
        (
            item["manifest"]["files"],
            item["manifest"]["bytes"],
            item["manifest"]["sha256"],
        )
        for item in inspections
    }
    listeners = [item["state"]["eventListener"] for item in inspections]
    canonical = next(iter(canonicals), None)
    write_completions = [item["write_completed_epoch_ms"] for item in writers]
    summary = {
        "run_id": args.run_id,
        "measurement": "five clock-scheduled atomic saves to one mounted path",
        "release_epoch_ms": release_epoch_ms,
        "write_completion_spread_ms": max(write_completions)
        - min(write_completions),
        "max_clock_uncertainty_ms": max(
            item["clock"]["uncertainty_ms"] for item in writers
        ),
        "max_clock_offset_delta_ms": max(
            abs(item["clock"]["offset_delta_ms"]) for item in writers
        ),
        "canonical_content": canonical if len(canonicals) == 1 else None,
        "identical_canonical": len(canonicals) == 1
        and len(canonical_hashes) == 1,
        "identical_public_manifests": len(manifests) == 1,
        "listeners_live": len(listeners) == 5
        and all(
            item.get("mode") == "websocket" and item.get("status") == "listening"
            for item in listeners
        ),
        "distinct_losing_contents": len(artifact_contents),
        "losing_artifacts": artifacts,
        "all_contenders_accounted_for": artifact_contents
        | ({canonical} if canonical else set())
        == expected,
        "ephemeral_atomic_save_paths": {
            item["role"]: item["manifest"]["ephemeral_atomic_save_paths"]
            for item in inspections
            if item["manifest"]["ephemeral_atomic_save_paths"]
        },
    }
    summary["gates"] = {
        "five_distinct_contenders": len(expected) == 5,
        "simultaneous_write_spread_le_15_ms": summary[
            "write_completion_spread_ms"
        ]
        <= 15,
        "clock_uncertainty_le_15_ms": summary["max_clock_uncertainty_ms"] <= 15,
        "clock_offset_delta_le_3_ms": summary["max_clock_offset_delta_ms"] <= 3,
        "one_canonical_everywhere": summary["identical_canonical"],
        "four_losing_contents_preserved": summary["distinct_losing_contents"]
        == 4,
        "all_contenders_accounted_for": summary["all_contenders_accounted_for"],
        "identical_public_manifests": summary["identical_public_manifests"],
        "listeners_live": summary["listeners_live"],
        "no_ephemeral_paths": not summary["ephemeral_atomic_save_paths"],
    }
    summary["qualified"] = all(summary["gates"].values())
    with open(os.path.join(args.output, "summary.json"), "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary, indent=2))
    if not summary["qualified"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
