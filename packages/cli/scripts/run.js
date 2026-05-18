#!/usr/bin/env node

const { spawnSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const args = process.argv.slice(2);
if (args[0] === "--version") {
  const { version } = require("../package.json");
  process.stdout.write(`${version}\n`);
  process.exit(0);
}

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

// In a source checkout, postinstall intentionally skips building the
// binary. Rather than leaving the installed `relayfile` command unusable,
// fall back to running it straight from Go source.
function sourceCheckoutRoot() {
  const repoRoot = path.resolve(__dirname, "..", "..", "..");
  if (
    fs.existsSync(path.join(repoRoot, "go.mod")) &&
    fs.existsSync(path.join(repoRoot, "cmd", "relayfile-cli"))
  ) {
    return repoRoot;
  }
  return null;
}

let result;
if (binPath) {
  result = spawnSync(binPath, args, { stdio: "inherit" });
} else {
  const repoRoot = sourceCheckoutRoot();
  if (!repoRoot) {
    console.error(
      `relayfile binary not found for ${os.platform()} ${os.arch()}. Reinstall the package or run postinstall again.`
    );
    process.exit(1);
  }
  result = spawnSync("go", ["run", "./cmd/relayfile-cli", ...args], {
    cwd: repoRoot,
    stdio: "inherit",
  });
  if (result.error && result.error.code === "ENOENT") {
    console.error(
      "relayfile binary not found and Go is not installed to run from source. " +
        "Install Go or run `npm run build --workspace=packages/cli`."
    );
    process.exit(1);
  }
}

if (result.error) {
  console.error(`Failed to launch relayfile: ${result.error.message}`);
  process.exit(1);
}

if (typeof result.status === "number") {
  process.exit(result.status);
}

// The child was terminated by a signal: spawnSync reports status === null
// and signal === <name>. Preserve conventional 128 + signal-number exit
// semantics (e.g. 130 for SIGINT) so callers can distinguish user
// cancellation from a generic failure.
if (result.signal) {
  const signum = os.constants.signals[result.signal];
  process.exit(typeof signum === "number" ? 128 + signum : 1);
}

process.exit(1);
