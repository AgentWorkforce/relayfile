#!/usr/bin/env node

// The `relayfile` bin shim. It resolves the Go binary and prepares the Cloud
// session, but owns neither implementation: both live in @relayfile/sdk's
// relay-cli module, which `agent-relay file` mounts as its CLI surface. Keeping
// them there means "find the relayfile binary" and "prepare Cloud auth" exist
// once in this repo, and the two entry points cannot diverge.

const { spawnSync } = require("child_process");
const os = require("os");
const path = require("path");

const args = process.argv.slice(2);
if (args[0] === "--version") {
  const { version } = require("../package.json");
  process.stdout.write(`${version}\n`);
  process.exit(0);
}

const SDK_BUILD_HINT =
  "@relayfile/sdk/relay-cli could not be loaded. In a source checkout, build it first:\n" +
  "  npm run build --workspace=packages/sdk/typescript";

async function loadRelayCli() {
  try {
    return await import("@relayfile/sdk/relay-cli");
  } catch (error) {
    const code = error && error.code;
    if (
      code === "ERR_MODULE_NOT_FOUND" ||
      code === "MODULE_NOT_FOUND" ||
      code === "ERR_PACKAGE_PATH_NOT_EXPORTED"
    ) {
      console.error(SDK_BUILD_HINT);
      process.exit(1);
    }
    throw error;
  }
}

async function main() {
  const relayCli = await loadRelayCli();

  // Agent Relay's Cloud SDK owns interactive login, token refresh, locking,
  // and the canonical session store. The native runtime reads that same store
  // directly instead of receiving copied tokens or invoking agent-relay CLI.
  relayCli.announceSetupIntent(args, process.env);
  await relayCli.prepareCloudSession(args, {
    env: process.env,
    // This package vendors the Cloud SDK bundle; hand the resolver the exact
    // path rather than making it search for it.
    cloudAuthBundlePath: path.join(__dirname, "cloud-auth.cjs"),
  });

  let resolution;
  try {
    resolution = relayCli.resolveRelayfileBinary({
      binDirs: [path.join(__dirname, "..", "bin")],
      searchFrom: [__dirname],
    });
  } catch (error) {
    if (error instanceof relayCli.RelayfileBinaryNotFoundError) {
      console.error(error.message);
      process.exit(1);
    }
    throw error;
  }

  // stdio is inherited rather than piped: this shim is the terminal-facing
  // entry point, so relayfile's own output (including binary payloads from
  // `export --output -`) must pass through untouched.
  const result = spawnSync(
    resolution.command,
    [...resolution.args, ...args],
    {
      cwd: resolution.kind === "go-run" ? resolution.cwd : undefined,
      stdio: "inherit",
    }
  );

  if (result.error) {
    if (result.error.code === "ENOENT" && resolution.kind === "go-run") {
      console.error(relayCli.GO_TOOLCHAIN_MISSING_MESSAGE);
      process.exit(1);
    }
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
}

main().catch((error) => {
  const detail = error instanceof Error ? error.message : String(error);
  console.error(`Relayfile Cloud sign-in failed: ${detail}`);
  process.exit(1);
});
