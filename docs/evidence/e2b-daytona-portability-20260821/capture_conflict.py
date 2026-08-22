#!/usr/bin/env python3
"""Capture canonical and conflict-artifact bytes from one materialized mount."""

import glob
import hashlib
import json
import os
import sys


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: capture-conflict.py MOUNT RELATIVE_PATH HOST OUTPUT")
    mount, relative, host, output = sys.argv[1:]
    if relative.startswith("/") or ".." in relative.split("/"):
        raise SystemExit("relative path must stay within the mount")

    canonical_path = os.path.join(mount, relative)
    with open(canonical_path, "rb") as handle:
        canonical = handle.read()

    artifact_pattern = os.path.join(mount, ".relay", "conflicts", relative) + ".*"
    artifacts = []
    for path in sorted(glob.glob(artifact_pattern)):
        if not os.path.isfile(path):
            continue
        with open(path, "rb") as handle:
            content = handle.read()
        artifacts.append(
            {
                "path": os.path.relpath(path, mount).replace(os.sep, "/"),
                "content": content.decode("utf-8"),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )

    result = {
        "host": host,
        "canonical": canonical.decode("utf-8"),
        "canonical_sha256": hashlib.sha256(canonical).hexdigest(),
        "artifacts": artifacts,
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")


if __name__ == "__main__":
    main()
