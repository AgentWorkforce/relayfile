#!/usr/bin/env python3
"""Drive and retain one simultaneous five-agent same-path conflict."""

import argparse
import concurrent.futures
import json
import os

from orchestrate import parse_json_output, run_daytona_command


def run_agent(agent, barrier_url, run_id, conflict_script, mount_root):
    command = [
        "daytona",
        "sandbox",
        "exec",
        agent["id"],
        "--timeout",
        "180",
        "--",
        "python3",
        conflict_script,
        "--root",
        mount_root,
        "--run-id",
        run_id,
        "--role",
        agent["role"],
        "--barrier-url",
        barrier_url,
    ]
    completed, attempts = run_daytona_command(command, 210)
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
        "control_plane_attempts": attempts,
        "stderr_tail": completed.stderr[-1000:],
    }
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    with open(args.config) as handle:
        config = json.load(handle)
    agents = config["agents"]
    conflict_script = config.get(
        "conflict_script", "/opt/relayfile-benchmark/conflict_write.py"
    )
    mount_root = config.get("mount_root", "/root/shared-repo")
    if len(agents) != 5 or len({item["role"] for item in agents}) != 5:
        raise SystemExit("config must contain five uniquely named agents")
    os.makedirs(args.output, exist_ok=True)

    results = []
    errors = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        futures = {
            agent["role"]: pool.submit(
                run_agent,
                agent,
                config["barrier_url"],
                args.run_id,
                conflict_script,
                mount_root,
            )
            for agent in agents
        }
        for role, future in futures.items():
            try:
                results.append(future.result())
            except Exception as error:
                errors.append({"role": role, "error": str(error)})
    results.sort(key=lambda item: item["role"])
    for result in results:
        with open(os.path.join(args.output, f"writer-{result['role']}.json"), "w") as handle:
            json.dump(result, handle, indent=2)
            handle.write("\n")
    summary = {
        "run_id": args.run_id,
        "writers": len(results),
        "distinct_contents": len({item["content"] for item in results}),
        "distinct_hashes": len({item["sha256"] for item in results}),
        "one_barrier_release": len({item["barrier_released_ns"] for item in results}) == 1,
        "errors": errors,
    }
    summary["status"] = "pass" if not errors and all(
        (summary["writers"] == 5, summary["distinct_contents"] == 5,
         summary["distinct_hashes"] == 5, summary["one_barrier_release"])
    ) else "fail"
    with open(os.path.join(args.output, "writers-summary.json"), "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary), flush=True)
    if summary["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
