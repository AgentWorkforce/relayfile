#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const os = require("os");
const https = require("https");

const VERSION = require("../package.json").version;
const BIN_DIR = path.join(__dirname, "..", "bin");

const PLATFORM_MAP = {
  darwin: "darwin",
  linux: "linux",
  win32: "windows",
};

const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

function getBinaryFilename() {
  return os.platform() === "win32" ? "relayfile.exe" : "relayfile";
}

function getPlatformSuffix() {
  const platform = PLATFORM_MAP[os.platform()];
  const arch = ARCH_MAP[os.arch()];

  if (!platform || !arch) {
    console.error(
      `Unsupported platform: ${os.platform()} ${os.arch()}`
    );
    process.exit(1);
  }

  return `${platform}-${arch}`;
}

function getPackagedBinaryFilename() {
  const suffix = getPlatformSuffix();
  const ext = suffix.startsWith("windows-") ? ".exe" : "";
  return `relayfile-cli-${suffix}${ext}`;
}

function getDownloadUrl() {
  return `https://github.com/AgentWorkforce/relayfile/releases/download/v${VERSION}/${getPackagedBinaryFilename()}`;
}

function isSourceCheckout() {
  const repoRoot = path.resolve(__dirname, "..", "..", "..");
  return (
    fs.existsSync(path.join(repoRoot, "go.mod")) &&
    fs.existsSync(path.join(repoRoot, "cmd", "relayfile-cli"))
  );
}

function download(url, dest) {
  return new Promise((resolve, reject) => {
    const follow = (url) => {
      https.get(url, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          follow(res.headers.location);
          return;
        }
        if (res.statusCode !== 200) {
          reject(new Error(`Download failed: HTTP ${res.statusCode} from ${url}`));
          return;
        }
        const file = fs.createWriteStream(dest);
        res.pipe(file);
        file.on("finish", () => {
          file.close(resolve);
        });
      }).on("error", reject);
    };
    follow(url);
  });
}

async function main() {
  const binPath = path.join(BIN_DIR, getBinaryFilename());

  fs.mkdirSync(BIN_DIR, { recursive: true });

  if (fs.existsSync(binPath)) {
    fs.chmodSync(binPath, 0o755);
    console.log("relayfile binary already installed.");
    return;
  }

  if (isSourceCheckout()) {
    console.log(
      "Skipping relayfile binary install in source checkout; run npm run build --workspace=packages/cli to build package binaries."
    );
    return;
  }

  const packagedBinPath = path.join(BIN_DIR, getPackagedBinaryFilename());

  if (fs.existsSync(packagedBinPath)) {
    fs.copyFileSync(packagedBinPath, binPath);
    fs.chmodSync(binPath, 0o755);
    console.log("relayfile installed from packaged binary.");
    return;
  }

  const url = getDownloadUrl();
  console.log(`Downloading relayfile v${VERSION}...`);
  try {
    await download(url, binPath);
    fs.chmodSync(binPath, 0o755);
    console.log("relayfile installed successfully.");
  } catch (err) {
    console.error(`Failed to download relayfile: ${err.message}`);
    console.error("You can install manually from https://github.com/AgentWorkforce/relayfile/releases");
    process.exit(1);
  }
}

main();
