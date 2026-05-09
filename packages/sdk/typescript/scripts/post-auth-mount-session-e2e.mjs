import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(scriptPath);
const packageDir = path.resolve(scriptsDir, "..");
const repoRoot = path.resolve(packageDir, "../../..");
const evidenceLogPath = path.join(
  repoRoot,
  "docs/evidence/post-auth-mount-session-e2e.log"
);
const npmCommand = process.platform === "win32" ? "npm.cmd" : "npm";
const workspaceId = "rw_12345678";
const relaycastBaseUrl = "https://relaycast.mount.test";
const relaycastApiKey = "rk_mount_workspace";
const mountExpiresAt = "2026-05-09T11:55:00.000Z";
const mountSuggestedRefreshAt = "2026-05-09T11:40:00.000Z";
const mountTokenIssuedAt = "2026-05-09T09:55:00.000Z";
const seededNotionPath = "/notion/research/brief.md";
const seededNotionContent = "# Mock Notion brief\n";
const logLines = [];

function log(message) {
  logLines.push(message);
  console.log(message);
}

async function flushLog() {
  await mkdir(path.dirname(evidenceLogPath), { recursive: true });
  await writeFile(evidenceLogPath, `${logLines.join("\n")}\n`);
}

function lowerCaseHeaders(headers) {
  return Object.fromEntries(
    Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value])
  );
}

async function readRequestBody(request) {
  const chunks = [];
  for await (const chunk of request) {
    chunks.push(Buffer.from(chunk));
  }
  const text = Buffer.concat(chunks).toString("utf8");
  if (text === "") {
    return undefined;
  }
  const contentType = String(request.headers["content-type"] ?? "");
  if (contentType.includes("application/json")) {
    return JSON.parse(text);
  }
  return text;
}

function sendJson(response, status, body) {
  response.writeHead(status, {
    "content-type": "application/json"
  });
  response.end(JSON.stringify(body));
}

function createMountSessionResponse({
  workspaceId,
  relayfileBaseUrl,
  requestBody,
  relayfileToken
}) {
  return {
    workspaceId,
    relayfileBaseUrl,
    relayfileToken,
    wsUrl: `${relayfileBaseUrl.replace(/^http/, "ws")}/v1/workspaces/${workspaceId}/fs/ws`,
    remotePath: requestBody.remotePath ?? "/",
    localDir: requestBody.localDir,
    mode: requestBody.mode ?? "poll",
    scopes:
      Array.isArray(requestBody.scopes) && requestBody.scopes.length > 0
        ? requestBody.scopes
        : ["relayfile:fs:read:*", "relayfile:fs:write:*"],
    tokenIssuedAt: mountTokenIssuedAt,
    expiresAt: mountExpiresAt,
    suggestedRefreshAt: mountSuggestedRefreshAt,
    relaycastApiKey,
    relaycastBaseUrl
  };
}

function createCloudServer(relayfileBaseUrlRef) {
  const state = {
    requests: [],
    current: {
      joinToken: "rf_jwt_join_default",
      mountToken: "rf_mount_default",
      providerStatuses: []
    }
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const body = await readRequestBody(request);
    const record = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body
    };
    state.requests.push(record);

    const joinMatch = record.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/join$/
    );
    if (record.method === "POST" && joinMatch) {
      const joinedWorkspaceId = decodeURIComponent(joinMatch[1]);
      sendJson(response, 200, {
        workspaceId: joinedWorkspaceId,
        token: state.current.joinToken,
        relayfileUrl: relayfileBaseUrlRef.current,
        wsUrl: `${relayfileBaseUrlRef.current.replace(/^http/, "ws")}/v1/workspaces/${joinedWorkspaceId}/fs/ws`,
        relaycastApiKey,
        relaycastBaseUrl
      });
      return;
    }

    const providerMatch = record.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/integrations\/([^/]+)\/status$/
    );
    if (record.method === "GET" && providerMatch) {
      const next =
        state.current.providerStatuses.shift() ?? {
          ready: false,
          state: "not_connected"
        };
      sendJson(response, 200, next);
      return;
    }

    const mountMatch = record.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/relayfile\/mount-session$/
    );
    if (record.method === "POST" && mountMatch) {
      const mountedWorkspaceId = decodeURIComponent(mountMatch[1]);
      sendJson(
        response,
        200,
        createMountSessionResponse({
          workspaceId: mountedWorkspaceId,
          relayfileBaseUrl: relayfileBaseUrlRef.current,
          requestBody: body ?? {},
          relayfileToken: state.current.mountToken
        })
      );
      return;
    }

    sendJson(response, 404, {
      error: `Unhandled cloud route ${record.method} ${record.pathname}`
    });
  });

  return {
    server,
    state,
    reset(config) {
      state.requests.length = 0;
      state.current = {
        joinToken: config.joinToken,
        mountToken: config.mountToken,
        providerStatuses: [...(config.providerStatuses ?? [])]
      };
    }
  };
}

