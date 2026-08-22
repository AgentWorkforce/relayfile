#!/usr/bin/env python3

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest


HERE = os.path.dirname(__file__)


def load_script(name):
    path = os.path.join(HERE, name)
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HarnessTests(unittest.TestCase):
    def test_empty_distribution_is_explicit(self):
        combine = load_script("combine-results.py")
        self.assertIsNone(combine.distribution([]))

    def test_anchor_uses_median_and_bounds_offset_range(self):
        analyse = load_script("analyse-local.py")
        with tempfile.NamedTemporaryFile("w", delete=False) as handle:
            path = handle.name
            for index, offset in enumerate((-20_000_000, -19_000_000, 12_000_000), 1):
                handle.write(json.dumps({
                    "sample": index,
                    "t0_client_ns": index * 100_000_000,
                    "t3_client_ns": index * 100_000_000 + 40_000_000,
                    "delay_ns": 40_000_000,
                    "offset_ns": offset,
                }) + "\n")
        try:
            _, offset, _, uncertainty = analyse.anchor(path)
            self.assertEqual(offset, -19_000_000)
            self.assertGreaterEqual(uncertainty, 31_000_000)
        finally:
            os.unlink(path)

    def test_http_clock_measure_honors_explicit_port(self):
        clock = load_script("http-clock.py")
        server = clock.http.server.ThreadingHTTPServer(("127.0.0.1", 0), clock.Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        with tempfile.NamedTemporaryFile(delete=False) as output:
            output_path = output.name
        try:
            clock.measure(f"http://127.0.0.1:{server.server_port}", 3, output_path)
            with open(output_path) as handle:
                summary = json.loads(handle.readlines()[-1])
            self.assertEqual(summary["estimator"], "median_offset")
        finally:
            server.shutdown()
            server.server_close()
            os.unlink(output_path)

    def test_scheduled_write_rejects_symlink_escape(self):
        with tempfile.TemporaryDirectory() as mount, tempfile.TemporaryDirectory() as outside:
            os.symlink(outside, os.path.join(mount, "escape"))
            result = subprocess.run(
                [
                    sys.executable,
                    os.path.join(HERE, "scheduled-write.py"),
                    mount,
                    "escape/probe.txt",
                    "payload",
                    "agent-a",
                    "0",
                ],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(os.path.exists(os.path.join(outside, "probe.txt")))

    def test_local_writer_rejects_dot_slug(self):
        result = subprocess.run(
            [
                sys.executable,
                os.path.join(HERE, "local-writer.py"),
                tempfile.gettempdir(),
                "small",
                ".",
                "a2b",
                "1",
                "0",
                os.devnull,
                "sender",
            ],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
