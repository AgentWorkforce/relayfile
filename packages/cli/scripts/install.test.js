"use strict";

// postinstall runs during `npm install`, which in a fresh clone is before
// packages/sdk/typescript/dist exists. It shipped once importing
// @relayfile/sdk/relay-cli before it could detect the checkout and skip, so a
// clone with no built SDK died in postinstall and `npm install` never
// completed.
//
// These tests run the real script inside a directory shaped like a fresh
// clone: the two checkout markers, no node_modules, no SDK dist anywhere above
// it. Nothing is stubbed, and the host checkout cannot make them pass.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const installScript = path.join(__dirname, "install.js");

/**
 * Build a throwaway tree holding a copy of install.js.
 *
 * @param {string} label - Included in the directory name.
 * @param {{ goMod?: boolean, cmdDir?: boolean }} markers - Which checkout
 *   markers to create at the root.
 * @returns {{ root: string, script: string }} The tree root and the script
 *   copy to run.
 */
function fakeClone(label, markers = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), `relayfile-install-${label}-`));
  const scripts = path.join(root, "packages", "cli", "scripts");
  fs.mkdirSync(scripts, { recursive: true });

  if (markers.goMod !== false) {
    fs.writeFileSync(path.join(root, "go.mod"), "module github.com/example/relayfile\n");
  }
  if (markers.cmdDir !== false) {
    fs.mkdirSync(path.join(root, "cmd", "relayfile-cli"), { recursive: true });
  }

  const script = path.join(scripts, "install.js");
  fs.copyFileSync(installScript, script);
  fs.writeFileSync(
    path.join(root, "packages", "cli", "package.json"),
    JSON.stringify({ name: "relayfile", version: "0.0.0-test" }, null, 2),
  );

  // A fresh clone's state: no node_modules, so no @relayfile/sdk to import.
  assert.equal(fs.existsSync(path.join(root, "node_modules")), false);

  return { root, script };
}

/**
 * Run a copied install.js with the caller's environment sealed off.
 *
 * `PATH` is emptied so nothing can shell out, and the temp root is the cwd, so
 * the only module resolution paths are inside the fake clone.
 *
 * @param {string} script - The copied script.
 * @param {string} root - Its tree root, used as cwd.
 * @returns {{ status: number, stdout: string, stderr: string }} The result.
 */
function runInstall(script, root) {
  const result = spawnSync(process.execPath, [script], {
    cwd: root,
    encoding: "utf8",
    timeout: 60000,
    env: { PATH: "", HOME: root },
  });
  if (result.error) {
    throw result.error;
  }
  return {
    status: result.status,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

test("skips the download in a source checkout with no SDK dist", () => {
  // The bug: install.js imported @relayfile/sdk/relay-cli before this check,
  // so this exited 1 and took `npm install` down with it.
  const { root, script } = fakeClone("fresh-clone");
  const result = runInstall(script, root);

  assert.equal(result.status, 0, `${result.stdout}${result.stderr}`);
  assert.match(result.stdout, /Skipping relayfile binary install in source checkout/);
  assert.doesNotMatch(result.stderr, /could not be loaded/);
  // It must not have reached the download path either.
  assert.doesNotMatch(result.stdout, /Downloading relayfile/);
});

test("creates no bin/ directory when it skips", () => {
  // The skip happens before any filesystem setup, so a clone stays clean.
  const { root, script } = fakeClone("no-bin");
  const result = runInstall(script, root);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.existsSync(path.join(root, "packages", "cli", "bin")), false);
});

test("requires both checkout markers before skipping", () => {
  // go.mod alone is some other Go project, not a relayfile checkout. Without
  // the SDK there is nothing to fall back to, so it must report the SDK load
  // failure rather than silently skip a real install.
  for (const markers of [{ cmdDir: false }, { goMod: false }]) {
    const { root, script } = fakeClone("partial", markers);
    const result = runInstall(script, root);

    assert.equal(result.status, 1, `${result.stdout}${result.stderr}`);
    assert.match(result.stderr, /@relayfile\/sdk\/relay-cli could not be loaded/);
    assert.doesNotMatch(result.stdout, /Skipping relayfile binary install/);
  }
});

test("detects a checkout the same way the SDK does", async () => {
  // install.js carries its own copy of the predicate because it runs before
  // the SDK is built. This pins the copy to the original.
  const { findSourceCheckoutRoot } = await import("@relayfile/sdk/relay-cli");
  const { root, script } = fakeClone("parity");

  assert.equal(findSourceCheckoutRoot(path.dirname(script)), root);
  assert.equal(findSourceCheckoutRoot(os.tmpdir()), null);

  const partial = fakeClone("parity-partial", { cmdDir: false });
  assert.equal(findSourceCheckoutRoot(path.dirname(partial.script)), null);
});

test("the checkout skip precedes the SDK import", () => {
  // Order is the whole fix: a source-checkout skip that needs the SDK built
  // cannot run on a fresh clone.
  const source = fs.readFileSync(installScript, "utf8");
  const skip = source.indexOf("findSourceCheckoutRoot(__dirname)");
  const load = source.indexOf("await loadRelayCli()");
  assert.ok(skip !== -1 && load !== -1);
  assert.ok(skip < load, "the source-checkout skip must come before loadRelayCli()");
});
