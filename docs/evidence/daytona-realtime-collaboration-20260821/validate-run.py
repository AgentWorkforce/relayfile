#!/usr/bin/env python3
"""Regenerate deterministic benchmark bytes and verify every observed hash."""

import hashlib
import json
import os
import sys

SMALL_BYTES = 300
REPO_FILE_COUNT = 11
REPO_TOTAL_BYTES = 14_000


def load_jsonl(path):
    with open(path) as handle:
        return [json.loads(line) for line in handle if line.strip()]


def trial_files(shape, run_id, direction, trial):
    root = f"testdata/daytona-sync-benchmark/{run_id}/{direction}/{shape}-{trial:03d}"
    if shape == "small":
        relative = f"{root}/probe.txt"
        content = f"run={run_id} direction={direction} trial={trial:03d} ".encode()
        return [("/" + relative, content.ljust(SMALL_BYTES, b"x"))]
    per_file = REPO_TOTAL_BYTES // REPO_FILE_COUNT
    result = []
    for index in range(REPO_FILE_COUNT):
        relative = f"{root}/src/module_{index:02d}.go"
        header = (
            f"// run={run_id} direction={direction} trial={trial:03d} file={index:02d}\n"
            "package benchmark\n"
        ).encode()
        result.append(("/" + relative, (header + b"// deterministic filler\n" * 1000)[:per_file]))
    return result


def verify_direction(run_dir, run_id, prefix, direction, receiver):
    sends = [
        row
        for row in load_jsonl(os.path.join(run_dir, f"{prefix}-{direction}-sends.jsonl"))
        if row.get("kind") == "local_save"
    ]
    arrivals = {}
    for row in load_jsonl(os.path.join(run_dir, f"{prefix}-{receiver}-arrivals.jsonl")):
        if "path" in row:
            arrivals.setdefault(row["path"], []).append(row)

    verified = 0
    errors = []
    for send in sends:
        expected = dict(trial_files(send["shape"], run_id, direction, send["trial"]))
        if send["paths"] != list(expected):
            errors.append({"correlation_id": send["correlation_id"], "error": "path list mismatch"})
        if send["expected_bytes"] != sum(map(len, expected.values())):
            errors.append({"correlation_id": send["correlation_id"], "error": "byte count mismatch"})
        for path, content in expected.items():
            observations = arrivals.get(path, [])
            digest = hashlib.sha256(content).hexdigest()
            if len(observations) != 1:
                errors.append(
                    {"correlation_id": send["correlation_id"], "path": path, "observations": len(observations)}
                )
            elif observations[0]["size"] != len(content) or observations[0]["content_hash"] != digest:
                errors.append({"correlation_id": send["correlation_id"], "path": path, "error": "content mismatch"})
            else:
                verified += 1
    return {
        "direction": direction,
        "saves": len(sends),
        "payload_arrivals": sum(len(trial_files(row["shape"], run_id, direction, row["trial"])) for row in sends),
        "content_hashes_verified": verified,
        "errors": errors,
    }


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: validate-run.py RUN_DIR RUN_ID PREFIX OUTPUT")
    run_dir, run_id, prefix, output = sys.argv[1:]
    directions = [
        verify_direction(run_dir, run_id, prefix, "a2b", "b"),
        verify_direction(run_dir, run_id, prefix, "b2a", "a"),
    ]
    concurrency_path = os.path.join(run_dir, "concurrency-summary.json")
    with open(concurrency_path) as handle:
        concurrency = json.load(handle)["pass"]
    total_saves = sum(row["saves"] for row in directions)
    total_arrivals = sum(row["payload_arrivals"] for row in directions)
    total_verified = sum(row["content_hashes_verified"] for row in directions)
    passed = (
        total_saves == 260
        and total_arrivals == 860
        and total_verified == 860
        and not any(row["errors"] for row in directions)
        and concurrency
    )
    result = {
        "status": "pass" if passed else "fail",
        "directions": directions,
        "total_saves": total_saves,
        "total_payload_arrivals": total_arrivals,
        "total_content_hashes_verified": total_verified,
        "concurrency": concurrency,
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
    print(json.dumps(result))
    if not passed:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
