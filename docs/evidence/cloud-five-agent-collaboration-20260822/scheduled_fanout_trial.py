#!/usr/bin/env python3
"""Run one clock-calibrated concurrent filesystem save inside a sandbox."""

import argparse
import hashlib
import http.client
import json
import os
import statistics
import time
import urllib.parse


SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000


def trial_files(shape, run_id, path_set, role, trial):
    root = f"testdata/cloud-five-agent/{path_set}/{shape}-{trial:03d}/{role}"
    if shape == "small":
        content = f"run={run_id} role={role} trial={trial:03d} ".encode()
        return [(f"{root}/probe.txt", content.ljust(SMALL_BYTES, b"x"))]
    per_file = REPO_TOTAL_BYTES // REPO_FILE_COUNT
    result = []
    for index in range(REPO_FILE_COUNT):
        header = (
            f"// run={run_id} role={role} trial={trial:03d} file={index:02d}\n"
            "package benchmark\n"
        ).encode()
        content = (header + b"// deterministic filler\n" * 1000)[:per_file]
        result.append((f"{root}/src/module_{index:02d}.go", content))
    return result


def calibrate_clock(base_url, samples=15, retained=7):
    parsed = urllib.parse.urlparse(base_url)
    connection = None
    observations = []
    attempts = 0
    while len(observations) < samples and attempts < samples * 3:
        attempts += 1
        try:
            if connection is None:
                connection = http.client.HTTPSConnection(
                    parsed.hostname, parsed.port or 443, timeout=5
                )
            started_ns = time.time_ns()
            connection.request("GET", parsed.path or "/time")
            response = connection.getresponse()
            body = json.loads(response.read())
            completed_ns = time.time_ns()
            if response.status != 200:
                raise RuntimeError(f"clock returned HTTP {response.status}")
            rtt_ms = (completed_ns - started_ns) / 1e6
            midpoint_ms = (started_ns + completed_ns) / 2e6
            observations.append(
                {
                    "rtt_ms": rtt_ms,
                    "offset_ms": float(body["epochMs"]) - midpoint_ms,
                }
            )
        except (OSError, http.client.HTTPException, ValueError, KeyError, json.JSONDecodeError):
            if connection is not None:
                connection.close()
            connection = None
        time.sleep(0.01)
    if connection is not None:
        connection.close()
    if len(observations) < samples:
        raise RuntimeError(
            f"only {len(observations)} of {samples} clock samples completed"
        )
    best = sorted(observations, key=lambda item: item["rtt_ms"])[:retained]
    return {
        "samples": len(observations),
        "retained": len(best),
        "min_rtt_ms": min(item["rtt_ms"] for item in observations),
        "median_rtt_ms": statistics.median(
            item["rtt_ms"] for item in observations
        ),
        "offset_ms": statistics.median(item["offset_ms"] for item in best),
        # The clock Worker reports integer epoch milliseconds. Half the best
        # round trip plus 0.5 ms is a conservative per-host error bound.
        "uncertainty_ms": min(item["rtt_ms"] for item in observations) / 2 + 0.5,
        "best_rtt_ms": [item["rtt_ms"] for item in best],
    }


def corrected_epoch_ms(local_ns, offset_ms):
    return local_ns / 1e6 + offset_ms


def wait_until(release_epoch_ms, offset_ms):
    while True:
        remaining_ms = release_epoch_ms - corrected_epoch_ms(time.time_ns(), offset_ms)
        if remaining_ms <= 0:
            return
        time.sleep(min(0.02, max(0.0002, remaining_ms / 2000)))


def atomic_save(root, files):
    started_ns = time.time_ns()
    for relative, content in files:
        destination = os.path.join(root, relative)
        os.makedirs(os.path.dirname(destination), exist_ok=True)
        temporary = f"{destination}.writer-tmp-{os.getpid()}"
        with open(temporary, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, destination)
    completed_ns = time.time_ns()
    return started_ns, completed_ns


def matches(root, files):
    for relative, expected in files:
        try:
            with open(os.path.join(root, relative), "rb") as handle:
                actual = handle.read()
        except OSError:
            return False
        if hashlib.sha256(actual).digest() != hashlib.sha256(expected).digest():
            return False
    return True


