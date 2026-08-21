#!/usr/bin/env python3
"""Expose hash-correct visibility of a materialized mount over HTTP."""

import hashlib
import http.server
import json
import os
import sys
import time
import urllib.parse


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    root = ""

    def send_json(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path.rstrip("/").endswith("/health"):
            self.send_json(200, {"status": "ok", "observed_ns": time.time_ns()})
            return
        self.send_json(404, {"error": "not found"})

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        if not parsed.path.rstrip("/").endswith("/probe-batch"):
            self.send_json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 1_000_000:
                raise ValueError("invalid body length")
            payload = json.loads(self.rfile.read(length))
            files = payload["files"]
            if not isinstance(files, list) or not files or len(files) > 100:
                raise ValueError("files must contain 1..100 entries")
            results = [self.inspect(item) for item in files]
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            self.send_json(400, {"error": str(exc)})
            return
        self.send_json(
            200,
            {
                "all_match": all(item["match"] for item in results),
                "matches": sum(1 for item in results if item["match"]),
                "files": results,
                "observed_ns": time.time_ns(),
            },
        )

    def inspect(self, item):
        relative = item["path"]
        expected = item["sha256"]
        if not isinstance(relative, str) or relative.startswith("/"):
            raise ValueError("paths must be relative")
        if ".." in relative.split("/"):
            raise ValueError("path escapes mount")
        if not isinstance(expected, str) or len(expected) != 64:
            raise ValueError("sha256 must contain 64 hex characters")
        root = os.path.realpath(self.root)
        candidate = os.path.realpath(os.path.join(root, relative))
        if not candidate.startswith(root + os.sep):
            raise ValueError("path escapes mount")
        try:
            with open(candidate, "rb") as handle:
                content = handle.read()
        except OSError:
            return {"path": relative, "match": False, "exists": False}
        digest = hashlib.sha256(content).hexdigest()
        return {
            "path": relative,
            "match": digest == expected,
            "exists": True,
            "size": len(content),
            "sha256": digest,
        }

    def log_message(self, *_):
        pass


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: hash_probe.py MOUNT_ROOT PORT")
    Handler.root = os.path.realpath(sys.argv[1])
    http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[2])), Handler).serve_forever()


if __name__ == "__main__":
    main()
