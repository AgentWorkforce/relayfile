#!/usr/bin/env python3
"""Fail closed if a timed trial is missing, duplicated, or content-corrupt."""

import hashlib
import importlib.util
import json
import pathlib
import sys

sys.dont_write_bytecode = True


def load_jsonl(path):
    with path.open() as handle:
        return [json.loads(line) for line in handle if line.strip()]


def load_writer(path):
    spec = importlib.util.spec_from_file_location("benchmark_writer", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def validate_direction(root, prefix, run_id, direction, writer):
    raw = root / "raw"
    sends = load_jsonl(raw / f"{prefix}-sends.jsonl")
    arrivals_raw = load_jsonl(raw / f"{prefix}-arrivals.jsonl")
    arrivals = {}
    for row in arrivals_raw:
        if "path" in row:
            arrivals.setdefault(row["path"], []).append(row)
    expected_counts = {"small": 100, "repo": 30}
    for shape, count in expected_counts.items():
        selected = [row for row in sends if row["shape"] == shape]
        assert len(selected) == count, (prefix, shape, len(selected))
        assert sorted(row["trial"] for row in selected) == list(range(1, count + 1))
        for send in selected:
            expected = {
                "/" + relative: content
                for relative, content in writer.trial_files(
                    shape, run_id, direction, send["trial"]
                )
            }
            assert send["paths"] == list(expected), send["correlation_id"]
            assert send["expected_bytes"] == sum(map(len, expected.values()))
            for path, content in expected.items():
                matches = arrivals.get(path, [])
                assert len(matches) == 1, (path, len(matches))
                arrival = matches[0]
                assert arrival["size"] == len(content), path
                assert arrival["content_hash"] == hashlib.sha256(content).hexdigest(), path
    expected_payloads = 100 + 30 * writer.REPO_FILE_COUNT
    assert sum("path" in row for row in arrivals_raw) == expected_payloads
    for position in ("pre", "post"):
        clock = load_jsonl(raw / f"{prefix}-clock-{position}.jsonl")
        assert sum("t0_client_ns" in row for row in clock) == 100
    return {
        "direction": direction,
        "saves": len(sends),
        "payload_arrivals": expected_payloads,
        "content_hashes_verified": expected_payloads,
    }


def main():
    root = pathlib.Path(sys.argv[1]) if len(sys.argv) == 2 else pathlib.Path(__file__).parents[1]
    writer = load_writer(root / "harness" / "local-writer.py")
    results = [
        validate_direction(root, "clean-a2b", "clean-a2b-r1", "a-to-b", writer),
        validate_direction(root, "clean-b2a", "clean-b2a-r1", "b-to-a", writer),
    ]
    print(
        json.dumps(
            {
                "status": "pass",
                "directions": results,
                "total_saves": sum(row["saves"] for row in results),
                "total_payload_arrivals": sum(
                    row["payload_arrivals"] for row in results
                ),
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
