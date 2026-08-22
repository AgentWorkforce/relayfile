#!/usr/bin/env python3
"""Make one local save and measure hash-correct visibility on every peer."""

import argparse
import concurrent.futures
import hashlib
import http.client
import json
import os
import threading
import time
import urllib.parse


SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000
PROBE_REQUEST_TIMEOUT_S = 1.0


class RequestDeadlineExceeded(TimeoutError):
    """Raised when one observation request exceeds its wall-clock budget."""


def trial_files(shape, run_id, path_set, role, trial):
    root = f"testdata/daytona-five-agent/{path_set}/{shape}-{trial:03d}/{role}"
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


class JSONConnection:
    def __init__(self, base_url, timeout_s=5):
        self.parsed = urllib.parse.urlparse(base_url)
        if self.parsed.scheme not in {"http", "https"} or not self.parsed.hostname:
            raise ValueError(f"invalid URL: {base_url}")
        self.timeout_s = timeout_s
        self.connection = None

    def connect(self):
        connection_type = (
            http.client.HTTPSConnection if self.parsed.scheme == "https" else http.client.HTTPConnection
        )
        port = self.parsed.port or (443 if self.parsed.scheme == "https" else 80)
        self.connection = connection_type(self.parsed.hostname, port, timeout=self.timeout_s)

    def request(self, method, endpoint, payload=None):
        if self.connection is None:
            self.connect()
        base_path = self.parsed.path.rstrip("/")
        path = base_path + endpoint
        if self.parsed.query:
            path += "?" + self.parsed.query
        body = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
        headers = {"Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
            headers["Content-Length"] = str(len(body))
        try:
            self.connection.request(method, path, body=body, headers=headers)
            response = self.connection.getresponse()
            raw = response.read()
            if response.status != 200:
                raise RuntimeError(f"HTTP {response.status}: {raw.decode(errors='replace')}")
            return json.loads(raw)
        except Exception:
            if self.connection is not None:
                self.connection.close()
            self.connection = None
            raise

    def close(self):
        if self.connection is not None:
            self.connection.close()


def request_with_deadline(connection, method, endpoint, payload, timeout_s):
    """Bound DNS, TLS, proxy, and response work, not just socket operations."""
    completed = threading.Event()
    outcome = {}

    def invoke():
        try:
            outcome["payload"] = connection.request(method, endpoint, payload)
        except Exception as exc:  # Propagate the request error on the caller thread.
            outcome["error"] = exc
        finally:
            completed.set()

    worker = threading.Thread(target=invoke, daemon=True)
    worker.start()
    if not completed.wait(timeout_s):
        connection.close()
        raise RequestDeadlineExceeded(f"request exceeded {timeout_s:.3f}s")
    if "error" in outcome:
        raise outcome["error"]
    return outcome["payload"]


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


def wait_for_receiver(receiver, base_url, expected, completed_ns, timeout_s, poll_s):
    # A signed Daytona preview tunnel can occasionally hold one HTTP request
    # until the client's socket timeout even while the peer filesystem is
    # already current. Bound one observation attempt and reconnect; the total
    # save-to-proof clock keeps running, so this improves liveness without
    # dropping or shortening the measured sample.
    connection = None
    attempts = 0
    errors = 0
    deadline = time.monotonic() + timeout_s
    request = {
        "files": [
            {"path": relative, "sha256": hashlib.sha256(content).hexdigest()}
            for relative, content in expected
        ]
    }
    try:
        while time.monotonic() < deadline:
            attempts += 1
            try:
                if connection is None:
                    connection = JSONConnection(
                        base_url,
                        timeout_s=min(PROBE_REQUEST_TIMEOUT_S, timeout_s),
                    )
                payload = request_with_deadline(
                    connection,
                    "POST",
                    "/probe-batch",
                    request,
                    min(PROBE_REQUEST_TIMEOUT_S, max(0.001, deadline - time.monotonic())),
                )
                observed_ns = time.time_ns()
                if payload.get("all_match"):
                    return {
                        "receiver": receiver,
                        "status": "visible",
                        "hashes_verified": len(expected),
                        "attempts": attempts,
                        "transport_errors": errors,
                        "observed_sender_ns": observed_ns,
                        "latency_ms": (observed_ns - completed_ns) / 1e6,
                    }
            except (OSError, RuntimeError, http.client.HTTPException, json.JSONDecodeError, RequestDeadlineExceeded):
                errors += 1
                if connection is not None:
                    connection.close()
                connection = None
            time.sleep(poll_s)
    finally:
        if connection is not None:
            connection.close()
    return {
        "receiver": receiver,
        "status": "timeout",
        "hashes_verified": 0,
        "attempts": attempts,
        "transport_errors": errors,
        "latency_ms": None,
    }


def await_receiver(receiver, base_url, expected, completed_ns, timeout_s, poll_s):
    connection = JSONConnection(base_url, timeout_s=timeout_s + 2.0)
    request = {
        "files": [
            {"path": relative, "sha256": hashlib.sha256(content).hexdigest()}
            for relative, content in expected
        ]
    }
    try:
        endpoint = (
            "/await-batch?"
            + urllib.parse.urlencode({"timeout_s": timeout_s, "poll_s": poll_s})
        )
        payload = request_with_deadline(
            connection,
            "POST",
            endpoint,
            request,
            timeout_s + 2.0,
        )
        observed_ns = time.time_ns()
        if payload.get("all_match"):
            return {
                "receiver": receiver,
                "status": "visible",
                "hashes_verified": len(expected),
                "attempts": int(payload.get("attempts", 1)),
                "transport_errors": 0,
                "observed_receiver_ns": payload.get("observed_ns"),
                "observed_sender_ns": observed_ns,
                "latency_ms": (observed_ns - completed_ns) / 1e6,
            }
    except (
        OSError,
        RuntimeError,
        http.client.HTTPException,
        json.JSONDecodeError,
        RequestDeadlineExceeded,
    ):
        pass
    finally:
        connection.close()
    return {
        "receiver": receiver,
        "status": "timeout",
        "hashes_verified": 0,
        "attempts": 1,
        "transport_errors": 1,
        "latency_ms": None,
    }


def parse_receiver(raw):
    if "=" not in raw:
        raise argparse.ArgumentTypeError("receiver must be ROLE=URL")
    role, url = raw.split("=", 1)
    if not role or not url:
        raise argparse.ArgumentTypeError("receiver must be ROLE=URL")
    return role, url


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--path-set")
    parser.add_argument("--role", required=True)
    parser.add_argument("--shape", choices=("small", "repo"), required=True)
    parser.add_argument("--trial", type=int, required=True)
    parser.add_argument("--barrier-url", required=True)
    parser.add_argument("--receiver", action="append", type=parse_receiver, default=[])
    parser.add_argument("--parties", type=int, default=5)
    parser.add_argument("--timeout-s", type=float, default=10)
    parser.add_argument("--barrier-timeout-s", type=float, default=120)
    parser.add_argument("--poll-s", type=float, default=0.005)
    parser.add_argument("--probe-mode", choices=("poll", "await"), default="poll")
    args = parser.parse_args()
    if len(args.receiver) != args.parties - 1:
        raise SystemExit(f"expected {args.parties - 1} receivers, got {len(args.receiver)}")

    path_set = args.path_set or args.run_id
    expected = trial_files(args.shape, args.run_id, path_set, args.role, args.trial)

    barrier = JSONConnection(args.barrier_url, timeout_s=args.barrier_timeout_s + 10)
    barrier_started_ns = time.time_ns()
    try:
        release = barrier.request(
            "POST",
            "/barrier",
            {
                "key": f"{args.run_id}:{args.shape}:{args.trial:03d}",
                "role": args.role,
                "parties": args.parties,
                "timeout_s": args.barrier_timeout_s,
            },
        )
    finally:
        barrier.close()
    barrier_completed_ns = time.time_ns()

    write_started_ns, write_completed_ns = atomic_save(args.root, expected)
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(args.receiver)) as pool:
        futures = [
            pool.submit(
                await_receiver if args.probe_mode == "await" else wait_for_receiver,
                receiver,
                url,
                expected,
                write_completed_ns,
                args.timeout_s,
                args.poll_s,
            )
            for receiver, url in args.receiver
        ]
        receivers = sorted((future.result() for future in futures), key=lambda item: item["receiver"])

    result = {
        "run_id": args.run_id,
        "path_set": path_set,
        "role": args.role,
        "shape": args.shape,
        "trial": args.trial,
        "paths": [relative for relative, _ in expected],
        "expected_bytes": sum(len(content) for _, content in expected),
        "expected_hashes": len(expected),
        "barrier": {
            "released_ns": release["released_ns"],
            "roles": release["roles"],
            "round_trip_ms": (barrier_completed_ns - barrier_started_ns) / 1e6,
        },
        "write_started_ns": write_started_ns,
        "write_completed_ns": write_completed_ns,
        "local_write_ms": (write_completed_ns - write_started_ns) / 1e6,
        "receivers": receivers,
        "all_visible": all(item["status"] == "visible" for item in receivers),
        "all_peer_latency_ms": max(
            (item["latency_ms"] for item in receivers if item["latency_ms"] is not None),
            default=None,
        ),
        "clock": "sender CLOCK_REALTIME; no cross-host subtraction",
    }
    print(json.dumps(result, separators=(",", ":")), flush=True)
    if not result["all_visible"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
