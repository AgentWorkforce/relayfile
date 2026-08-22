#!/usr/bin/env python3
"""Measure local CLOCK_REALTIME against a public SNTP reference clock."""

import json
import socket
import struct
import sys
import time

NTP_EPOCH_SECONDS = 2_208_988_800


def integer_median(values):
    values = sorted(values)
    middle = len(values) // 2
    if len(values) % 2:
        return values[middle]
    return (values[middle - 1] + values[middle]) // 2


def ntp_to_unix_ns(seconds, fraction):
    return int((seconds - NTP_EPOCH_SECONDS) * 1_000_000_000 + fraction * 1_000_000_000 / 2**32)


def unix_ns_to_ntp(value):
    seconds, nanoseconds = divmod(value, 1_000_000_000)
    return seconds + NTP_EPOCH_SECONDS, (nanoseconds << 32) // 1_000_000_000


def sample(sock, address):
    request = bytearray(48)
    request[0] = 0x23  # leap=0, version=4, client mode
    t0 = time.time_ns()
    transmit = unix_ns_to_ntp(t0)
    struct.pack_into("!II", request, 40, *transmit)
    sock.sendto(request, address)
    response, source = sock.recvfrom(512)
    t3 = time.time_ns()
    if len(response) < 48:
        raise RuntimeError("short SNTP response")
    fields = struct.unpack("!12I", response[:48])
    leap = response[0] >> 6
    version = (response[0] >> 3) & 0x07
    mode = response[0] & 0x07
    stratum = response[1]
    if source[0] != address[0] or source[1] != address[1]:
        raise RuntimeError("SNTP response source did not match request destination")
    if leap == 3 or version not in {3, 4} or mode != 4 or not 1 <= stratum <= 15:
        raise RuntimeError("invalid or unsynchronized SNTP server response")
    if (fields[6], fields[7]) != transmit:
        raise RuntimeError("SNTP originate timestamp did not match request")
    if fields[10] == 0 and fields[11] == 0:
        raise RuntimeError("SNTP response had a zero transmit timestamp")
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
    if sample_count <= 0:
        raise SystemExit("SAMPLES must be positive")
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
        offsets = [item["offset_ns"] for item in observations]
        delays = [item["delay_ns"] for item in observations]
        offset_ns = integer_median(offsets)
        min_delay_ns = min(delays)
        uncertainty_ns = max(
            min_delay_ns // 2,
            abs(min(offsets) - offset_ns),
            abs(max(offsets) - offset_ns),
        )
        summary = {
            "kind": "sntp_summary",
            "host": host_alias,
            "reference": host,
            "samples": sample_count,
            "estimator": "median_offset",
            "offset_ms": offset_ns / 1e6,
            "offset_min_ms": min(offsets) / 1e6,
            "offset_max_ms": max(offsets) / 1e6,
            "min_delay_ms": min_delay_ns / 1e6,
            "uncertainty_ms": uncertainty_ns / 1e6,
        }
        raw.write(json.dumps(summary) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
