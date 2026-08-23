"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const {
  prepareCloudSession,
  shouldPrepareCloudSession,
} = require("./cloud-preflight.js");

test("bare relayfile prepares Cloud auth through the Agent Relay SDK", async () => {
  const env = {};
  const calls = [];
  const prepared = await prepareCloudSession([], env, {
    ensureCloudSession: async (options) => {
      calls.push(options);
      return {
        auth: {
          apiUrl: "https://cloud.example",
          accessToken: "cld_at_test_secret",
          refreshToken: "cld_rt_test_secret",
          accessTokenExpiresAt: "2026-08-23T14:00:00Z",
          refreshTokenExpiresAt: "2026-09-23T14:00:00Z",
        },
      };
    },
  });

  assert.equal(prepared, true);
  assert.deepEqual(calls, [
    {
      apiUrl: "https://agentrelay.com/cloud",
      interactive: true,
      device: false,
    },
  ]);
  assert.equal(env.CLOUD_API_ACCESS_TOKEN, "cld_at_test_secret");
  assert.equal(env.CLOUD_API_REFRESH_TOKEN, "cld_rt_test_secret");
  assert.equal(env.CLOUD_API_URL, "https://cloud.example");
});

test("setup forwards its Cloud URL and no-open mode to the SDK", async () => {
  const env = {};
  let received;
  await prepareCloudSession(
    [
      "setup",
      "--cloud-api-url=https://staging.example/cloud",
      "--no-open",
    ],
    env,
    {
      ensureCloudSession: async (options) => {
        received = options;
        return {
          auth: {
            apiUrl: options.apiUrl,
            accessToken: "access",
            refreshToken: "refresh",
            accessTokenExpiresAt: "2026-08-23T14:00:00Z",
          },
        };
      },
    },
  );

  assert.deepEqual(received, {
    apiUrl: "https://staging.example/cloud",
    interactive: true,
    device: true,
  });
  assert.equal(env.CLOUD_API_REFRESH_TOKEN_EXPIRES_AT, undefined);
});

test("help and caller-owned tokens do not start interactive auth", () => {
  assert.equal(shouldPrepareCloudSession(["setup", "--help"], {}), false);
  assert.equal(
    shouldPrepareCloudSession(["setup", "--cloud-token", "explicit"], {}),
    false,
  );
  assert.equal(
    shouldPrepareCloudSession([], { CLOUD_API_ACCESS_TOKEN: "ci-token" }),
    false,
  );
  assert.equal(shouldPrepareCloudSession(["status"], {}), false);
});

test("the bundled SDK reuses canonical auth without exposing tokens", () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "relayfile-cloud-sdk-"));
  const authDir = path.join(home, ".agentworkforce", "relay");
  fs.mkdirSync(authDir, { recursive: true, mode: 0o700 });
  fs.writeFileSync(
    path.join(authDir, "cloud-auth.json"),
    `${JSON.stringify({
      apiUrl: "https://cloud.example",
      accessToken: "cld_at_bundle_secret",
      refreshToken: "cld_rt_bundle_secret",
      accessTokenExpiresAt: "2099-08-23T14:00:00Z",
      refreshTokenExpiresAt: "2099-09-23T14:00:00Z",
    })}\n`,
    { mode: 0o600 },
  );

  const modulePath = path.join(__dirname, "cloud-preflight.js");
  const script = `
    const { prepareCloudSession } = require(${JSON.stringify(modulePath)});
    prepareCloudSession([], process.env).then(() => {
      console.log(JSON.stringify({
        apiUrl: process.env.CLOUD_API_URL,
        hasAccess: Boolean(process.env.CLOUD_API_ACCESS_TOKEN),
        hasRefresh: Boolean(process.env.CLOUD_API_REFRESH_TOKEN),
      }));
    }).catch((error) => { console.error(error.message); process.exit(1); });
  `;
  const result = spawnSync(process.execPath, ["-e", script], {
    encoding: "utf8",
    env: { ...process.env, HOME: home },
  });

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), {
    apiUrl: "https://cloud.example",
    hasAccess: true,
    hasRefresh: true,
  });
  assert.doesNotMatch(result.stdout, /cld_[ar]t_bundle_secret/);
});
