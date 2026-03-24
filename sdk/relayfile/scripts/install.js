#!/usr/bin/env node

const { execSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const os = require("os");
const https = require("https");

const VERSION = require("../package.json").version;
const BIN_DIR = path.join(__dirname, "..", "bin");
const BIN_PATH = path.join(BIN_DIR, "relayfile");

const PLATFORM_MAP = {
  darwin: "darwin",
  linux: "linux",
  win32: "windows",
};

const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

function getDownloadUrl() {
  const platform = PLATFORM_MAP[os.platform()];
  const arch = ARCH_MAP[os.arch()];

  if (!platform || !arch) {
    console.error(
      `Unsupported platform: ${os.platform()} ${os.arch()}`
    );
    process.exit(1);
  }

  const ext = platform === "windows" ? ".exe" : "";
  return `https://github.com/AgentWorkforce/relayfile/releases/download/v${VERSION}/relayfile-${platform}-${arch}${ext}`;
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
  const url = getDownloadUrl();

  fs.mkdirSync(BIN_DIR, { recursive: true });

  console.log(`Downloading relayfile v${VERSION}...`);
  try {
    await download(url, BIN_PATH);
    fs.chmodSync(BIN_PATH, 0o755);
    console.log("relayfile installed successfully.");
  } catch (err) {
    console.error(`Failed to download relayfile: ${err.message}`);
    console.error("You can install manually from https://github.com/AgentWorkforce/relayfile/releases");
    process.exit(1);
  }
}

main();
