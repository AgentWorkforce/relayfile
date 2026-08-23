"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const {
  hasValidSetupArguments,
  parseGoDurationMilliseconds,
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
      refreshTimeoutMs: 10000,
    },
  ]);
  assert.deepEqual(env, {});
});

test("setup forwards its Cloud URL and no-open mode to the SDK", async () => {
  const env = {};
  let received;
  await prepareCloudSession(
    [
      "setup",
      "--cloud-api-url=https://staging.example/cloud",
      "--no-open",
      "--login-timeout=10s",
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
    refreshTimeoutMs: 10000,
  });
  assert.deepEqual(env, {});
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

test("malformed setup arguments fail before interactive auth", async () => {
  assert.equal(hasValidSetupArguments(["setup", "--provider"]), false);
  assert.equal(hasValidSetupArguments(["setup", "--unknown"]), false);
  assert.equal(hasValidSetupArguments(["setup", "unexpected"]), false);
  assert.equal(
    shouldPrepareCloudSession(["setup", "--provider"], {}),
    false,
  );
  assert.equal(
    shouldPrepareCloudSession(["setup", "--unknown"], {}),
    false,
  );
  let authCalls = 0;
  await assert.rejects(
    prepareCloudSession(
      ["setup", "--provider"],
      {},
      {
        ensureCloudSession: async () => {
          authCalls += 1;
          throw new Error("interactive auth must not run");
        },
      },
    ),
    /--provider requires a value/,
  );
  assert.equal(authCalls, 0);
});

test("invalid setup values fail before interactive auth", async () => {
  assert.equal(
    hasValidSetupArguments(["setup", "--connect-timeout=bogus"]),
    false,
  );
  assert.equal(
    hasValidSetupArguments(["setup", "--backend", "invalid"]),
    false,
  );
  let authCalls = 0;
  await assert.rejects(
    prepareCloudSession(
      ["setup", "--backend", "invalid"],
      {},
      {
        ensureCloudSession: async () => {
          authCalls += 1;
        },
      },
    ),
    /unsupported integration backend/,
  );
  assert.equal(authCalls, 0);
});

test("Go durations are validated and converted for SDK login", () => {
  assert.equal(parseGoDurationMilliseconds("10s"), 10000);
  assert.equal(parseGoDurationMilliseconds("1m30.5s"), 90500);
  assert.equal(parseGoDurationMilliseconds("250ms"), 250);
  assert.equal(parseGoDurationMilliseconds("bogus"), null);
  assert.equal(parseGoDurationMilliseconds("10"), null);
});

test("login timeout bounds SDK authentication", async () => {
  await assert.rejects(
    prepareCloudSession(
      ["setup", "--login-timeout=1ms"],
      {},
      { ensureCloudSession: () => new Promise(() => {}) },
    ),
    /Cloud sign-in timed out after 1ms/,
  );
});

test("false no-open values keep browser login enabled", async () => {
  for (const value of ["false", "0"]) {
    let received;
    await prepareCloudSession(
      ["setup", `--no-open=${value}`],
      {},
      {
        ensureCloudSession: async (options) => {
          received = options;
        },
      },
    );
    assert.equal(received.device, false);
  }
});

test("valid explicit setup arguments still prepare Cloud auth", () => {
  assert.equal(
    hasValidSetupArguments([
      "setup",
      "--provider",
      "github",
      "--workspace=frontend",
      "--once",
      "--no-open=true",
    ]),
    true,
  );
  assert.equal(
    shouldPrepareCloudSession(
      ["setup", "--provider", "github", "--workspace=frontend", "--once"],
      {},
    ),
    true,
  );
});

test("the bundled SDK keeps canonical auth out of the child environment", () => {
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
  const authPath = path.join(authDir, "cloud-auth.json");
  const script = `
    const fs = require("node:fs");
    const { prepareCloudSession } = require(${JSON.stringify(modulePath)});
    prepareCloudSession([], process.env).then(() => {
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
