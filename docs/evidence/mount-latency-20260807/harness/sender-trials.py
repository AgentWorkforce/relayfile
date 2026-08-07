#!/usr/bin/env python3
"""Sender side of the one-way latency run. Runs on the laptop (sender host).

Each trial writes a UNIQUE path, so no trial can be satisfied by a previous
trial's content and none can be collapsed into another by any coalescing.

`t_send_ns` is taken on this host's CLOCK_REALTIME immediately before the HTTP
request is issued -- that is the instant a writing agent "has written". The
receiver's watcher timestamps arrival on its own clock; the analyser subtracts
the separately measured clock offset. `t_ack_ns` is recorded too, so the
server-side write leg can be separated from the propagation leg.

Raw records are appended and flushed IMMEDIATELY after each trial so that a
crash mid-run leaves usable evidence.

Trial shapes:
  small -- one ~300 byte file. Isolates propagation with transfer time ~0.
  repo  -- 11 files / ~14 KB, the p75 change set in this repository's own
           history (median 6 files/105 lines, p75 11 files/357 lines over the
           last 300 non-merge commits). A number quoted from this shape is one
           a reader can expect from real agent work.

Usage:
    sender-trials.py SHAPE SERVER WORKSPACE TOKEN_FILE RUN_ID COUNT SPACING_S OUT_JSONL
"""

import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000


def body_for(shape, run_id, trial):
    """Return (paths, request_path, payload) for one trial."""
    if shape == "small":
        path = f"/trials/{run_id}/small-{trial:03d}/probe.txt"
        # Deterministic filler; unique per trial so content hashes differ.
        content = f"trial={run_id}-{trial:03d} " .ljust(SMALL_BYTES, "x")
        return (
            [path],
            "fs/file",
            {"path": path, "content": content},
        )

    per_file = REPO_TOTAL_BYTES // REPO_FILE_COUNT
    files = []
    paths = []
    for index in range(REPO_FILE_COUNT):
        path = f"/trials/{run_id}/repo-{trial:03d}/src/module_{index:02d}.go"
        header = f"// trial={run_id}-{trial:03d} file={index:02d}\npackage main\n"
        content = header + ("// filler line to reach a realistic size\n" * 1000)
        content = content[:per_file]
        paths.append(path)
        files.append(
            {"path": path, "contentType": "text/plain", "content": content}
        )
    return paths, "fs/bulk", {"files": files}


def request(server, workspace, endpoint, payload, token, correlation_id):
    url = f"{server}/v1/workspaces/{workspace}/{endpoint}"
    method = "POST"
    if endpoint == "fs/file":
        url += "?path=" + urllib.parse.quote(payload.pop("path"), safe="")
        method = "PUT"
    encoded = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=encoded, method=method)
    req.add_header("Authorization", f"Bearer {token}")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-Correlation-Id", correlation_id)
    if method == "PUT":
        req.add_header("If-Match", "*")
    with urllib.request.urlopen(req, timeout=30) as response:
        return response.status, response.read()


def main():
    (
        shape,
        server,
        workspace,
        token_file,
        run_id,
        count,
        spacing,
        out_path,
    ) = sys.argv[1:9]
    count = int(count)
    spacing = float(spacing)
    with open(token_file) as handle:
        token = handle.read().strip()

    with open(out_path, "a") as raw:
        for trial in range(1, count + 1):
            paths, endpoint, payload = body_for(shape, run_id, trial)
            correlation_id = f"{run_id}-{shape}-{trial:03d}"
            expected_bytes = sum(
                len(f["content"]) for f in payload.get("files", [])
            ) or len(payload.get("content", ""))

            t_send_ns = time.time_ns()
            try:
                status, _ = request(
                    server, workspace, endpoint, dict(payload), token, correlation_id
                )
                error = None
            except urllib.error.HTTPError as exc:
                status, error = exc.code, exc.read().decode()[:400]
            except Exception as exc:  # noqa: BLE001 - record, never lose a trial
                status, error = None, repr(exc)
            t_ack_ns = time.time_ns()

            record = {
                "kind": "send",
                "shape": shape,
                "run_id": run_id,
                "trial": trial,
                "correlation_id": correlation_id,
                "paths": paths,
                "expected_bytes": expected_bytes,
                "t_send_ns": t_send_ns,
                "t_ack_ns": t_ack_ns,
                "ack_ms": (t_ack_ns - t_send_ns) / 1e6,
                "http_status": status,
                "error": error,
                "host": "khaliqs-macbook-pro",
                "clock": "CLOCK_REALTIME",
            }
            raw.write(json.dumps(record) + "\n")
            raw.flush()
            print(
                f"{shape} trial {trial}/{count} status={status} "
                f"ack={record['ack_ms']:.1f}ms"
            )
            if trial < count:
                time.sleep(spacing)


if __name__ == "__main__":
    main()
