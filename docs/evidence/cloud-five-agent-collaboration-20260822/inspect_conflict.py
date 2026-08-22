#!/usr/bin/env python3
"""Inspect one cloud conflict without exposing credentials or private state."""

import argparse
import glob
import hashlib
import json
import os


def read_bytes(path):
    with open(path, "rb") as handle:
        return handle.read()


def public_manifest(root, scope_relative=None):
    entries = []
    total_bytes = 0
    ephemeral = []
    manifest_root = (
        os.path.join(root, scope_relative) if scope_relative else root
    )
    for current, directories, files in os.walk(manifest_root):
        directories[:] = sorted(name for name in directories if name != ".relay")
        for name in sorted(files):
            path = os.path.join(current, name)
            if os.path.islink(path):
                continue
            relative = os.path.relpath(path, root).replace(os.sep, "/")
            if name == ".relayfile-mount-state.json" or name.startswith(
                ".relayfile-mount-state.json.tmp-"
            ):
                continue
            content = read_bytes(path)
            entries.append(
                (relative, len(content), hashlib.sha256(content).hexdigest())
            )
            if ".writer-tmp-" in name or ".tmp-" in name:
                ephemeral.append(relative)
            total_bytes += len(content)
    digest = hashlib.sha256()
    for relative, size, content_hash in entries:
        digest.update(f"{relative}\0{size}\0{content_hash}\n".encode())
    return {
        "scope": scope_relative or ".",
        "files": len(entries),
        "bytes": total_bytes,
        "sha256": digest.hexdigest(),
        "ephemeral_atomic_save_paths": ephemeral,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--role", required=True)
    parser.add_argument("--relative-path", required=True)
    args = parser.parse_args()

    public_state = json.loads(
        read_bytes(os.path.join(args.root, ".relay", "state.json"))
    )
    canonical = read_bytes(os.path.join(args.root, args.relative_path))
    scope_relative = os.path.dirname(os.path.dirname(args.relative_path))
    # Exact scoped mounts store conflict artifacts by the full remote path
    # (for this fleet: /benchmark/corpus/<relative>), while the public working
    # tree begins at the scoped root. Match the run-specific suffix recursively
    # so the verifier reflects the daemon's collision-safe artifact layout.
    pattern = os.path.join(
        args.root, ".relay", "conflicts", "**", args.relative_path
    ) + ".*"
    artifacts = []
    for path in sorted(glob.glob(pattern, recursive=True)):
        if not os.path.isfile(path):
            continue
        content = read_bytes(path)
        artifacts.append(
            {
                "path": os.path.relpath(path, args.root).replace(os.sep, "/"),
                "content": content.decode(errors="replace"),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )
    print(
        json.dumps(
            {
                "role": args.role,
                "manifest": public_manifest(args.root, scope_relative),
                "state": {
                    "status": public_state.get("status"),
                    "pendingWriteback": public_state.get("pendingWriteback"),
                    "pendingConflicts": public_state.get("pendingConflicts"),
                    "eventListener": public_state.get("eventListener", {}),
                },
                "conflict": {
                    "canonical": canonical.decode(errors="replace"),
                    "canonical_sha256": hashlib.sha256(canonical).hexdigest(),
                    "artifacts": artifacts,
                },
            },
            separators=(",", ":"),
        )
    )


if __name__ == "__main__":
    main()