def observe_peers(root, expected_by_role, own_role, timeout_s, poll_s):
    unresolved = {role for role in expected_by_role if role != own_role}
    observations = []
    attempts = {role: 0 for role in unresolved}
    deadline = time.monotonic() + timeout_s
    while unresolved and time.monotonic() < deadline:
        for role in list(unresolved):
            attempts[role] += 1
            if matches(root, expected_by_role[role]):
                observations.append(
                    {
                        "source": role,
                        "observed_local_ns": time.time_ns(),
                        "hashes_verified": len(expected_by_role[role]),
                        "attempts": attempts[role],
                    }
                )
                unresolved.remove(role)
        if unresolved:
            time.sleep(poll_s)
    for role in sorted(unresolved):
        observations.append(
            {
                "source": role,
                "observed_local_ns": None,
                "hashes_verified": 0,
                "attempts": attempts[role],
            }
        )
    observations.sort(key=lambda item: item["source"])
    return observations


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set", required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--peer-role", action="append", required=True)
    parser.add_argument("--shape", choices=("small", "repo"), required=True)
    parser.add_argument("--trial", type=int, required=True)
    parser.add_argument("--release-epoch-ms", type=float, required=True)
    parser.add_argument("--clock-url", required=True)
    parser.add_argument("--timeout-s", type=float, default=10)
    parser.add_argument("--poll-s", type=float, default=0.001)
    args = parser.parse_args()

    roles = sorted(set(args.peer_role + [args.role]))
    expected_by_role = {
        role: trial_files(
            args.shape, args.run_id, args.path_set, role, args.trial
        )
        for role in roles
    }
    pre = calibrate_clock(args.clock_url)
    ready_local_ns = time.time_ns()
    ready_epoch_ms = corrected_epoch_ms(ready_local_ns, pre["offset_ms"])
    if ready_epoch_ms > args.release_epoch_ms:
        raise RuntimeError(
            f"agent started {ready_epoch_ms - args.release_epoch_ms:.3f} ms after release"
        )

    wait_until(args.release_epoch_ms, pre["offset_ms"])
    write_started_ns, write_completed_ns = atomic_save(
        args.root, expected_by_role[args.role]
    )
    observations = observe_peers(
        args.root,
        expected_by_role,
        args.role,
        args.timeout_s,
        args.poll_s,
    )
    post = calibrate_clock(args.clock_url)
    midpoint_offset_ms = (pre["offset_ms"] + post["offset_ms"]) / 2
    for observation in observations:
        local_ns = observation["observed_local_ns"]
        observation["observed_epoch_ms"] = (
            None
            if local_ns is None
            else corrected_epoch_ms(local_ns, midpoint_offset_ms)
        )

    result = {
        "run_id": args.run_id,
        "path_set": args.path_set,
        "role": args.role,
        "shape": args.shape,
        "trial": args.trial,
        "release_epoch_ms": args.release_epoch_ms,
        "ready_lead_ms": args.release_epoch_ms - ready_epoch_ms,
        "write": {
            "started_local_ns": write_started_ns,
            "completed_local_ns": write_completed_ns,
            "started_epoch_ms": corrected_epoch_ms(
                write_started_ns, midpoint_offset_ms
            ),
            "completed_epoch_ms": corrected_epoch_ms(
                write_completed_ns, midpoint_offset_ms
            ),
            "duration_ms": (write_completed_ns - write_started_ns) / 1e6,
            "files": len(expected_by_role[args.role]),
            "bytes": sum(len(content) for _, content in expected_by_role[args.role]),
        },
        "observations": observations,
        "all_visible": all(
            item["observed_local_ns"] is not None for item in observations
        ),
        "clock": {
            "source": args.clock_url,
            "pre": pre,
            "post": post,
            "midpoint_offset_ms": midpoint_offset_ms,
            "offset_delta_ms": post["offset_ms"] - pre["offset_ms"],
            "uncertainty_ms": max(pre["uncertainty_ms"], post["uncertainty_ms"]),
        },
    }
    print(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