function createRelayfileServer() {
  const files = new Map();
  const state = {
    requests: []
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const body = await readRequestBody(request);
    const record = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body
    };
    state.requests.push(record);

    const treeMatch = record.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/fs\/tree$/
    );
    if (record.method === "GET" && treeMatch) {
      const requestedPath = normalizePath(record.searchParams.path ?? "/");
      sendJson(response, 200, {
        path: requestedPath,
        entries: buildTreeEntries(files, requestedPath),
        nextCursor: null
      });
      return;
    }

    const fileMatch = record.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/fs\/file$/
    );
    if (record.method === "GET" && fileMatch) {
      const requestedPath = normalizePath(record.searchParams.path ?? "/");
      const file = files.get(requestedPath);
      if (!file) {
        sendJson(response, 404, {
          error: `No file at ${requestedPath}`
        });
        return;
      }

      sendJson(response, 200, {
        path: requestedPath,
        revision: file.revision,
        contentType: file.contentType,
        content: file.content,
        encoding: "utf-8"
      });
      return;
    }

    sendJson(response, 404, {
      error: `Unhandled relayfile route ${record.method} ${record.pathname}`
    });
  });

  return {
    server,
    state,
    seedFile(targetPath, content, contentType = "text/markdown") {
      files.set(normalizePath(targetPath), {
        path: normalizePath(targetPath),
        content,
        contentType,
        revision: `rev_${files.size + 1}`
      });
    },
    reset() {
      state.requests.length = 0;
    }
  };
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      const address = server.address();
      resolve(`http://127.0.0.1:${address.port}`);
    });
  });
}

function closeServer(server) {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}

function commandError(command, args, error, stdout, stderr) {
  const details = [
    `Command failed: ${command} ${args.join(" ")}`,
    stdout ? `stdout:\n${stdout}` : "",
    stderr ? `stderr:\n${stderr}` : ""
  ]
    .filter(Boolean)
    .join("\n\n");
  const wrapped = new Error(details);
  wrapped.cause = error;
  return wrapped;
}

function runCommand(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    execFile(
      command,
      args,
      {
        cwd: options.cwd,
        env: options.env,
        maxBuffer: 10 * 1024 * 1024
      },
      (error, stdout, stderr) => {
        if (error) {
          reject(commandError(command, args, error, stdout, stderr));
          return;
        }
        resolve({ stdout, stderr });
      }
    );
  });
}

async function packWorkspace(relativeWorkspacePath, destinationDir) {
  const { stdout } = await runCommand(
    npmCommand,
    [
      "pack",
      `./${relativeWorkspacePath.replace(/^\.?\//, "")}`,
      "--pack-destination",
      destinationDir
    ],
    { cwd: repoRoot, env: process.env }
  );
  const tarballName = stdout.trim().split(/\r?\n/).filter(Boolean).at(-1);
  assert.ok(
    tarballName,
    `npm pack did not print a tarball name for ${relativeWorkspacePath}`
  );
  return path.join(destinationDir, tarballName);
}

