#!/usr/bin/env python3
"""NTP-style clock-offset anchors transported through an HTTP endpoint."""

import http.server
import http.client
import json
import os
import sys
import time
import urllib.parse


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    mirror_root = None

    def do_GET(self):
        received = time.time_ns()
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/probe" and self.mirror_root:
            relative = urllib.parse.parse_qs(parsed.query).get("path", [""])[0].lstrip("/")
            candidate = os.path.realpath(os.path.join(self.mirror_root, relative))
            root = os.path.realpath(self.mirror_root)
            exists = candidate.startswith(root + os.sep) and os.path.isfile(candidate)
            payload = json.dumps({"exists": exists, "size": os.path.getsize(candidate) if exists else None}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        if parsed.path != "/clock":
            self.send_response(404)
            self.end_headers()
            return
        sent = time.time_ns()
        payload = json.dumps({"received_ns": received, "sent_ns": sent}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_):
        pass


def measure(url, samples, output):
    parsed = urllib.parse.urlparse(url)
    connection_type = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    connection = connection_type(parsed.netloc, timeout=10)
    path = (parsed.path.rstrip("/") if parsed.path else "") + "/clock"
    # Establish and warm the persistent proxy connection before measuring it.
    for _ in range(5):
        connection.request("GET", path)
        warm = connection.getresponse()
        warm.read()
    observations = []
    with open(output, "a", buffering=1) as raw:
        for index in range(samples):
            t0 = time.time_ns()
            connection.request("GET", path)
            response = connection.getresponse()
            payload = json.loads(response.read())
            t3 = time.time_ns()
            t1, t2 = payload["received_ns"], payload["sent_ns"]
            record = {
                "sample": index + 1,
                "t0_client_ns": t0,
                "t1_server_recv_ns": t1,
                "t2_server_send_ns": t2,
                "t3_client_ns": t3,
                "delay_ns": (t3 - t0) - (t2 - t1),
                "offset_ns": ((t1 - t0) + (t2 - t3)) // 2,
            }
            observations.append(record)
            raw.write(json.dumps(record) + "\n")
            time.sleep(0.01)
        best = min(observations, key=lambda item: item["delay_ns"])
        raw.write(
            json.dumps(
                {
                    "kind": "clock_offset_summary",
                    "samples": samples,
                    "offset_ms": best["offset_ns"] / 1e6,
                    "min_delay_ms": best["delay_ns"] / 1e6,
                    "uncertainty_ms": best["delay_ns"] / 2e6,
                }
            )
            + "\n"
        )
    print(json.dumps({"offset_ms": best["offset_ns"] / 1e6, "min_delay_ms": best["delay_ns"] / 1e6}))
    connection.close()


if __name__ == "__main__":
    if len(sys.argv) in {3, 4} and sys.argv[1] == "serve":
        Handler.mirror_root = sys.argv[3] if len(sys.argv) == 4 else None
        http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[2])), Handler).serve_forever()
    elif len(sys.argv) == 5 and sys.argv[1] == "measure":
        measure(sys.argv[2], int(sys.argv[3]), sys.argv[4])
    else:
        raise SystemExit("usage: http-clock.py serve PORT [MIRROR_ROOT] | measure URL SAMPLES OUTPUT")
