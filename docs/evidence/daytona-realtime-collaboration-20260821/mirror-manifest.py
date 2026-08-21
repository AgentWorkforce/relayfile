#!/usr/bin/env python3
"""Create a deterministic content manifest for a materialized mount."""

import hashlib
import json
import os
import sys


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: mirror-manifest.py MOUNT OUTPUT")
    mount, output = map(os.path.realpath, sys.argv[1:])
    files = []
    for current, directories, names in os.walk(mount):
        directories[:] = sorted(name for name in directories if name != ".relay")
        for name in sorted(names):
            path = os.path.join(current, name)
            if os.path.islink(path):
                continue
            with open(path, "rb") as handle:
                content = handle.read()
            files.append(
                {
                    "path": os.path.relpath(path, mount).replace(os.sep, "/"),
                    "bytes": len(content),
                    "sha256": hashlib.sha256(content).hexdigest(),
                }
            )
    files.sort(key=lambda row: row["path"])
    canonical = json.dumps(files, sort_keys=True, separators=(",", ":")).encode()
    result = {
        "file_count": len(files),
        "total_bytes": sum(row["bytes"] for row in files),
        "manifest_sha256": hashlib.sha256(canonical).hexdigest(),
        "files": files,
    }
    with open(output, "w") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")


if __name__ == "__main__":
    main()