async function writeConsumerScript(consumerDir) {
  const consumerScript = String.raw`
import assert from "node:assert/strict";
import { mkdir, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { RelayfileSetup } from "@relayfile/sdk";

const scenario = process.env.POST_AUTH_SCENARIO;
const cloudApiUrl = process.env.POST_AUTH_CLOUD_URL;
const resultFile = process.env.POST_AUTH_RESULT_FILE;
const workspaceId = process.env.POST_AUTH_WORKSPACE_ID;
const mountRoot = process.env.POST_AUTH_MOUNT_ROOT;
const accessToken = process.env.POST_AUTH_ACCESS_TOKEN;

function createLauncher() {
  const state = {
    startEnv: null,
    background: null,
    readyTimeoutMs: null,
    stopCalls: 0
  };

  return {
    state,
    launcher: {
      async start(input) {
        state.startEnv = { ...input.env };
        state.background = input.background ?? true;
        state.readyTimeoutMs = input.readyTimeoutMs;
        await mkdir(input.env.RELAYFILE_LOCAL_DIR, { recursive: true });
        return {
          pid: 4242,
          ready: Promise.resolve(),
          async status() {
            return {
              ready: true,
              mode: input.env.RELAYFILE_MOUNT_MODE ?? "poll",
              pid: 4242,
              expiresAt: null,
              suggestedRefreshAt: null
            };
          },
          async stop() {
            state.stopCalls += 1;
          }
        };
      }
    }
  };
}

function serializeError(error) {
  return {
    name: error?.name,
    message: error?.message,
    code: error?.code,
    mode: error?.mode,
    provider: error?.provider,
    state: error?.state,
    initialSyncState: error?.initialSyncState
  };
}

async function expectError(run, assertion) {
  try {
    await run();
  } catch (error) {
    await assertion(error);
    return serializeError(error);
  }
  throw new Error("Expected scenario to fail.");
}

async function runMountLike(mountCall) {
  const localDir = path.join(mountRoot, scenario);
  const { launcher, state } = createLauncher();
  const setup = new RelayfileSetup({
    cloudApiUrl,
    accessToken,
    requestTimeoutMs: 1_000,
    retry: { maxRetries: 0, baseDelayMs: 1 }
  });

  const handle = await mountCall(setup, localDir, launcher);
  const localDirStats = await stat(localDir);
  const mountedStatus = await handle.status();
  const mountedEnv = handle.env();
  await handle.stop();

  return {
    localDir,
    localDirCreated: localDirStats.isDirectory(),
    ready: handle.ready,
    expiresAt: handle.expiresAt,
    suggestedRefreshAt: handle.suggestedRefreshAt,
    env: mountedEnv,
    status: mountedStatus,
    launcher: {
      startEnv: state.startEnv,
      background: state.background,
      readyTimeoutMs: state.readyTimeoutMs,
      stopCalls: state.stopCalls
    }
  };
}

async function main() {
  let result;

  switch (scenario) {
    case "mount-workspace":
      result = await runMountLike((setup, localDir, launcher) =>
        setup.mountWorkspace({
          workspaceId,
          localDir,
          remotePath: "/notion",
          mode: "poll",
          launcher
        })
      );
      break;

    case "ensure-mounted-success":
      result = await runMountLike((setup, localDir, launcher) =>
        setup.ensureMountedWorkspace({
          workspaceId,
          localDir,
          remotePath: "/notion",
          provider: "notion",
          verifyProvider: true,
          mode: "poll",
          launcher
        })
      );
      break;

    case "ensure-mounted-readonly":
      result = await runMountLike(async (setup, localDir, launcher) => {
        const workspace = await setup.joinWorkspace(workspaceId, {
          scopes: ["fs:read"]
        });

        return setup.ensureMountedWorkspace({
          workspace,
          localDir,
          remotePath: "/notion",
          provider: "notion",
          verifyProvider: true,
          mode: "poll",
          scopes: ["fs:read"],
          launcher
        });
      });
      break;

    case "ensure-mounted-failure": {
      const localDir = path.join(mountRoot, scenario);
      const { launcher } = createLauncher();
      const setup = new RelayfileSetup({
        cloudApiUrl,
        accessToken,
        requestTimeoutMs: 1_000,
        retry: { maxRetries: 0, baseDelayMs: 1 }
      });

      result = {
        error: await expectError(
          () =>
            setup.ensureMountedWorkspace({
              workspaceId,
              localDir,
              remotePath: "/notion",
              provider: "notion",
              verifyProvider: true,
              mode: "poll",
              launcher
            }),
          async (error) => {
            assert.equal(error.code, "provider_not_connected");
            assert.equal(error.provider, "notion");
          }
        )
      };
      break;
    }

    case "mount-invalid-stream":
      {
        const setup = new RelayfileSetup({
          cloudApiUrl,
          accessToken,
          requestTimeoutMs: 1_000,
          retry: { maxRetries: 0, baseDelayMs: 1 }
        });

        result = {
        error: await expectError(
          () =>
            setup.mountWorkspace({
              workspaceId,
              localDir: path.join(mountRoot, scenario),
              mode: "stream"
            }),
          async (error) => {
            assert.equal(error.code, "invalid_mount_mode");
            assert.equal(error.mode, "stream");
          }
        )
        };
      }
      break;

    default:
      throw new Error("Unknown scenario: " + scenario);
  }

  await writeFile(resultFile, JSON.stringify({ ok: true, result }, null, 2));
}

main().catch(async (error) => {
  await writeFile(
    resultFile,
    JSON.stringify(
      {
        ok: false,
        error: serializeError(error)
      },
      null,
      2
    )
  );
  console.error(error);
  process.exitCode = 1;
});
`;

  const consumerScriptPath = path.join(consumerDir, "consumer.mjs");
  await writeFile(consumerScriptPath, consumerScript);
  return consumerScriptPath;
}

