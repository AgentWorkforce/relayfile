"use strict";

// The bin shim must keep working unchanged after binary resolution and the
// Cloud preflight moved into @relayfile/sdk/relay-cli. These tests run the
// real shim.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const shimPath = path.join(__dirname, "run.js");
const { version } = require("../package.json");

test("--version short-circuits before any binary lookup", () => {
  // No binary and no Go toolchain are required for this path; it must answer
  // from package.json alone.
  const result = spawnSync(process.execPath, [shimPath, "--version"], {
    encoding: "utf8",
    env: { ...process.env, PATH: "" },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${version}\n`);
});

test("the shim owns no copy of binary resolution or the Cloud preflight", () => {
  // Both implementations live in @relayfile/sdk/relay-cli. If either is
  // reimplemented here, the two entry points can diverge.
  const source = fs.readFileSync(shimPath, "utf8");
  assert.match(source, /@relayfile\/sdk\/relay-cli/);
  assert.match(source, /resolveRelayfileBinary/);
  assert.match(source, /prepareCloudSession/);
  assert.doesNotMatch(source, /PLATFORM_MAP|ARCH_MAP/);
  assert.doesNotMatch(source, /relayfile-cli-\$\{|relayfile-cli-linux/);
  assert.doesNotMatch(source, /ensureCloudSession\(/);
});

test("the removed preflight module is not reintroduced", () => {
  assert.equal(fs.existsSync(path.join(__dirname, "cloud-preflight.js")), false);
});

test("a real command runs through the shim and returns the binary's output", () => {
  // Executes the actual Go binary (built, or via `go run` in a checkout).
  const result = spawnSync(process.execPath, [shimPath, "status", "--help"], {
    encoding: "utf8",
    timeout: 300000,
  });
  if (
    result.status !== 0 &&
    /Go is not installed|could not be loaded/.test(
      `${result.stderr}${result.stdout}`,
    )
  ) {
    assert.fail(
      `the shim could not reach the relayfile binary: ${result.stderr || result.stdout}`,
    );
  }
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Usage: relayfile status/);
});
