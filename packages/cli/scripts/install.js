#!/usr/bin/env node

// postinstall: put a runnable relayfile binary in this package's bin/.
//
// The platform mapping and binary file names come from @relayfile/sdk/relay-cli,
// the same module run.js and `agent-relay file` use, so "which binary is this
// host's" is decided in exactly one place.
//
// Source-checkout detection is the one thing that cannot come from there. This
// script runs during `npm install`, and in a fresh clone that is before
// packages/sdk/typescript/dist exists — importing the SDK to decide whether to
// skip would fail the install before it could skip. So the two checkout markers
// are tested locally, ahead of any SDK load. `install.test.js` pins this
// predicate against the SDK's `findSourceCheckoutRoot`.

const fs = require("fs");
const path = require("path");
const https = require("https");

const VERSION = require("../package.json").version;
const BIN_DIR = path.join(__dirname, "..", "bin");

const SDK_LOAD_HINT =
  "@relayfile/sdk/relay-cli could not be loaded, so the relayfile binary name for this platform " +
  "cannot be resolved. Reinstall relayfile, or in a source checkout run " +
  "`npm run build --workspace=packages/sdk/typescript`.";

async function loadRelayCli() {
  try {
    return await import("@relayfile/sdk/relay-cli");
  } catch (error) {
    console.error(SDK_LOAD_HINT);
    console.error(error && error.message ? error.message : String(error));
    process.exit(1);
  }
}

/**
 * Locate a relayfile source checkout above `start`: a directory with both
 * `go.mod` and `cmd/relayfile-cli`.
 *
 * Same two markers as `findSourceCheckoutRoot` in @relayfile/sdk/relay-cli,
 * duplicated here only because this runs before the SDK is built.
 *
 * @param {string} start - Directory to search upward from.
 * @returns {string|null} The checkout root, or null when there is none.
 */
function findSourceCheckoutRoot(start) {
  let current = path.resolve(start);
  for (;;) {
    if (
      fs.existsSync(path.join(current, "go.mod")) &&
      fs.existsSync(path.join(current, "cmd", "relayfile-cli"))
    ) {
      return current;
    }
    const parent = path.dirname(current);
    if (parent === current) {
      return null;
    }
    current = parent;
  }
}

function getDownloadUrl(packagedBinaryName) {
  return `https://github.com/AgentWorkforce/relayfile/releases/download/v${VERSION}/${packagedBinaryName}`;
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
  // Before the SDK is touched: a fresh clone has no SDK dist/, and failing
  // here would break `npm install` for the whole repo.
  if (findSourceCheckoutRoot(__dirname)) {
    // run.js falls back to `go run ./cmd/relayfile-cli` in a checkout, so the
    // command still works without a downloaded binary.
    console.log(
      "Skipping relayfile binary install in source checkout; run npm run build --workspace=packages/cli to build package binaries."
    );
    return;
  }

  const { genericBinaryName, platformBinaryName } = await loadRelayCli();

  const packagedBinaryName = platformBinaryName();
  if (!packagedBinaryName) {
    console.error(`Unsupported platform: ${process.platform} ${process.arch}`);
    process.exit(1);
  }

  const binPath = path.join(BIN_DIR, genericBinaryName());

  fs.mkdirSync(BIN_DIR, { recursive: true });

  if (fs.existsSync(binPath)) {
    fs.chmodSync(binPath, 0o755);
    console.log("relayfile binary already installed.");
    return;
  }

  const packagedBinPath = path.join(BIN_DIR, packagedBinaryName);

  if (fs.existsSync(packagedBinPath)) {
    fs.copyFileSync(packagedBinPath, binPath);
    fs.chmodSync(binPath, 0o755);
    console.log("relayfile installed from packaged binary.");
    return;
  }

  const url = getDownloadUrl(packagedBinaryName);
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
