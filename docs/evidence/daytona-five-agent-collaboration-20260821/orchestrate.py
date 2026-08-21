#!/usr/bin/env python3
"""Drive simultaneous five-agent saves through the Daytona CLI."""

import argparse
import concurrent.futures
import json
import os
import statistics
import subprocess
import sys
import time


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


def parse_json_output(stdout):
    for line in reversed(stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return json.loads(line)
    raise ValueError(f"no JSON object in output: {stdout[-1000:]}")


def run_agent(agent, agents, barrier_url, run_id, path_set, shape, trial, timeout_s):
    command = [
        "daytona",
        "sandbox",
        "exec",
        agent["id"],
        "--timeout",
        str(int(timeout_s + 40)),
        "--",
        "python3",
        "/opt/relayfile-benchmark/fanout_trial.py",
        "--root",
        "/root/shared-repo",
        "--run-id",
        run_id,
        "--path-set",
        path_set,
        "--role",
        agent["role"],
        "--shape",
        shape,
        "--trial",
        str(trial),
        "--barrier-url",
        barrier_url,
        "--timeout-s",
        str(timeout_s),
    ]
    for receiver in agents:
        if receiver["role"] != agent["role"]:
            command.extend(["--receiver", f"{receiver['role']}={receiver['probe_url']}"])
    completed = subprocess.run(command, text=True, capture_output=True, timeout=timeout_s + 60)
    if completed.returncode:
        raise RuntimeError(
            json.dumps(
                {
                    "role": agent["role"],
                    "returncode": completed.returncode,
                    "stdout": completed.stdout[-2000:],
                    "stderr": completed.stderr[-2000:],
                }
            )
        )
    return parse_json_output(completed.stdout)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set")
    parser.add_argument("--output", required=True)
    parser.add_argument("--small-rounds", type=int, default=50)
    parser.add_argument("--repo-rounds", type=int, default=10)
    args = parser.parse_args()
    with open(args.config) as handle:
        config = json.load(handle)
    agents = config["agents"]
    path_set = args.path_set or args.run_id
    if len(agents) != 5 or len({item["role"] for item in agents}) != 5:
        raise SystemExit("config must contain five uniquely named agents")
    os.makedirs(os.path.join(args.output, "raw"), exist_ok=True)

    results = []
    for shape, rounds, timeout_s in (
        ("small", args.small_rounds, 10),
        ("repo", args.repo_rounds, 15),
    ):
        for trial in range(1, rounds + 1):
            round_started = time.monotonic()
            with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
                futures = [
                    pool.submit(
                        run_agent,
                        agent,
                        agents,
                        config["barrier_url"],
                        args.run_id,
                        path_set,
                        shape,
                        trial,
                        timeout_s,
                    )
                    for agent in agents
                ]
                round_results = [future.result() for future in futures]
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
            results.extend(round_results)
            print(
                json.dumps(
                    {
                        "shape": shape,
                        "trial": trial,
                        "round_wall_ms": (time.monotonic() - round_started) * 1000,
                        "max_all_peer_ms": max(item["all_peer_latency_ms"] for item in round_results),
                    }
                ),
                flush=True,
            )

    shapes = {}
    directions = {}
    total_hashes = 0
    for shape in ("small", "repo"):
        selected = [item for item in results if item["shape"] == shape]
        pairwise = []
        all_peer = []
        local_writes = []
        for item in selected:
            all_peer.append(item["all_peer_latency_ms"])
            local_writes.append(item["local_write_ms"])
            for receiver in item["receivers"]:
                if receiver["status"] != "visible":
                    raise RuntimeError({"missing": receiver, "save": item})
                pairwise.append(receiver["latency_ms"])
                total_hashes += receiver["hashes_verified"]
                key = f"{item['role']}->{receiver['receiver']}"
                directions.setdefault(key, []).append(receiver["latency_ms"])
        shapes[shape] = {
            "saves": len(selected),
            "pairwise_deliveries": distribution(pairwise),
            "all_peer_convergence": distribution(all_peer),
            "local_write": distribution(local_writes),
        }

    summary = {
        "run_id": args.run_id,
        "path_set": path_set,
        "agents": [item["role"] for item in agents],
        "agent_count": len(agents),
        "saves": len(results),
        "pairwise_deliveries": sum(len(item["receivers"]) for item in results),
        "content_hashes_verified": total_hashes,
        "shapes": shapes,
        "directions": {key: distribution(values) for key, values in sorted(directions.items())},
    }
    gates = {
        "all_saves_present": summary["saves"] == args.small_rounds * 5 + args.repo_rounds * 5,
        "all_pairwise_present": summary["pairwise_deliveries"]
        == (args.small_rounds + args.repo_rounds) * 5 * 4,
        "all_hashes_correct": total_hashes
        == args.small_rounds * 5 * 4 + args.repo_rounds * 5 * 4 * REPO_FILE_COUNT,
        "small_pairwise_p95_le_1000_ms": shapes["small"]["pairwise_deliveries"]["p95_ms"] <= 1000,
        "small_pairwise_p99_le_2000_ms": shapes["small"]["pairwise_deliveries"]["p99_ms"] <= 2000,
        "small_all_peer_p95_le_1500_ms": shapes["small"]["all_peer_convergence"]["p95_ms"] <= 1500,
        "small_max_le_5000_ms": shapes["small"]["all_peer_convergence"]["max_ms"] <= 5000,
        "repo_pairwise_p95_le_3000_ms": shapes["repo"]["pairwise_deliveries"]["p95_ms"] <= 3000,
        "repo_all_peer_p95_le_4000_ms": shapes["repo"]["all_peer_convergence"]["p95_ms"] <= 4000,
        "repo_max_le_8000_ms": shapes["repo"]["all_peer_convergence"]["max_ms"] <= 8000,
    }
    summary["gates"] = gates
    summary["status"] = "pass" if all(gates.values()) else "fail"
    summary_path = os.path.join(args.output, "summary.json")
    with open(summary_path, "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary), flush=True)
    if summary["status"] != "pass":
        raise SystemExit(1)


REPO_FILE_COUNT = 11


if __name__ == "__main__":
    main()
