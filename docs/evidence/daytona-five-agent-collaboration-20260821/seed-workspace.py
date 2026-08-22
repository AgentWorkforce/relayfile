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


DEFAULT_MAX_REQUEST_BYTES = 900_000


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


def request_body(files):
    return json.dumps({"files": files}, separators=(",", ":")).encode()


def post_batch(base_url, workspace, token, files):
    body = request_body(files)
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
        raise RuntimeError(
            f"bulk write HTTP {error.code} ({len(body)} request bytes): "
            + error.read().decode(errors="replace")
        ) from error
    if payload.get("errorCount"):
        raise RuntimeError(payload)
    return payload.get("written", 0)


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: seed_workspace.py BASE_URL WORKSPACE TOKEN_FILE ROOT")
    base_url, workspace, token_file, root = sys.argv[1:]
    with open(token_file) as handle:
        token = handle.read().strip()
    max_request_bytes = int(
        os.environ.get("RELAYFILE_SEED_MAX_REQUEST_BYTES", DEFAULT_MAX_REQUEST_BYTES)
    )
    if max_request_bytes <= 0:
        raise SystemExit("RELAYFILE_SEED_MAX_REQUEST_BYTES must be positive")

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
    for path in candidates:
        item, raw_bytes = wire_file(root, path)
        candidate = batch + [item]
        candidate_bytes = len(request_body(candidate))
        if batch and (len(batch) >= 100 or candidate_bytes > max_request_bytes):
            written += post_batch(base_url, workspace, token, batch)
            batch = [item]
            candidate_bytes = len(request_body(batch))
        else:
            batch = candidate
        if candidate_bytes > max_request_bytes:
            raise RuntimeError(
                f"{item['path']} requires {candidate_bytes} request bytes, above "
                f"RELAYFILE_SEED_MAX_REQUEST_BYTES={max_request_bytes}"
            )
        total_bytes += raw_bytes
    if batch:
        written += post_batch(base_url, workspace, token, batch)
    print(json.dumps({"files": len(candidates), "written": written, "bytes": total_bytes}))


if __name__ == "__main__":
    main()
