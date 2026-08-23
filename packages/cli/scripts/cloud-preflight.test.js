"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const test = require("node:test");

const {
  SETUP_INTENT,
  SETUP_INTENT_PRINTED_ENV,
  announceSetupIntent,
  hasValidSetupArguments,
  parseGoDurationMilliseconds,
  prepareCloudSession,
  shouldPrepareCloudSession,
} = require("./cloud-preflight.js");

test("SDK setup intent is announced once before authentication", () => {
  const env = {};
  const lines = [];
  assert.equal(announceSetupIntent([], env, (line) => lines.push(line)), true);
  assert.equal(announceSetupIntent([], env, (line) => lines.push(line)), true);
  assert.deepEqual(lines, [SETUP_INTENT]);
  assert.equal(env[SETUP_INTENT_PRINTED_ENV], "1");
});

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
  assert.equal(calls[0].signal instanceof AbortSignal, true);
  assert.equal(calls[0].signal.aborted, false);
  assert.deepEqual(calls, [
    {
      apiUrl: "https://agentrelay.com/cloud",
      client: "relayfile",
      interactive: true,
      device: false,
      refreshTimeoutMs: 10000,
      signal: calls[0].signal,
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
    client: "relayfile",
    interactive: true,
    device: true,
    refreshTimeoutMs: 10000,
    signal: received.signal,
  });
  assert.equal(received.signal instanceof AbortSignal, true);
  assert.deepEqual(env, {});
});

test("bundled SDK carries the Relayfile marker through both login modes", () => {
  const bundledSdk = fs.readFileSync(
    path.join(__dirname, "cloud-auth.cjs"),
    "utf8",
  );
  assert.match(
    bundledSdk,
    /loginUrl\.searchParams\.set\("client", options\.client\)/,
  );
  assert.match(bundledSdk, /clientName: options\.client/);
  assert.match(bundledSdk, /signal: options\.signal/);
  assert.match(bundledSdk, /throwIfAborted\(options\.signal\)/);
});

test("bundled SDK aborts device polling without issuing or storing credentials", () => {
  const modulePath = path.join(__dirname, "cloud-auth.cjs");
  const script = `
    const { ensureCloudSession } = require(${JSON.stringify(modulePath)});
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

test("help and caller-owned tokens do not start interactive auth", () => {
  assert.equal(shouldPrepareCloudSession(["setup", "--help"], {}), false);
  assert.equal(shouldPrepareCloudSession(["setup", "--help=true"], {}), false);
  assert.equal(shouldPrepareCloudSession(["setup", "--help=false"], {}), false);
  assert.equal(shouldPrepareCloudSession(["setup", "-h=0"], {}), false);
  assert.equal(
    shouldPrepareCloudSession(["setup", "--cloud-token", "explicit"], {}),
    false,
  );
  assert.equal(
    shouldPrepareCloudSession(["setup", "-cloud-token", "explicit"], {}),
    false,
  );
  assert.equal(
    shouldPrepareCloudSession(["setup", "--cloud-token="], {}),
    true,
  );
  assert.equal(
    shouldPrepareCloudSession(["setup", "--cloud-token", ""], {}),
    true,
  );
  assert.equal(
    shouldPrepareCloudSession(
      ["setup", "--cloud-token="],
      { RELAYFILE_CLOUD_TOKEN: "inherited-token" },
    ),
    true,
  );
  assert.equal(
    shouldPrepareCloudSession(
      ["setup"],
      { RELAYFILE_CLOUD_TOKEN: "inherited-token" },
    ),
    false,
  );
  assert.equal(
    shouldPrepareCloudSession([], { CLOUD_API_ACCESS_TOKEN: "ci-token" }),
    false,
  );
  assert.equal(shouldPrepareCloudSession(["status"], {}), false);
});

test("pseudo-help values never start SDK auth", async () => {
  for (const args of [
    ["setup", "--help=false"],
    ["setup", "-h=0", "--cloud-token="],
  ]) {
    let calls = 0;
    const prepared = await prepareCloudSession(
      args,
      { RELAYFILE_CLOUD_TOKEN: "inherited-token" },
      {
        ensureCloudSession: async () => {
          calls += 1;
        },
      },
    );
    assert.equal(prepared, false);
    assert.equal(calls, 0);
  }
});

test("native help short-circuits even when it occupies a setup value slot", async () => {
  let calls = 0;
  const prepared = await prepareCloudSession(
    ["setup", "--provider", "--help"],
    {},
    {
      ensureCloudSession: async () => {
        calls += 1;
      },
    },
  );
  assert.equal(prepared, false);
  assert.equal(calls, 0);
});

test("version only bypasses auth when the native CLI treats it as version", async () => {
  assert.equal(shouldPrepareCloudSession(["--version"], {}), false);
  assert.equal(shouldPrepareCloudSession(["version"], {}), false);
  assert.equal(
    shouldPrepareCloudSession(["setup", "--local-dir", "--version"], {}),
    true,
  );

  let calls = 0;
  const prepared = await prepareCloudSession(
    ["setup", "--local-dir", "--version"],
    {},
    {
      ensureCloudSession: async () => {
        calls += 1;
      },
    },
  );
  assert.equal(prepared, true);
  assert.equal(calls, 1);
});

test("dash-prefixed values follow the native setup grammar", async () => {
  const args = ["setup", "--local-dir", "-mirror"];
  assert.equal(hasValidSetupArguments(args), true);
  let calls = 0;
  const prepared = await prepareCloudSession(args, {}, {
    ensureCloudSession: async () => {
      calls += 1;
    },
  });
  assert.equal(prepared, true);
  assert.equal(calls, 1);
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
  let receivedSignal;
  let lateCredentialWrite = false;
  await assert.rejects(
    prepareCloudSession(
      ["setup", "--login-timeout=1ms"],
      {},
      {
        ensureCloudSession: ({ signal }) => {
          receivedSignal = signal;
          return new Promise((resolve, reject) => {
            const lateWrite = setTimeout(() => {
              lateCredentialWrite = true;
              resolve();
            }, 25);
            signal.addEventListener(
              "abort",
              () => {
                clearTimeout(lateWrite);
                reject(signal.reason);
              },
              { once: true },
            );
          });
        },
      },
    ),
    /Cloud sign-in timed out after 1ms/,
  );
  assert.equal(receivedSignal.aborted, true);
  assert.match(receivedSignal.reason.message, /timed out after 1ms/);
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(lateCredentialWrite, false);
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
