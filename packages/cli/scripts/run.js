#!/usr/bin/env node

const { spawnSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const binName = os.platform() === "win32" ? "relayfile.exe" : "relayfile";
const binPath = path.join(__dirname, "..", "bin", binName);

if (!fs.existsSync(binPath)) {
  console.error(
    `relayfile binary not found at ${binPath}. Reinstall the package or run postinstall again.`
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), {
  stdio: "inherit",
});

if (result.error) {
  console.error(`Failed to launch relayfile binary: ${result.error.message}`);
  process.exit(1);
}

if (typeof result.status === "number") {
  process.exit(result.status);
}

process.exit(1);
