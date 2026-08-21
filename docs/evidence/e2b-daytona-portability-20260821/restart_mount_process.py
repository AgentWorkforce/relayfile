#!/usr/bin/env python3
"""Stop and relaunch an exact mount argv without printing its credential."""

import argparse
import glob
import json
import os
import signal
import subprocess


def find_process(target):
    matches = []
    for path in glob.glob("/proc/[0-9]*/cmdline"):
        try:
            with open(path, "rb") as handle:
                argv = [part.decode() for part in handle.read().split(b"\0") if part]
        except (OSError, UnicodeDecodeError):
            continue
        if argv and argv[0] == target:
            matches.append((int(path.split("/")[2]), argv))
    if len(matches) != 1:
        raise SystemExit(f"expected one {target} process, found {len(matches)}")
    return matches[0]


def stop(target, state_file):
    pid, argv = find_process(target)
    descriptor = os.open(state_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        json.dump(argv, handle)
    os.kill(pid, signal.SIGTERM)
    print(json.dumps({"stopped_pid": pid}, separators=(",", ":")))


def start(state_file, log_file):
    with open(state_file) as handle:
        argv = json.load(handle)
    descriptor = os.open(log_file, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(descriptor, "ab", closefd=True) as log:
        process = subprocess.Popen(
            argv,
            stdin=subprocess.DEVNULL,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    os.remove(state_file)
    print(json.dumps({"started_pid": process.pid}, separators=(",", ":")))


def main():
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="action", required=True)
    stop_parser = subparsers.add_parser("stop")
    stop_parser.add_argument("--target", required=True)
    stop_parser.add_argument("--state-file", required=True)
    start_parser = subparsers.add_parser("start")
    start_parser.add_argument("--state-file", required=True)
    start_parser.add_argument("--log-file", required=True)
    args = parser.parse_args()
    if args.action == "stop":
        stop(args.target, args.state_file)
    else:
        start(args.state_file, args.log_file)


if __name__ == "__main__":
    main()