async function writeHarnessScript(consumerDir) {
  const harnessScript = String.raw`
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import {
  MountHarnessPermissionError,
  startMountHarness
} from "@relayfile/sdk/dist/mount-harness.js";

const resultFile = process.env.HARNESS_RESULT_FILE;
const expectedRelativePath = process.env.HARNESS_EXPECTED_RELATIVE_PATH;
const writeRelativePath = process.env.HARNESS_TEST_READ_ONLY_RELATIVE_PATH;
const scopes = process.env.HARNESS_SCOPES
  ? JSON.parse(process.env.HARNESS_SCOPES)
  : [];

async function main() {
  const harness = await startMountHarness({
    env: process.env,
    pollIntervalMs: 25,
    scopes
  });

  try {
    const observedContent = expectedRelativePath
      ? await readFile(
          path.join(process.env.RELAYFILE_LOCAL_DIR, expectedRelativePath),
          "utf8"
        )
      : null;

    let readOnlyError = null;
    if (writeRelativePath) {
      try {
        await harness.writeLocalFile(
          writeRelativePath,
          process.env.HARNESS_TEST_READ_ONLY_CONTENT ?? ""
        );
        throw new Error("Expected read-only write to fail.");
      } catch (error) {
        if (!(error instanceof MountHarnessPermissionError)) {
          throw error;
        }
        readOnlyError = {
          code: error.code,
          path: error.path,
          scopes: error.scopes
        };
      }
    }

    await writeFile(
      resultFile,
      JSON.stringify(
        {
          ok: true,
          observedContent,
          readOnlyError
        },
        null,
        2
      )
    );
  } finally {
    await harness.stop();
  }
}

main().catch(async (error) => {
  await writeFile(
    resultFile,
    JSON.stringify(
      {
        ok: false,
        error: {
          name: error?.name,
          message: error?.message,
          code: error?.code
        }
      },
      null,
      2
    )
  );
  console.error(error);
  process.exitCode = 1;
});
`;

  const harnessScriptPath = path.join(consumerDir, "mount-harness-runner.mjs");
  await writeFile(harnessScriptPath, harnessScript);
  return harnessScriptPath;
}

async function runScenario({
  consumerDir,
  consumerScriptPath,
  cloudApiUrl,
  scenario,
  mountRoot
}) {
  const resultFile = path.join(consumerDir, `${scenario}.json`);
  await runCommand(process.execPath, [consumerScriptPath], {
    cwd: consumerDir,
    env: {
      ...process.env,
      POST_AUTH_SCENARIO: scenario,
      POST_AUTH_CLOUD_URL: cloudApiUrl,
      POST_AUTH_RESULT_FILE: resultFile,
      POST_AUTH_WORKSPACE_ID: workspaceId,
      POST_AUTH_MOUNT_ROOT: mountRoot,
      POST_AUTH_ACCESS_TOKEN: "cld_at_post_auth"
    }
  });

  const payload = JSON.parse(await readFile(resultFile, "utf8"));
  assert.equal(
    payload.ok,
    true,
    `${scenario} consumer run failed: ${JSON.stringify(payload.error)}`
  );
  return payload.result;
}

