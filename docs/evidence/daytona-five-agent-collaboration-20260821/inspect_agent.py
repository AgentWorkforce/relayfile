#!/usr/bin/env python3
"""Capture bounded correctness evidence from one materialized agent mount."""

import argparse
import glob
import hashlib
import json
import os


def read_file(path):
    with open(path, "rb") as handle:
        return handle.read()


def is_ephemeral_atomic_save_path(relative):
    base = os.path.basename(relative).lower()
    if base.startswith((".#", ".goutputstream-")):
        return True
    if base.startswith(".~lock.") and base.endswith("#"):
        return True
    if base.endswith(("___jb_tmp___", "___jb_old___", ".swp", ".swo", ".swx", ".tmp", "~")):
        return True
    return any(marker in base and base.rsplit(marker, 1)[1] for marker in (".tmp-", ".writer-tmp-"))


def manifest(root):
    entries = []
    ephemeral_atomic_save_paths = []
    total_bytes = 0
    for current, directories, files in os.walk(root):
        directories[:] = sorted(name for name in directories if name != ".relay")
        for name in sorted(files):
            path = os.path.join(current, name)
            if os.path.islink(path):
                continue
            content = read_file(path)
            relative = os.path.relpath(path, root).replace(os.sep, "/")
            entries.append((relative, len(content), hashlib.sha256(content).hexdigest()))
            if is_ephemeral_atomic_save_path(relative):
                ephemeral_atomic_save_paths.append(relative)
            total_bytes += len(content)
    digest = hashlib.sha256()
    for relative, size, content_hash in entries:
        digest.update(f"{relative}\0{size}\0{content_hash}\n".encode())
    return {
        "files": len(entries),
        "bytes": total_bytes,
        "manifest_sha256": digest.hexdigest(),
        "ephemeral_atomic_save_paths": ephemeral_atomic_save_paths,
    }


def state(root, private_state):
    public = json.loads(read_file(os.path.join(root, ".relay", "state.json")))
    private = json.loads(read_file(private_state))
    listener = public.get("eventListener", {})
    return {
        "status": public.get("status"),
        "last_applied_revision": public.get("lastAppliedRevision"),
        "event_listener": listener,
        "cursor": (
            private.get("eventsCursor")
            or private.get("eventCursor")
            or private.get("cursor")
            or public.get("eventsCursor")
            or public.get("eventCursor")
        ),
        "tracked_files": len(public.get("files", {})),
        "conflicts": sum(1 for value in public.get("files", {}).values() if value.get("status") == "conflict"),
    }


def conflict(root, relative):
    canonical = read_file(os.path.join(root, relative))
    pattern = os.path.join(root, ".relay", "conflicts", relative) + ".*"
    artifacts = []
    for path in sorted(glob.glob(pattern)):
        if not os.path.isfile(path):
            continue
        content = read_file(path)
        artifacts.append(
            {
                "path": os.path.relpath(path, root).replace(os.sep, "/"),
                "content": content.decode(errors="replace"),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )
    return {
        "canonical": canonical.decode(errors="replace"),
        "canonical_sha256": hashlib.sha256(canonical).hexdigest(),
        "artifacts": artifacts,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--private-state", required=True)
    parser.add_argument("--conflict-path", required=True)
    parser.add_argument("--role", required=True)
    args = parser.parse_args()
    print(
        json.dumps(
            {
                "role": args.role,
                "manifest": manifest(args.root),
                "state": state(args.root, args.private_state),
                "conflict": conflict(args.root, args.conflict_path),
            },
            separators=(",", ":"),
        )
    )


if __name__ == "__main__":
    main()
