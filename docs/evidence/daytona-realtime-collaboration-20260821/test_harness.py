#!/usr/bin/env python3

import base64
import hashlib
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest


HERE = os.path.dirname(__file__)


def load_script(name):
    path = os.path.join(HERE, name)
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HarnessTests(unittest.TestCase):
    def test_load_json_rejects_trailing_data(self):
        analyze = load_script("analyze-concurrency.py")
        with tempfile.NamedTemporaryFile("w", delete=False) as handle:
            handle.write('{"valid": true} trailing')
            path = handle.name
        try:
            with self.assertRaises(json.JSONDecodeError):
                analyze.load_json(path)
        finally:
            os.unlink(path)

    def test_manifest_keeps_nested_relay_and_rejects_output_inside_mount(self):
        script = os.path.join(HERE, "mirror-manifest.py")
        with tempfile.TemporaryDirectory() as mount, tempfile.TemporaryDirectory() as output_dir:
            os.makedirs(os.path.join(mount, ".relay"))
            os.makedirs(os.path.join(mount, "fixture", ".relay"))
            with open(os.path.join(mount, ".relay", "state.json"), "w") as handle:
                handle.write("ignored")
            with open(os.path.join(mount, "fixture", ".relay", "data.txt"), "w") as handle:
                handle.write("kept")
            output = os.path.join(output_dir, "manifest.json")
            subprocess.run([sys.executable, script, mount, output], check=True)
            with open(output) as handle:
                manifest = json.load(handle)
            self.assertEqual([row["path"] for row in manifest["files"]], ["fixture/.relay/data.txt"])
            result = subprocess.run(
                [sys.executable, script, mount, os.path.join(mount, "manifest.json")],
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0)

    def test_convergence_rejects_duplicate_and_escaping_paths(self):
        script = os.path.join(HERE, "convergence-watch.py")
        digest = hashlib.sha256(b"x").hexdigest()
        with tempfile.TemporaryDirectory() as root:
            duplicate = subprocess.run(
                [sys.executable, script, root, os.devnull, "0.01", "x", digest, "x", digest],
                capture_output=True,
            )
            escaping = subprocess.run(
                [sys.executable, script, root, os.devnull, "0.01", "../x", digest],
                capture_output=True,
            )
        self.assertNotEqual(duplicate.returncode, 0)
        self.assertNotEqual(escaping.returncode, 0)

    def test_conflict_capture_preserves_binary_bytes_and_literal_glob_path(self):
        script = os.path.join(HERE, "capture-conflict.py")
        with tempfile.TemporaryDirectory() as mount, tempfile.TemporaryDirectory() as output_dir:
            relative = "work/[agent].bin"
            canonical_path = os.path.join(mount, relative)
            os.makedirs(os.path.dirname(canonical_path))
            canonical = b"\x00\xffcanonical"
            with open(canonical_path, "wb") as handle:
                handle.write(canonical)
            artifact = os.path.join(mount, ".relay", "conflicts", relative + ".rev.local")
            os.makedirs(os.path.dirname(artifact))
            losing = b"\xfe\x00losing"
            with open(artifact, "wb") as handle:
                handle.write(losing)
            output = os.path.join(output_dir, "outcome.json")
            subprocess.run([sys.executable, script, mount, relative, "agent-a", output], check=True)
            with open(output) as handle:
                outcome = json.load(handle)
            self.assertEqual(base64.b64decode(outcome["canonical_base64"]), canonical)
            self.assertEqual(base64.b64decode(outcome["artifacts"][0]["content_base64"]), losing)


if __name__ == "__main__":
    unittest.main()
