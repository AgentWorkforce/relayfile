"use strict";

// Tests for the vendored Agent Relay Cloud SDK bundle that ships in this
// package. The preflight *logic* that drives it now lives in
// @relayfile/sdk/relay-cli (single-homed so `relayfile` and
// `agent-relay file` behave identically) and is tested there, in
// packages/sdk/typescript/src/relay-cli/cloud-preflight.test.ts. What is
// tested here is the bundle itself plus this shim's use of it.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const bundlePath = path.join(__dirname, "cloud-auth.cjs");

test("the vendored bundle is present for the preflight to load", () => {
  assert.equal(fs.existsSync(bundlePath), true);
  assert.equal(
    typeof require(bundlePath).ensureCloudSession,
    "function",
  );
});

test("bundled SDK carries the Relayfile marker through both login modes", () => {
  const bundledSdk = fs.readFileSync(bundlePath, "utf8");
  assert.match(
    bundledSdk,
    /loginUrl\.searchParams\.set\("client", options\.client\)/,
  );
  assert.match(bundledSdk, /clientName: options\.client/);
  assert.match(bundledSdk, /signal: options\.signal/);
  assert.match(bundledSdk, /throwIfAborted\(options\.signal\)/);
});

test("bundled SDK aborts device polling without issuing or storing credentials", () => {
  const script = `
    const { ensureCloudSession } = require(${JSON.stringify(bundlePath)});
    const controller = new AbortController();
    let fetchCalls = 0;
    global.fetch = async () => {
      fetchCalls += 1;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          device_code: "device-test",
          user_code: "TEST-CODE",
          verification_uri: "https://example.test/device",
          expires_in: 600,
          interval: 5,
        }),
      };
    };
    console.log = () => {};
    const auth = ensureCloudSession({
      apiUrl: "https://example.test/cloud",
      client: "relayfile",
      device: true,
      force: true,
      signal: controller.signal,
    });
    setTimeout(() => controller.abort(new Error("preflight cancelled")), 10);
    auth.then(
      () => process.exit(2),
      (error) => process.exit(error.message === "preflight cancelled" && fetchCalls === 1 ? 0 : 3),
    );
  `;
  const result = spawnSync(process.execPath, ["-e", script], {
    encoding: "utf8",
    timeout: 2000,
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
});

test("bundled SDK handles browser-launch errors and honors the login timeout", () => {
  const script = `
    const os = require("node:os");
    os.platform = () => "linux";
    process.env.PATH = "";
    const { ensureCloudSession } = require(${JSON.stringify(bundlePath)});
    console.log = () => {};
    const startedAt = Date.now();
    ensureCloudSession({
      apiUrl: "https://example.test/cloud",
      client: "relayfile",
      interactive: true,
      device: false,
      force: true,
      env: { DISPLAY: ":99" },
      loginTimeoutMs: 25,
    }).then(
      () => process.exit(2),
      (error) => {
        const elapsedMs = Date.now() - startedAt;
        const passed =
          error.message === "Timed out waiting for browser login" &&
          elapsedMs < 1000;
        setTimeout(() => process.exit(passed ? 0 : 3), 25);
      },
    );
  `;
  const result = spawnSync(process.execPath, ["-e", script], {
    encoding: "utf8",
    timeout: 2000,
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
});

test("the SDK preflight keeps canonical auth out of the child environment", () => {
  // End-to-end through the real preflight in @relayfile/sdk/relay-cli, loading
  // the real bundle from this package, against a real on-disk session.
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "relayfile-cloud-sdk-"));
  const authDir = path.join(home, ".agentworkforce", "relay");
  fs.mkdirSync(authDir, { recursive: true, mode: 0o700 });
  const authPath = path.join(authDir, "cloud-auth.json");
  fs.writeFileSync(
    authPath,
    `${JSON.stringify({
      apiUrl: "https://cloud.example",
      accessToken: "cld_at_bundle_secret",
      refreshToken: "cld_rt_bundle_secret",
      accessTokenExpiresAt: "2099-08-23T14:00:00Z",
      refreshTokenExpiresAt: "2099-09-23T14:00:00Z",
    })}\n`,
    { mode: 0o600 },
  );

  const script = `
    const fs = require("node:fs");
    import("@relayfile/sdk/relay-cli").then(({ prepareCloudSession }) =>
      prepareCloudSession([], {
        env: process.env,
        cloudAuthBundlePath: ${JSON.stringify(bundlePath)},
      }),
    ).then(() => {
      const stored = JSON.parse(fs.readFileSync(${JSON.stringify(authPath)}, "utf8"));
      console.log(JSON.stringify({
        apiUrl: stored.apiUrl,
        hasAccess: Boolean(process.env.CLOUD_API_ACCESS_TOKEN),
        hasRefresh: Boolean(process.env.CLOUD_API_REFRESH_TOKEN),
      }));
    }).catch((error) => { console.error(error.message); process.exit(1); });
  `;
  const childEnv = { ...process.env, HOME: home };
  for (const name of [
    "CLOUD_API_URL",
    "CLOUD_API_ACCESS_TOKEN",
    "CLOUD_API_REFRESH_TOKEN",
    "CLOUD_API_ACCESS_TOKEN_EXPIRES_AT",
    "CLOUD_API_REFRESH_TOKEN_EXPIRES_AT",
  ]) {
    delete childEnv[name];
  }
  const result = spawnSync(process.execPath, ["-e", script], {
    cwd: __dirname,
    encoding: "utf8",
    env: childEnv,
  });

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), {
    apiUrl: "https://cloud.example",
    hasAccess: false,
    hasRefresh: false,
  });
  assert.doesNotMatch(result.stdout, /cld_[ar]t_bundle_secret/);
});
