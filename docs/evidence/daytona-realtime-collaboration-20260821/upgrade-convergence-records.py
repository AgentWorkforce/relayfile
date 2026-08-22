#!/usr/bin/env python3
"""Backfill the expected hashes that legacy convergence records proved."""

import json
import os
import sys


def load(path):
    with open(path) as handle:
        return json.load(handle)


def main():
    if len(sys.argv) != 4:
        raise SystemExit(
            "usage: upgrade-convergence-records.py RUN_DIR DISJOINT_PREFIX PATH_ROOT"
        )
    run_dir, prefix, path_root = sys.argv[1:]
    writes = {
        "a": load(os.path.join(run_dir, f"{prefix}-a-write.json")),
        "b": load(os.path.join(run_dir, f"{prefix}-b-write.json")),
    }
    expected = {
        f"{path_root}/a.txt": writes["a"]["sha256"],
        f"{path_root}/b.txt": writes["b"]["sha256"],
    }
    for role in ("a", "b"):
        path = os.path.join(run_dir, f"{prefix}-{role}-visible.json")
        record = load(path)
        if set(record.get("first_match_ns", {})) != set(expected):
            raise ValueError(f"legacy convergence paths do not match expected writes: {path}")
        record["matched_sha256"] = expected
        with open(path, "w") as handle:
            json.dump(record, handle)
            handle.write("\n")


if __name__ == "__main__":
    main()
