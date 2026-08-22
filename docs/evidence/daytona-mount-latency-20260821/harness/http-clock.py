#!/usr/bin/env python3
"""NTP-style clock-offset anchors transported through an HTTP endpoint."""

import http.server
import http.client
import json
import os
import sys
import time
import urllib.parse


def integer_median(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return (values[middle - 1] + values[middle]) // 2


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
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
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
    if samples <= 0:
        raise ValueError("samples must be positive")
    parsed = urllib.parse.urlparse(url)
    connection_type = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    if not parsed.hostname:
        raise ValueError("measurement URL must include a hostname")
    connection = connection_type(parsed.hostname, port=parsed.port, timeout=10)
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
        offsets = [item["offset_ns"] for item in observations]
        delays = [item["delay_ns"] for item in observations]
        offset_ns = integer_median(offsets)
        median_absolute_deviation_ns = integer_median(
            [abs(value - offset_ns) for value in offsets]
        )
        min_delay_ns = min(delays)
        uncertainty_ns = max(
            min_delay_ns // 2,
            abs(min(offsets) - offset_ns),
            abs(max(offsets) - offset_ns),
        )
        raw.write(
            json.dumps(
                {
                    "kind": "clock_offset_summary",
                    "samples": samples,
                    "estimator": "median_offset",
                    "offset_ms": offset_ns / 1e6,
                    "offset_min_ms": min(offsets) / 1e6,
                    "offset_max_ms": max(offsets) / 1e6,
                    "offset_mad_ms": median_absolute_deviation_ns / 1e6,
                    "min_delay_ms": min_delay_ns / 1e6,
                    "uncertainty_ms": uncertainty_ns / 1e6,
                }
            )
            + "\n"
        )
    print(json.dumps({"offset_ms": offset_ns / 1e6, "min_delay_ms": min_delay_ns / 1e6}))
    connection.close()


if __name__ == "__main__":
    if len(sys.argv) in {3, 4} and sys.argv[1] == "serve":
        Handler.mirror_root = sys.argv[3] if len(sys.argv) == 4 else None
        http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[2])), Handler).serve_forever()
    elif len(sys.argv) == 5 and sys.argv[1] == "measure":
        measure(sys.argv[2], int(sys.argv[3]), sys.argv[4])
    else:
        raise SystemExit("usage: http-clock.py serve PORT [MIRROR_ROOT] | measure URL SAMPLES OUTPUT")
