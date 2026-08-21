#!/usr/bin/env python3
"""Seed one Relayfile workspace from an extracted repository tree."""

import base64
import json
import mimetypes
import os
import sys
import time
import urllib.error
import urllib.request


def wire_file(root, path):
    with open(path, "rb") as handle:
        raw = handle.read()
    relative = os.path.relpath(path, root).replace(os.sep, "/")
    content_type = mimetypes.guess_type(relative)[0] or "application/octet-stream"
    try:
        content = raw.decode("utf-8")
        encoding = ""
    except UnicodeDecodeError:
        content = base64.b64encode(raw).decode("ascii")
        encoding = "base64"
    item = {
        "path": "/" + relative,
        "contentType": content_type,
        "content": content,
    }
    if encoding:
        item["encoding"] = encoding
    return item, len(raw)


def post_batch(base_url, workspace, token, files):
    body = json.dumps({"files": files}).encode()
    request = urllib.request.Request(
        f"{base_url.rstrip('/')}/v1/workspaces/{workspace}/fs/bulk",
        data=body,
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            "X-Correlation-Id": f"daytona-seed-{time.time_ns()}",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"bulk write HTTP {error.code}: {error.read().decode(errors='replace')}") from error
    if payload.get("errorCount"):
        raise RuntimeError(payload)
    return payload.get("written", 0)


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: seed-workspace.py BASE_URL WORKSPACE TOKEN_FILE ROOT")
    base_url, workspace, token_file, root = sys.argv[1:]
    with open(token_file) as handle:
        token = handle.read().strip()
    candidates = []
    for current, directories, files in os.walk(root):
        directories[:] = sorted(name for name in directories if name != ".git")
        for name in sorted(files):
            path = os.path.join(current, name)
            if not os.path.islink(path):
                candidates.append(path)

    written = 0
    total_bytes = 0
    batch = []
    batch_bytes = 0
    for path in candidates:
        item, raw_bytes = wire_file(root, path)
        encoded_bytes = len(item["content"].encode())
        # The production default request-body limit is 1 MiB. Keep enough
        # headroom for JSON escaping, paths, and request metadata.
        if batch and (len(batch) >= 100 or batch_bytes + encoded_bytes > 700_000):
            written += post_batch(base_url, workspace, token, batch)
            batch = []
            batch_bytes = 0
        batch.append(item)
        batch_bytes += encoded_bytes
        total_bytes += raw_bytes
    if batch:
        written += post_batch(base_url, workspace, token, batch)
    print(json.dumps({"files": len(candidates), "written": written, "bytes": total_bytes}))


if __name__ == "__main__":
    main()
