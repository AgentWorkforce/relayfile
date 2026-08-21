#!/usr/bin/env python3
"""Atomically save local files and prove hash-correct visibility on one peer."""

import argparse
import hashlib
import http.client
import json
import os
import time
import urllib.parse


SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000


def trial_files(shape, run_id, path_set, role, trial):
    root = f"testdata/cross-provider/{path_set}/{shape}-{trial:03d}/{role}"
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


class ProbeConnection:
    def __init__(self, base_url, timeout_s):
        parsed = urllib.parse.urlparse(base_url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError(f"invalid receiver URL: {base_url}")
        connection_type = (
            http.client.HTTPSConnection
            if parsed.scheme == "https"
            else http.client.HTTPConnection
        )
        port = parsed.port or (443 if parsed.scheme == "https" else 80)
        self.connection = connection_type(parsed.hostname, port, timeout=timeout_s)
        self.path = parsed.path.rstrip("/") + "/probe-batch"
        if parsed.query:
            self.path += "?" + parsed.query

    def probe(self, files):
        body = json.dumps(
            {
                "files": [
                    {"path": path, "sha256": hashlib.sha256(content).hexdigest()}
                    for path, content in files
                ]
            },
            separators=(",", ":"),
        ).encode()
        self.connection.request(
            "POST",
            self.path,
            body=body,
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
            },
        )
        response = self.connection.getresponse()
        payload = response.read()
        if response.status != 200:
            raise RuntimeError(f"probe HTTP {response.status}: {payload[:200]!r}")
        return json.loads(payload)

    def close(self):
        self.connection.close()


def atomic_save(root, files):
    wall_started_ns = time.time_ns()
    monotonic_started_ns = time.monotonic_ns()
    for relative, content in files:
        destination = os.path.join(root, relative)
        os.makedirs(os.path.dirname(destination), exist_ok=True)
        temporary = f"{destination}.writer-tmp-{os.getpid()}"
        with open(temporary, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, destination)
    return {
        "wall_started_ns": wall_started_ns,
        "wall_completed_ns": time.time_ns(),
        "monotonic_started_ns": monotonic_started_ns,
        "monotonic_completed_ns": time.monotonic_ns(),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set")
    parser.add_argument("--role", required=True)
    parser.add_argument("--receiver-role", required=True)
    parser.add_argument("--receiver-url", required=True)
    parser.add_argument("--shape", choices=("small", "repo"), required=True)
    parser.add_argument("--trial", type=int, required=True)
    parser.add_argument("--timeout-s", type=float, default=15)
    parser.add_argument("--poll-s", type=float, default=0.001)
    args = parser.parse_args()

    path_set = args.path_set or args.run_id
    files = trial_files(args.shape, args.run_id, path_set, args.role, args.trial)
    write = atomic_save(args.root, files)
    started = write["monotonic_completed_ns"]
    deadline = time.monotonic() + args.timeout_s
    attempts = 0
    transport_errors = 0
    probe = None
    visible = None
    while time.monotonic() < deadline:
        attempts += 1
        try:
            if probe is None:
                probe = ProbeConnection(args.receiver_url, min(1.0, args.timeout_s))
            payload = probe.probe(files)
            if payload.get("all_match"):
                visible = time.monotonic_ns()
                break
        except (OSError, RuntimeError, http.client.HTTPException, json.JSONDecodeError):
            transport_errors += 1
            if probe is not None:
                probe.close()
            probe = None
        time.sleep(args.poll_s)
    if probe is not None:
        probe.close()

    result = {
        "run_id": args.run_id,
        "path_set": path_set,
        "role": args.role,
        "receiver": args.receiver_role,
        "shape": args.shape,
        "trial": args.trial,
        "paths": [path for path, _ in files],
        "expected_bytes": sum(len(content) for _, content in files),
        "expected_hashes": len(files),
        "write": write,
        "local_write_ms": (
            write["monotonic_completed_ns"] - write["monotonic_started_ns"]
        )
        / 1e6,
        "attempts": attempts,
        "transport_errors": transport_errors,
        "status": "visible" if visible is not None else "timeout",
        "latency_ms": None if visible is None else (visible - started) / 1e6,
        "clock": "sender CLOCK_MONOTONIC; no cross-host subtraction",
    }
    print(json.dumps(result, separators=(",", ":")), flush=True)
    if visible is None:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
