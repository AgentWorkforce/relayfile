#!/usr/bin/env node

const { spawnSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const PLATFORM_MAP = {
  darwin: "darwin",
  linux: "linux",
  win32: "windows",
};

const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

function getPlatformBinaryName() {
  const platform = PLATFORM_MAP[os.platform()];
  const arch = ARCH_MAP[os.arch()];

  if (!platform || !arch) {
    return null;
  }

  const ext = platform === "windows" ? ".exe" : "";
  return `relayfile-cli-${platform}-${arch}${ext}`;
}

const genericBinName = os.platform() === "win32" ? "relayfile.exe" : "relayfile";
const packagedBinName = getPlatformBinaryName();
const candidates = [
  path.join(__dirname, "..", "bin", genericBinName),
  packagedBinName && path.join(__dirname, "..", "bin", packagedBinName),
].filter(Boolean);

const binPath = candidates.find((candidate) => fs.existsSync(candidate));

if (!binPath) {
  console.error(
    `relayfile binary not found for ${os.platform()} ${os.arch()}. Reinstall the package or run postinstall again.`
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