async function runHarnessScenario({
  consumerDir,
  harnessScriptPath,
  scenario,
  mountEnv,
  scopes,
  expectedRelativePath,
  writeRelativePath,
  writeContent
}) {
  const resultFile = path.join(consumerDir, `${scenario}-harness.json`);
  await runCommand(process.execPath, [harnessScriptPath], {
    cwd: consumerDir,
    env: {
      ...process.env,
      ...mountEnv,
      HARNESS_RESULT_FILE: resultFile,
      HARNESS_SCOPES: JSON.stringify(scopes),
      ...(expectedRelativePath
        ? { HARNESS_EXPECTED_RELATIVE_PATH: expectedRelativePath }
        : {}),
      ...(writeRelativePath
        ? {
            HARNESS_TEST_READ_ONLY_RELATIVE_PATH: writeRelativePath,
            HARNESS_TEST_READ_ONLY_CONTENT: writeContent ?? ""
          }
        : {})
    }
  });

  const payload = JSON.parse(await readFile(resultFile, "utf8"));
  assert.equal(
    payload.ok,
    true,
    `${scenario} harness run failed: ${JSON.stringify(payload.error)}`
  );
  return payload;
}

function assertMountSessionShape(body) {
  assert.deepEqual(Object.keys(body), [
    "workspaceId",
    "relayfileBaseUrl",
    "relayfileToken",
    "wsUrl",
    "remotePath",
    "localDir",
    "mode",
    "scopes",
    "tokenIssuedAt",
    "expiresAt",
    "suggestedRefreshAt",
    "relaycastApiKey",
    "relaycastBaseUrl"
  ]);
}

