#!/usr/bin/env python3
"""Measure local CLOCK_REALTIME against a public SNTP reference clock."""

import json
import socket
import struct
import sys
import time

NTP_EPOCH_SECONDS = 2_208_988_800


def ntp_to_unix_ns(seconds, fraction):
    return int((seconds - NTP_EPOCH_SECONDS) * 1_000_000_000 + fraction * 1_000_000_000 / 2**32)


def sample(sock, address):
    request = bytearray(48)
    request[0] = 0x23  # leap=0, version=4, client mode
    t0 = time.time_ns()
    sock.sendto(request, address)
    response, _ = sock.recvfrom(512)
    t3 = time.time_ns()
    if len(response) < 48:
        raise RuntimeError("short SNTP response")
    fields = struct.unpack("!12I", response[:48])
    t1 = ntp_to_unix_ns(fields[8], fields[9])
    t2 = ntp_to_unix_ns(fields[10], fields[11])
    return {
        "t0_client_ns": t0,
        "t1_server_recv_ns": t1,
        "t2_server_send_ns": t2,
        "t3_client_ns": t3,
        "delay_ns": (t3 - t0) - (t2 - t1),
        "offset_ns": ((t1 - t0) + (t2 - t3)) // 2,
    }


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: sntp-clock.py HOST SAMPLES OUTPUT HOST_ALIAS")
    host, sample_count, output, host_alias = sys.argv[1:]
    sample_count = int(sample_count)
    address = socket.getaddrinfo(host, 123, socket.AF_INET, socket.SOCK_DGRAM)[0][4]
    observations = []
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock, open(output, "a", buffering=1) as raw:
        sock.settimeout(5)
        for index in range(sample_count):
            record = sample(sock, address)
            record.update({"sample": index + 1, "host": host_alias, "reference": host})
            observations.append(record)
            raw.write(json.dumps(record) + "\n")
            time.sleep(0.05)
        best = min(observations, key=lambda item: item["delay_ns"])
        summary = {
            "kind": "sntp_summary",
            "host": host_alias,
            "reference": host,
            "samples": sample_count,
            "offset_ms": best["offset_ns"] / 1e6,
            "min_delay_ms": best["delay_ns"] / 1e6,
            "uncertainty_ms": best["delay_ns"] / 2e6,
        }
        raw.write(json.dumps(summary) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
