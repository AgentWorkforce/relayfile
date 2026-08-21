#!/usr/bin/env python3
"""Capture five public manifests, listener state, and conflict artifacts."""

import argparse
import concurrent.futures
import json
import os

from orchestrate import parse_json_output, run_daytona_command


PRIVATE_STATE = "/root/relayfile-state/0004b04faa5a4e18628cf5aa76d6db2a/state.json"


def inspect(agent, conflict_path):
    command = [
        "daytona",
        "sandbox",
        "exec",
        agent["id"],
        "--timeout",
        "180",
        "--",
        "python3",
        "/opt/relayfile-benchmark/inspect_agent.py",
        "--root",
        "/root/shared-repo",
        "--private-state",
        PRIVATE_STATE,
        "--conflict-path",
        conflict_path,
        "--role",
        agent["role"],
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
    parser.add_argument("--conflict-path", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    with open(args.config) as handle:
        agents = json.load(handle)["agents"]
    os.makedirs(args.output, exist_ok=True)

    results = []
    errors = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        futures = {agent["role"]: pool.submit(inspect, agent, args.conflict_path) for agent in agents}
        for role, future in futures.items():
            try:
                results.append(future.result())
            except Exception as error:
                errors.append({"role": role, "error": str(error)})
    results.sort(key=lambda item: item["role"])
    for result in results:
        with open(os.path.join(args.output, f"inspection-{result['role']}.json"), "w") as handle:
            json.dump(result, handle, indent=2)
            handle.write("\n")

    manifests = {
        (item["manifest"]["files"], item["manifest"]["bytes"], item["manifest"]["manifest_sha256"])
        for item in results
    }
    cursors = {item["state"]["cursor"] for item in results}
    canonicals = {item["conflict"]["canonical"] for item in results}
    canonical_hashes = {item["conflict"]["canonical_sha256"] for item in results}
    artifacts = [
        {"agent": item["role"], **artifact}
        for item in results
        for artifact in item["conflict"]["artifacts"]
    ]
    artifact_contents = {item["content"] for item in artifacts}
    canonical = next(iter(canonicals), None)
    expected = {f"{args.run_id}-agent-{role}-conflict" for role in ("a", "b", "c", "d", "e")}
    observed = artifact_contents | ({canonical} if canonical is not None else set())
    listeners = [item["state"]["event_listener"] for item in results]
    ephemeral_atomic_save_paths = {
        item["role"]: item["manifest"].get("ephemeral_atomic_save_paths", [])
        for item in results
        if item["manifest"].get("ephemeral_atomic_save_paths")
    }
    summary = {
        "run_id": args.run_id,
        "agents_inspected": len(results),
        "identical_public_manifests": len(manifests) == 1,
        "manifest": dict(zip(("files", "bytes", "manifest_sha256"), next(iter(manifests)))) if len(manifests) == 1 else None,
        "identical_cursors": len(cursors) == 1,
        "cursor": next(iter(cursors), None) if len(cursors) == 1 else None,
        "listeners_live": len(listeners) == 5 and all(
            item.get("mode") == "websocket" and item.get("status") == "listening" for item in listeners
        ),
        "canonical_content": canonical if len(canonicals) == 1 else None,
        "canonical_sha256": next(iter(canonical_hashes), None) if len(canonical_hashes) == 1 else None,
        "losing_artifacts": artifacts,
        "distinct_losing_contents": len(artifact_contents),
        "all_contenders_accounted_for": observed == expected,
        "ephemeral_atomic_save_paths": ephemeral_atomic_save_paths,
        "errors": errors,
    }
    summary["status"] = "pass" if not errors and all(
        (
            summary["agents_inspected"] == 5,
            summary["identical_public_manifests"],
            summary["identical_cursors"],
            summary["listeners_live"],
            len(canonicals) == 1,
            len(canonical_hashes) == 1,
            summary["distinct_losing_contents"] == 4,
            summary["all_contenders_accounted_for"],
            not summary["ephemeral_atomic_save_paths"],
        )
    ) else "fail"
    with open(os.path.join(args.output, "inspection-summary.json"), "w") as handle:
        json.dump(summary, handle, indent=2)
        handle.write("\n")
    print(json.dumps(summary), flush=True)
    if summary["status"] != "pass":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
