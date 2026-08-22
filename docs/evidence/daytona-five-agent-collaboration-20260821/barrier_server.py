#!/usr/bin/env python3
"""Release a named benchmark round only after every expected writer arrives."""

import http.server
import json
import sys
import threading
import time
import urllib.parse


class Round:
    def __init__(self, parties):
        self.parties = parties
        self.roles = set()
        self.released_ns = None
        self.responses = 0


class Registry:
    def __init__(self):
        self.condition = threading.Condition()
        self.rounds = {}

    def arrive(self, key, role, parties, timeout_s):
        deadline = time.monotonic() + timeout_s
        with self.condition:
            current = self.rounds.setdefault(key, Round(parties))
            if current.parties != parties:
                raise ValueError("party count changed within round")
            if role in current.roles:
                raise ValueError("role arrived twice")
            current.roles.add(role)
            if len(current.roles) == parties:
                current.released_ns = time.time_ns()
                self.condition.notify_all()
            while current.released_ns is None:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError(sorted(current.roles))
                self.condition.wait(remaining)
            payload = {
                "key": key,
                "role": role,
                "parties": parties,
                "roles": sorted(current.roles),
                "released_ns": current.released_ns,
            }
            current.responses += 1
            if current.responses == parties:
                del self.rounds[key]
            return payload


REGISTRY = Registry()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

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
        if not parsed.path.rstrip("/").endswith("/barrier"):
            self.send_json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length))
            result = REGISTRY.arrive(
                str(payload["key"]),
                str(payload["role"]),
                int(payload["parties"]),
                float(payload.get("timeout_s", 30)),
            )
        except TimeoutError as exc:
            self.send_json(408, {"error": "barrier timeout", "arrived": exc.args[0]})
            return
        except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            self.send_json(400, {"error": str(exc)})
            return
        self.send_json(200, result)

    def log_message(self, *_):
        pass


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: barrier_server.py PORT")
    http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()


if __name__ == "__main__":
    main()
