#!/usr/bin/env python3
"""Record first hash-correct visibility for a fixed set of local paths."""

import hashlib
import json
import os
import sys
import time


def main():
    if len(sys.argv) < 6 or (len(sys.argv) - 4) % 2:
        raise SystemExit(
            "usage: convergence-watch.py ROOT OUTPUT TIMEOUT_S PATH SHA256 [PATH SHA256 ...]"
        )
    root, output, timeout_raw = sys.argv[1:4]
    root = os.path.realpath(root)
    pairs = list(zip(sys.argv[4::2], sys.argv[5::2]))
    paths = [relative for relative, _ in pairs]
    if len(paths) != len(set(paths)):
        raise SystemExit("expected paths must be unique")
    expected = {}
    for relative, digest in pairs:
        normalized = relative.replace("\\", "/")
        if normalized.startswith("/") or ".." in normalized.split("/"):
            raise SystemExit("expected path must stay within the root")
        candidate = os.path.realpath(os.path.join(root, relative))
        if os.path.commonpath([root, candidate]) != root:
            raise SystemExit("expected path resolves outside the root")
        expected[relative] = digest
    started_ns = time.time_ns()
    deadline = time.monotonic() + float(timeout_raw)
    first_match_ns = {}
    while time.monotonic() < deadline:
        for relative, expected_hash in expected.items():
            if relative in first_match_ns:
                continue
            path = os.path.realpath(os.path.join(root, relative))
            if os.path.commonpath([root, path]) != root:
                raise RuntimeError("expected path resolved outside the root while watching")
            try:
                with open(path, "rb") as handle:
                    content = handle.read()
            except OSError:
                continue
            if hashlib.sha256(content).hexdigest() == expected_hash:
                first_match_ns[relative] = time.time_ns()
        if len(first_match_ns) == len(expected):
            record = {
                "started_ns": started_ns,
                "first_match_ns": first_match_ns,
                "matched_sha256": expected,
                "all_visible_ns": max(first_match_ns.values()),
                "paths": sorted(expected),
                "clock": "CLOCK_REALTIME",
            }
            with open(output, "w") as handle:
                json.dump(record, handle)
                handle.write("\n")
            print(json.dumps(record))
            return
        time.sleep(0.001)
    raise TimeoutError({"missing": sorted(set(expected) - set(first_match_ns))})


if __name__ == "__main__":
    main()
