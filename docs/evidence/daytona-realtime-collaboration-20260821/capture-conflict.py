#!/usr/bin/env python3
"""Capture canonical and conflict-artifact bytes from one materialized mount."""

import glob
import base64
import hashlib
import json
import os
import sys


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: capture-conflict.py MOUNT RELATIVE_PATH HOST OUTPUT")
    mount, relative, host, output = sys.argv[1:]
    if relative.startswith("/") or ".." in relative.replace("\\", "/").split("/"):
        raise SystemExit("relative path must stay within the mount")

    mount = os.path.realpath(mount)
    canonical_path = os.path.realpath(os.path.join(mount, relative))
    if os.path.commonpath([mount, canonical_path]) != mount:
        raise SystemExit("relative path resolves outside the mount")
    with open(canonical_path, "rb") as handle:
        canonical = handle.read()

    artifact_base = os.path.join(mount, ".relay", "conflicts", relative)
    artifact_pattern = glob.escape(artifact_base) + ".*"
    artifacts = []
    for path in sorted(glob.glob(artifact_pattern)):
        resolved_path = os.path.realpath(path)
        conflicts_root = os.path.realpath(os.path.join(mount, ".relay", "conflicts"))
        if os.path.commonpath([conflicts_root, resolved_path]) != conflicts_root:
            raise RuntimeError("conflict artifact resolves outside the conflict directory")
        if not os.path.isfile(resolved_path):
            continue
        with open(resolved_path, "rb") as handle:
            content = handle.read()
        artifacts.append(
            {
                "path": os.path.relpath(path, mount).replace(os.sep, "/"),
                "content_base64": base64.b64encode(content).decode("ascii"),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )

    result = {
        "host": host,
        "canonical_base64": base64.b64encode(canonical).decode("ascii"),
        "canonical_sha256": hashlib.sha256(canonical).hexdigest(),
        "artifacts": artifacts,
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")


if __name__ == "__main__":
    main()