async function main() {
  const tempRoot = await mkdtemp(
    path.join(tmpdir(), "relayfile-post-auth-mount-e2e-")
  );
  const packedDir = path.join(tempRoot, "packed");
  const consumerDir = path.join(tempRoot, "consumer");
  const mountRoot = path.join(tempRoot, "mounts");
  const relayfileBaseUrlRef = { current: "" };
  const cloudServer = createCloudServer(relayfileBaseUrlRef);
  const relayfileServer = createRelayfileServer();

  try {
    log("Building workspace packages");
    await runCommand(
      npmCommand,
      ["run", "build", "--workspace=packages/core"],
      { cwd: repoRoot, env: process.env }
    );
    await runCommand(
      npmCommand,
      ["run", "build", "--workspace=packages/sdk/typescript"],
      { cwd: repoRoot, env: process.env }
    );

    log("Packing @relayfile/core and @relayfile/sdk");
    await mkdir(packedDir, { recursive: true });
    const [coreTarball, sdkTarball] = await Promise.all([
      packWorkspace("packages/core", packedDir),
      packWorkspace("packages/sdk/typescript", packedDir)
    ]);

    log("Starting mock Cloud and Relayfile services");
    relayfileBaseUrlRef.current = await listen(relayfileServer.server);
    const cloudApiUrl = await listen(cloudServer.server);
    relayfileServer.seedFile(seededNotionPath, seededNotionContent);

    log("Installing packed artifacts into a temporary consumer");
    await mkdir(consumerDir, { recursive: true });
    await mkdir(mountRoot, { recursive: true });
    await writeFile(
      path.join(consumerDir, "package.json"),
      JSON.stringify(
        {
          name: "post-auth-mount-e2e-consumer",
          private: true,
          type: "module"
        },
        null,
        2
      )
    );
    await runCommand(
      npmCommand,
      ["install", "--no-package-lock", coreTarball, sdkTarball],
      { cwd: consumerDir, env: process.env }
    );
    const consumerScriptPath = await writeConsumerScript(consumerDir);
    const harnessScriptPath = await writeHarnessScript(consumerDir);

    log("Running mountWorkspace packaged scenario");
    cloudServer.reset({
      joinToken: "rf_jwt_join_mount",
      mountToken: "rf_mount_session_token"
    });
    relayfileServer.reset();
    const mountResult = await runScenario({
      consumerDir,
      consumerScriptPath,
      cloudApiUrl,
      scenario: "mount-workspace",
      mountRoot
    });
    assert.equal(cloudServer.state.requests.length, 2);
    assert.equal(
      cloudServer.state.requests[0].pathname,
      `/api/v1/workspaces/${workspaceId}/join`
    );
    assert.equal(
      cloudServer.state.requests[0].headers.authorization,
      "Bearer cld_at_post_auth"
    );
    assert.equal(
      cloudServer.state.requests[1].pathname,
      `/api/v1/workspaces/${workspaceId}/relayfile/mount-session`
    );
    assert.equal(
      cloudServer.state.requests[1].headers.authorization,
      "Bearer rf_jwt_join_mount"
    );
    assert.deepEqual(cloudServer.state.requests[1].body, {
      localDir: mountResult.localDir,
      remotePath: "/notion",
      mode: "poll",
      agentName: "relayfile-mount"
    });
    const mountResponseShape = createMountSessionResponse({
      workspaceId,
      relayfileBaseUrl: relayfileBaseUrlRef.current,
      requestBody: cloudServer.state.requests[1].body,
      relayfileToken: "rf_mount_session_token"
    });
    assertMountSessionShape(mountResponseShape);
    assert.equal(mountResult.localDirCreated, true);
    assert.equal(mountResult.ready, true);
    assert.equal(mountResult.expiresAt, mountExpiresAt);
    assert.equal(mountResult.suggestedRefreshAt, mountSuggestedRefreshAt);
    assert.deepEqual(mountResult.env, {
      RELAYFILE_BASE_URL: relayfileBaseUrlRef.current,
      RELAYFILE_TOKEN: "rf_mount_session_token",
      RELAYFILE_WORKSPACE: workspaceId,
      RELAYFILE_REMOTE_PATH: "/notion",
      RELAYFILE_LOCAL_DIR: mountResult.localDir,
      RELAYFILE_MOUNT_MODE: "poll",
      RELAYCAST_API_KEY: relaycastApiKey,
      RELAY_API_KEY: relaycastApiKey,
      RELAYCAST_BASE_URL: relaycastBaseUrl,
      RELAY_BASE_URL: relaycastBaseUrl
    });
    assert.deepEqual(mountResult.launcher, {
      startEnv: {
        ...mountResult.env,
        RELAYFILE_MOUNT_SCOPES: "relayfile:fs:read:* relayfile:fs:write:*"
      },
      background: true,
      readyTimeoutMs: 60000,
      stopCalls: 1
    });
    assert.deepEqual(mountResult.status, {
      ready: true,
      mode: "poll",
      pid: 4242,
      expiresAt: mountExpiresAt,
      suggestedRefreshAt: mountSuggestedRefreshAt
    });
    assert.equal(relayfileServer.state.requests.length, 1);
    assert.equal(
      relayfileServer.state.requests[0].pathname,
      `/v1/workspaces/${workspaceId}/fs/tree`
    );
    assert.deepEqual(relayfileServer.state.requests[0].searchParams, {
      path: "/notion",
      depth: "1"
    });
    assert.equal(
      relayfileServer.state.requests[0].headers.authorization,
      "Bearer rf_mount_session_token"
    );
    const leadHarnessResult = await runHarnessScenario({
      consumerDir,
      harnessScriptPath,
      scenario: "mount-workspace",
      mountEnv: mountResult.env,
      scopes: ["fs:read", "fs:write"],
      expectedRelativePath: "research/brief.md"
    });
    assert.equal(leadHarnessResult.observedContent, seededNotionContent);
    log("Verified mountWorkspace request path, body, authorization, env mapping, seeded /notion/research/brief.md sync, expiresAt, status(), env(), and stop()");

    log("Running ensureMountedWorkspace verifyProvider=true success scenario");
    cloudServer.reset({
      joinToken: "rf_jwt_join_ensure_success",
      mountToken: "rf_mount_ensure_success",
      providerStatuses: [{ ready: true, state: "ready" }]
    });
    relayfileServer.reset();
    const ensureSuccessResult = await runScenario({
      consumerDir,
      consumerScriptPath,
      cloudApiUrl,
      scenario: "ensure-mounted-success",
      mountRoot
    });
    assert.equal(cloudServer.state.requests.length, 3);
    assert.equal(
      cloudServer.state.requests[0].pathname,
      `/api/v1/workspaces/${workspaceId}/join`
    );
    assert.equal(
      cloudServer.state.requests[1].pathname,
      `/api/v1/workspaces/${workspaceId}/integrations/notion/status`
    );
    assert.deepEqual(cloudServer.state.requests[1].searchParams, {
      connectionId: workspaceId
    });
    assert.equal(
      cloudServer.state.requests[1].headers.authorization,
      "Bearer rf_jwt_join_ensure_success"
    );
    assert.equal(
      cloudServer.state.requests[2].pathname,
      `/api/v1/workspaces/${workspaceId}/relayfile/mount-session`
    );
    assert.equal(
      cloudServer.state.requests[2].headers.authorization,
      "Bearer rf_jwt_join_ensure_success"
    );
    assert.deepEqual(cloudServer.state.requests[2].body, {
      localDir: ensureSuccessResult.localDir,
      remotePath: "/notion",
      mode: "poll",
      agentName: "relayfile-mount"
    });
    assert.equal(ensureSuccessResult.ready, true);
    assert.equal(ensureSuccessResult.localDirCreated, true);
    assert.equal(ensureSuccessResult.status.ready, true);
    assert.equal(ensureSuccessResult.expiresAt, mountExpiresAt);
    assert.equal(ensureSuccessResult.launcher.stopCalls, 1);
    assert.equal(relayfileServer.state.requests.length, 1);
    assert.equal(
      relayfileServer.state.requests[0].headers.authorization,
      "Bearer rf_mount_ensure_success"
    );
    log("Verified ensureMountedWorkspace verifyProvider=true success path");

    log("Running ensureMountedWorkspace invited read-only scenario");
    cloudServer.reset({
      joinToken: "rf_jwt_join_invited_readonly",
      mountToken: "rf_mount_invited_readonly",
      providerStatuses: [{ ready: true, state: "ready" }]
    });
    relayfileServer.reset();
    const ensureReadOnlyResult = await runScenario({
      consumerDir,
      consumerScriptPath,
      cloudApiUrl,
      scenario: "ensure-mounted-readonly",
      mountRoot
    });
    assert.equal(cloudServer.state.requests.length, 3);
    assert.equal(
      cloudServer.state.requests[0].pathname,
      `/api/v1/workspaces/${workspaceId}/join`
    );
    assert.deepEqual(cloudServer.state.requests[0].body, {
      agentName: "sdk-agent",
      scopes: ["fs:read"]
    });
    assert.equal(
      cloudServer.state.requests[1].pathname,
      `/api/v1/workspaces/${workspaceId}/integrations/notion/status`
    );
    assert.equal(
      cloudServer.state.requests[2].pathname,
      `/api/v1/workspaces/${workspaceId}/relayfile/mount-session`
    );
    assert.deepEqual(cloudServer.state.requests[2].body, {
      localDir: ensureReadOnlyResult.localDir,
      remotePath: "/notion",
      mode: "poll",
      agentName: "relayfile-mount",
      scopes: ["fs:read"]
    });
    const readOnlyHarnessResult = await runHarnessScenario({
      consumerDir,
      harnessScriptPath,
      scenario: "ensure-mounted-readonly",
      mountEnv: ensureReadOnlyResult.env,
      scopes: ["fs:read"],
      expectedRelativePath: "research/brief.md",
      writeRelativePath: "review/notes.md",
      writeContent: "should fail"
    });
    assert.equal(readOnlyHarnessResult.observedContent, seededNotionContent);
    assert.deepEqual(readOnlyHarnessResult.readOnlyError, {
      code: "permission_denied",
      path: "/notion/review/notes.md",
      scopes: ["fs:read"]
    });
    log("Verified ensureMountedWorkspace invited read-only path and MountHarnessPermissionError");

    log("Running ensureMountedWorkspace verifyProvider=true failure scenario");
    cloudServer.reset({
      joinToken: "rf_jwt_join_ensure_failure",
      mountToken: "rf_mount_ensure_failure",
      providerStatuses: [{ ready: false, state: "not_connected" }]
    });
    relayfileServer.reset();
    const ensureFailureResult = await runScenario({
      consumerDir,
      consumerScriptPath,
      cloudApiUrl,
      scenario: "ensure-mounted-failure",
      mountRoot
    });
    assert.equal(cloudServer.state.requests.length, 2);
    assert.equal(
      cloudServer.state.requests[0].pathname,
      `/api/v1/workspaces/${workspaceId}/join`
    );
    assert.equal(
      cloudServer.state.requests[1].pathname,
      `/api/v1/workspaces/${workspaceId}/integrations/notion/status`
    );
    assert.equal(relayfileServer.state.requests.length, 0);
    assert.deepEqual(ensureFailureResult.error, {
      name: "ProviderNotConnectedError",
      message: 'Provider "notion" is not connected for this workspace.',
      code: "provider_not_connected",
      provider: "notion"
    });
    log("Verified ensureMountedWorkspace verifyProvider=true failure path");

    log("Running mode=\"stream\" rejection scenario");
    cloudServer.reset({
      joinToken: "rf_jwt_unused_stream",
      mountToken: "rf_mount_unused_stream"
    });
    relayfileServer.reset();
    const invalidModeResult = await runScenario({
      consumerDir,
      consumerScriptPath,
      cloudApiUrl,
      scenario: "mount-invalid-stream",
      mountRoot
    });
    assert.deepEqual(invalidModeResult.error, {
      name: "InvalidMountModeError",
      message: 'Invalid mount mode "stream". Expected "poll" or "fuse".',
      code: "invalid_mount_mode",
      mode: "stream"
    });
    assert.equal(cloudServer.state.requests.length, 0);
    assert.equal(relayfileServer.state.requests.length, 0);
    log("Verified mode=\"stream\" is rejected by the SDK before any HTTP request");

    log(`Evidence log written to ${evidenceLogPath}`);
    log("POST_AUTH_MOUNT_E2E_OK");
  } finally {
    await Promise.allSettled([
      closeServer(cloudServer.server),
      closeServer(relayfileServer.server),
      rm(tempRoot, { recursive: true, force: true })
    ]);
  }
}

