#!/usr/bin/env python3
"""Measure the realtime-clock offset between two hosts over the Tailscale LAN.

We must NOT assume the sender and receiver clocks agree. A one-way latency of
order 100ms is smaller than the drift two unsynchronised macOS hosts can
accumulate, so an uncorrected cross-host subtraction can produce a negative or
absurd "latency".

Method: NTP's four-timestamp formula over a raw TCP exchange on the Tailscale
LAN, which keeps the round trip in the low-single-digit-millisecond range (an
ssh-based comparison would have an RTT far larger than the signal).

    t0 = client realtime before send
    t1 = server realtime on receipt   (server sends t1 and t2)
    t2 = server realtime before reply
    t3 = client realtime after receive

    delay  = (t3 - t0) - (t2 - t1)          # round-trip minus server think time
    offset = ((t1 - t0) + (t2 - t3)) / 2    # server_clock - client_clock

Both formulas assume a symmetric path. That assumption is weakest when the
network is congested, so we take many samples and select the one with the
SMALLEST delay -- the least-queued sample is the least asymmetric. The residual
uncertainty on that offset is bounded by +/- delay/2, which we report so the
final latency number can be quoted with an honest error bar.

Usage:
    clock-offset.py server [BIND_HOST] [PORT]
    clock-offset.py client HOST PORT SAMPLES OUTPUT_JSONL
"""

import json
import socket
import sys
import time


def serve(bind_host, port):
    """Reply to each probe with receipt and reply realtime timestamps."""
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind((bind_host, port))
    listener.listen(8)
    sys.stderr.write(f"clock-offset server listening on {bind_host}:{port}\n")
    sys.stderr.flush()

    while True:
        connection, _ = listener.accept()
        connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        with connection:
            stream = connection.makefile("rwb")
            for line in stream:
                if not line.strip():
                    continue
                receipt_ns = time.time_ns()
                if line.strip() == b"QUIT":
                    return
                reply_ns = time.time_ns()
                stream.write(f"{receipt_ns} {reply_ns}\n".encode())
                stream.flush()


def measure(host, port, samples, output_path):
    """Take `samples` NTP-style exchanges and keep the minimum-delay estimate."""
    connection = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    connection.settimeout(10)
    connection.connect((host, port))
    stream = connection.makefile("rwb")

    observations = []
    with open(output_path, "a") as raw:
        for index in range(samples):
            t0 = time.time_ns()
            stream.write(b"PROBE\n")
            stream.flush()
            reply = stream.readline()
            t3 = time.time_ns()
            if not reply:
                raise RuntimeError("clock-offset server closed the connection early")

            t1_text, t2_text = reply.split()
            t1 = int(t1_text)
            t2 = int(t2_text)

            delay_ns = (t3 - t0) - (t2 - t1)
            offset_ns = ((t1 - t0) + (t2 - t3)) // 2
            observation = {
                "sample": index + 1,
                "t0_client_ns": t0,
                "t1_server_recv_ns": t1,
                "t2_server_send_ns": t2,
                "t3_client_ns": t3,
                "delay_ns": delay_ns,
                "offset_ns": offset_ns,
            }
            observations.append(observation)
            raw.write(json.dumps(observation) + "\n")
            raw.flush()
            time.sleep(0.005)

    stream.write(b"QUIT\n")
    stream.flush()
    connection.close()

    best = min(observations, key=lambda obs: obs["delay_ns"])
    offsets = sorted(obs["offset_ns"] for obs in observations)
    summary = {
        "kind": "clock_offset_summary",
        "host": host,
        "samples": len(observations),
        # server_clock - client_clock, from the least-queued exchange
        "offset_ns": best["offset_ns"],
        "offset_ms": best["offset_ns"] / 1e6,
        "min_delay_ns": best["delay_ns"],
        "min_delay_ms": best["delay_ns"] / 1e6,
        # symmetry assumption residual: the true offset lies within +/- delay/2
        "uncertainty_ms": best["delay_ns"] / 2e6,
        "offset_median_ms": offsets[len(offsets) // 2] / 1e6,
        "offset_spread_ms": (offsets[-1] - offsets[0]) / 1e6,
        "measured_at_client_utc": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime()
        ),
    }
    with open(output_path, "a") as raw:
        raw.write(json.dumps(summary) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    if len(sys.argv) >= 2 and sys.argv[1] == "server":
        serve(
            sys.argv[2] if len(sys.argv) > 2 else "0.0.0.0",
            int(sys.argv[3]) if len(sys.argv) > 3 else 19299,
        )
    elif len(sys.argv) == 6 and sys.argv[1] == "client":
        measure(sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), sys.argv[5])
    else:
        sys.stderr.write(__doc__)
        sys.exit(2)
