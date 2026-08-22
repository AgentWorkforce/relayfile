#!/usr/bin/env python3
"""Drive five clock-calibrated Daytona writers and aggregate exact samples."""

import argparse
import concurrent.futures
import http.client
import json
import os
import statistics
import subprocess
import time
import urllib.parse


REPO_FILE_COUNT = 11


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


def clock_epoch_ms(clock_url):
    parsed = urllib.parse.urlparse(clock_url)
    connection = http.client.HTTPSConnection(
        parsed.hostname, parsed.port or 443, timeout=5
    )
    try:
        connection.request("GET", parsed.path or "/time")
        response = connection.getresponse()
        body = json.loads(response.read())
        if response.status != 200:
            raise RuntimeError(f"clock returned HTTP {response.status}")
        return float(body["epochMs"])
    finally:
        connection.close()


def parse_json_output(stdout):
    for line in reversed(stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return json.loads(line)
    raise ValueError(f"no JSON object in output: {stdout[-1000:]}")


def run_agent(agent, agents, config, args, shape, trial, release_epoch_ms, timeout_s):
    command = [
        "daytona",
        "sandbox",
        "exec",
        agent["id"],
        "--timeout",
        str(int(timeout_s + 150)),
        "--",
        "python3",
        config.get(
            "scheduled_trial_script",
            "/home/daytona/rfbench/scheduled_fanout_trial.py",
        ),
        "--root",
        config.get("mount_root", "/home/daytona/shared-repo"),
        "--run-id",
        args.run_id,
        "--path-set",
        args.path_set or args.run_id,
        "--role",
        agent["role"],
        "--shape",
        shape,
        "--trial",
        str(trial),
        "--release-epoch-ms",
        str(release_epoch_ms),
        "--clock-url",
        config["clock_url"],
        "--timeout-s",
        str(timeout_s),
    ]
    for peer in agents:
        if peer["role"] != agent["role"]:
            command.extend(["--peer-role", peer["role"]])
    completed = subprocess.run(
        command,
        text=True,
        capture_output=True,
        timeout=timeout_s + 180,
    )
    try:
        result = parse_json_output(completed.stdout)
    except (ValueError, json.JSONDecodeError) as error:
        raise RuntimeError(
            json.dumps(
                {
                    "role": agent["role"],
                    "returncode": completed.returncode,
                    "parse_error": str(error),
                    "stdout": completed.stdout[-2000:],
                    "stderr": completed.stderr[-2000:],
                }
            )
        )
    result["execution"] = {
        "daytona_returncode": completed.returncode,
        "stderr_tail": completed.stderr[-1000:],
    }
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set")
    parser.add_argument("--output", required=True)
    parser.add_argument("--small-rounds", type=int, default=15)
    parser.add_argument("--repo-rounds", type=int, default=9)
    parser.add_argument("--release-lead-ms", type=float, default=3000)
    args = parser.parse_args()
    with open(args.config) as handle:
        config = json.load(handle)
    agents = config["agents"]
    if len(agents) != 5 or len({item["role"] for item in agents}) != 5:
        raise SystemExit("config must contain five uniquely named agents")
    os.makedirs(os.path.join(args.output, "raw"), exist_ok=True)

    workload = [
        item
        for item in (
            ("small", args.small_rounds, 10),
            ("repo", args.repo_rounds, 15),
        )
        if item[1] > 0
    ]
    if not workload:
        raise SystemExit("at least one small or repository-shaped round is required")

    results = []
    for shape, rounds, timeout_s in workload:
        for trial in range(1, rounds + 1):
            round_started = time.monotonic()
            release_epoch_ms = clock_epoch_ms(config["clock_url"]) + args.release_lead_ms
            with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
                futures = {
                    agent["role"]: pool.submit(
                        run_agent,
                        agent,
                        agents,
                        config,
                        args,
                        shape,
                        trial,
                        release_epoch_ms,
                        timeout_s,
                    )
                    for agent in agents
                }
                round_results = []
                round_errors = []
                for role, future in futures.items():
                    try:
                        round_results.append(future.result())
                    except Exception as error:
                        round_errors.append({"role": role, "error": str(error)})
            round_results.sort(key=lambda item: item["role"])
            for result in round_results:
                path = os.path.join(
                    args.output,
                    "raw",
                    f"{shape}-{trial:03d}-{result['role']}.json",
                )
                with open(path, "w") as handle:
                    json.dump(result, handle, indent=2)
                    handle.write("\n")
            all_visible = len(round_results) == 5 and all(
                item.get("all_visible") for item in round_results
            )
            if round_errors or not all_visible:
                failure = {
                    "shape": shape,
                    "trial": trial,
                    "release_epoch_ms": release_epoch_ms,
                    "results_retained": len(round_results),
                    "all_visible": all_visible,
                    "errors": round_errors,
                }
                with open(
                    os.path.join(
                        args.output, "raw", f"{shape}-{trial:03d}-failure.json"
                    ),
                    "w",
                ) as handle:
                    json.dump(failure, handle, indent=2)
                    handle.write("\n")
                print(json.dumps({**failure, "status": "fail"}), flush=True)
                raise SystemExit(1)
            results.extend(round_results)
            writes = [item["write"]["completed_epoch_ms"] for item in round_results]
            print(
                json.dumps(
                    {
                        "shape": shape,
                        "trial": trial,
                        "round_wall_ms": (time.monotonic() - round_started) * 1000,
                        "write_completion_spread_ms": max(writes) - min(writes),
                        "status": "pass",
                    }
                ),
                flush=True,
            )

    shapes = {}
    directions = {}
    total_hashes = 0
    latency_uncertainties = []
    clock_deltas = []
    write_spreads = []
    for shape, _, _ in workload:
        selected = [item for item in results if item["shape"] == shape]
        pairwise = []
        all_peer = []
        local_writes = []
        by_trial = {}
        for item in selected:
            by_trial.setdefault(item["trial"], {})[item["role"]] = item
            local_writes.append(item["write"]["duration_ms"])
            clock_deltas.append(abs(item["clock"]["offset_delta_ms"]))
        for trial, trial_results in sorted(by_trial.items()):
            writes = [
                item["write"]["completed_epoch_ms"]
                for item in trial_results.values()
            ]
            write_spreads.append(max(writes) - min(writes))
            for sender_role, sender in sorted(trial_results.items()):
                sender_latencies = []
                for receiver_role, receiver in sorted(trial_results.items()):
                    if sender_role == receiver_role:
                        continue
                    observation = next(
                        item
                        for item in receiver["observations"]
                        if item["source"] == sender_role
                    )
                    latency_ms = (
                        observation["observed_epoch_ms"]
                        - sender["write"]["completed_epoch_ms"]
                    )
                    uncertainty_ms = (
                        sender["clock"]["uncertainty_ms"]
                        + receiver["clock"]["uncertainty_ms"]
                    )
                    pairwise.append(latency_ms)
                    sender_latencies.append(latency_ms)
                    latency_uncertainties.append(uncertainty_ms)
                    total_hashes += observation["hashes_verified"]
                    directions.setdefault(
                        f"{sender_role}->{receiver_role}", []
                    ).append(latency_ms)
                all_peer.append(max(sender_latencies))
        shapes[shape] = {
            "saves": len(selected),
            "pairwise_deliveries": distribution(pairwise),
            "all_peer_convergence": distribution(all_peer),
            "local_write": distribution(local_writes),
        }

    expected_saves = (args.small_rounds + args.repo_rounds) * 5
    expected_pairwise = expected_saves * 4
    expected_hashes = (
        args.small_rounds * 5 * 4
        + args.repo_rounds * 5 * 4 * REPO_FILE_COUNT
    )
    summary = {
        "run_id": args.run_id,
        "path_set": args.path_set or args.run_id,
        "measurement": "sender atomic-save completion to hash-correct peer filesystem visibility",
        "clock": {
            "source": config["clock_url"],
            "method": "per-node pre/post Cristian calibration; median of seven lowest-RTT samples",
            "max_pairwise_uncertainty_ms": max(latency_uncertainties),
            "max_pre_post_offset_delta_ms": max(clock_deltas),
        },
        "agents": [item["role"] for item in agents],
        "agent_count": len(agents),
        "saves": len(results),
        "pairwise_deliveries": sum(
            item["pairwise_deliveries"]["count"] for item in shapes.values()
        ),
        "content_hashes_verified": total_hashes,
        "write_completion_spread": distribution(write_spreads),
        "shapes": shapes,
        "directions": {
            key: distribution(values) for key, values in sorted(directions.items())
        },
    }
    gates = {
        "all_saves_present": summary["saves"] == expected_saves,
        "all_pairwise_present": summary["pairwise_deliveries"] == expected_pairwise,
        "all_hashes_correct": total_hashes == expected_hashes,
        "no_negative_latency": all(
            item["min_ms"] >= 0
            for item in [
                {
                    "min_ms": min(values),
                }
                for values in directions.values()
            ]
        ),
        "clock_uncertainty_le_15_ms": summary["clock"][
            "max_pairwise_uncertainty_ms"
        ]
        <= 15,
        "clock_offset_delta_le_3_ms": summary["clock"][
            "max_pre_post_offset_delta_ms"
        ]
        <= 3,
        "simultaneous_write_spread_p95_le_15_ms": summary[
            "write_completion_spread"
        ]["p95_ms"]
        <= 15,
    }
    if "small" in shapes:
        gates.update(
            {
                "small_pairwise_p95_le_500_ms": shapes["small"][
                    "pairwise_deliveries"
                ]["p95_ms"]
                <= 500,
                "small_pairwise_p99_le_750_ms": shapes["small"][
                    "pairwise_deliveries"
                ]["p99_ms"]
                <= 750,
                "small_all_peer_p95_le_750_ms": shapes["small"][
                    "all_peer_convergence"
                ]["p95_ms"]
                <= 750,
            }
        )
    if "repo" in shapes:
        gates.update(
            {
                "repo_pairwise_p95_le_1000_ms": shapes["repo"][
                    "pairwise_deliveries"
                ]["p95_ms"]
                <= 1000,
                "repo_all_peer_p95_le_1500_ms": shapes["repo"][
                    "all_peer_convergence"
                ]["p95_ms"]
                <= 1500,
            }
        )
    summary["gates"] = gates
    summary["qualified"] = all(gates.values())
    with open(os.path.join(args.output, "summary.json"), "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary, indent=2))
    if not summary["qualified"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