main()
  .catch(async (error) => {
    log(`POST_AUTH_MOUNT_E2E_FAILED: ${error instanceof Error ? error.message : String(error)}`);
    await flushLog();
    throw error;
  })
  .then(async () => {
    await flushLog();
  });

function normalizePath(input) {
  const value = String(input ?? "").trim();
  if (value === "" || value === "/") {
    return "/";
  }
  const normalized = value.replace(/\\/g, "/").replace(/\/{2,}/g, "/");
  return normalized.startsWith("/") ? normalized : `/${normalized}`;
}

function buildTreeEntries(files, rootPath) {
  const root = normalizePath(rootPath);
  const entries = new Map();

  for (const [filePath, file] of files.entries()) {
    if (root !== "/" && filePath !== root && !filePath.startsWith(`${root}/`)) {
      continue;
    }

    const relative = root === "/" ? filePath.slice(1) : filePath.slice(root.length + 1);
    if (relative === "") {
      continue;
    }

    const segments = relative.split("/").filter(Boolean);
    if (segments.length === 1) {
      entries.set(filePath, {
        path: filePath,
        type: "file",
        revision: file.revision
      });
      continue;
    }

    const childDir = `${root === "/" ? "" : root}/${segments[0]}`.replace(/\/{2,}/g, "/");
    if (!entries.has(childDir)) {
      entries.set(childDir, {
        path: childDir.startsWith("/") ? childDir : `/${childDir}`,
        type: "dir",
        revision: "dir_rev_1"
      });
    }
  }

  return [...entries.values()].sort((left, right) => left.path.localeCompare(right.path));
}
