import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(scriptPath);
const packageDir = path.resolve(scriptsDir, "..");
const repoRoot = path.resolve(packageDir, "../../..");
const npmCommand = process.platform === "win32" ? "npm.cmd" : "npm";
const tempRoot = await mkdtemp(path.join(tmpdir(), "relayfile-sdk-setup-e2e-"));

const activityLog = [];

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

function sendResponse(response, config) {
  const status = config?.status ?? 200;
  const headers = { ...(config?.headers ?? {}) };
  if (config?.body !== undefined) {
    headers["content-type"] = headers["content-type"] ?? "application/json";
  }
  response.writeHead(status, headers);
  if (config?.rawBody !== undefined) {
    response.end(config.rawBody);
    return;
  }
  if (config?.body !== undefined) {
    response.end(JSON.stringify(config.body));
    return;
  }
  response.end();
}

function resolveQueueEntry(entry, requestRecord, context) {
  if (typeof entry === "function") {
    return entry(requestRecord, context);
  }
  return entry;
}

function createCloudServer(relayfileBaseUrl) {
  const state = {
    workspaceId: "ws_e2e",
    requests: [],
    createQueue: [],
    joinQueue: [],
    connectQueue: [],
    deleteQueue: [],
    statusQueues: new Map(),
    joinCounter: 0
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const requestBody = await readRequestBody(request);
    const requestRecord = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body: requestBody,
      receivedAt: Date.now()
    };

    state.requests.push(requestRecord);
    activityLog.push({ source: "cloud", ...requestRecord });

    if (requestRecord.method === "POST" && requestRecord.pathname === "/api/v1/workspaces") {
      const entry = state.createQueue.shift();
      const config =
        resolveQueueEntry(entry, requestRecord, state) ??
        {
          body: {
            workspaceId: state.workspaceId,
            relayfileUrl: relayfileBaseUrl(),
            relaycastApiKey: "rc_test",
            createdAt: "2026-04-30T00:00:00.000Z",
            name: "sdk-e2e"
          }
        };
      sendResponse(response, config);
      return;
    }

    const joinMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/join$/
    );
    if (requestRecord.method === "POST" && joinMatch) {
      const workspaceId = decodeURIComponent(joinMatch[1]);
      state.joinCounter += 1;
      const entry = state.joinQueue.shift();
      const config =
        resolveQueueEntry(entry, requestRecord, {
          ...state,
          workspaceId
        }) ??
        {
          body: {
            workspaceId,
            token: `rf_jwt_join_${state.joinCounter}`,
            relayfileUrl: relayfileBaseUrl(),
            wsUrl: "wss://relayfile.test/ws",
            relaycastApiKey: "rc_test"
          }
        };
      sendResponse(response, config);
      return;
    }

    const connectMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/integrations\/connect-session$/
    );
    if (requestRecord.method === "POST" && connectMatch) {
      const workspaceId = decodeURIComponent(connectMatch[1]);
      const entry = state.connectQueue.shift();
      const config =
        resolveQueueEntry(entry, requestRecord, {
          ...state,
          workspaceId
        }) ??
        {
          body: {
            token: "session_token",
            expiresAt: "2026-04-30T01:00:00.000Z",
            connectLink: "https://connect.test/github",
            connectionId: "conn_default"
          }
        };
      sendResponse(response, config);
      return;
    }

    const statusMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/integrations\/([^/]+)\/status$/
    );
    if (statusMatch) {
      const workspaceId = decodeURIComponent(statusMatch[1]);
      const provider = decodeURIComponent(statusMatch[2]);
      if (requestRecord.method === "GET") {
        const connectionId = requestRecord.searchParams.connectionId ?? "";
        const key = `${provider}:${connectionId}`;
        const queue = state.statusQueues.get(key) ?? [];
        const entry = queue.shift();
        if (queue.length === 0) {
          state.statusQueues.delete(key);
        } else {
          state.statusQueues.set(key, queue);
        }
        const config =
          resolveQueueEntry(entry, requestRecord, {
            ...state,
            workspaceId,
            provider,
            connectionId
          }) ?? { body: { ready: false } };
        sendResponse(response, config);
        return;
      }

      if (requestRecord.method === "DELETE") {
        const entry = state.deleteQueue.shift();
        const config =
          resolveQueueEntry(entry, requestRecord, {
            ...state,
            workspaceId,
            provider
          }) ?? { status: 204 };
        sendResponse(response, config);
        return;
      }
    }

    sendResponse(response, {
      status: 404,
      body: { message: `Unhandled cloud route ${requestRecord.method} ${requestRecord.pathname}` }
    });
  });

  return {
    server,
    state,
    reset(config = {}) {
      state.workspaceId = config.workspaceId ?? "ws_e2e";
      state.requests.length = 0;
      state.createQueue = [...(config.createQueue ?? [])];
      state.joinQueue = [...(config.joinQueue ?? [])];
      state.connectQueue = [...(config.connectQueue ?? [])];
      state.deleteQueue = [...(config.deleteQueue ?? [])];
      state.statusQueues = new Map(
        Object.entries(config.statusQueues ?? {}).map(([key, value]) => [
          key,
          [...value]
        ])
      );
      state.joinCounter = 0;
      activityLog.length = 0;
    }
  };
}

function createRelayfileServer() {
  const state = {
    requests: [],
    treeQueue: []
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const requestBody = await readRequestBody(request);
    const requestRecord = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body: requestBody,
      receivedAt: Date.now()
    };

    state.requests.push(requestRecord);
    activityLog.push({ source: "relay", ...requestRecord });

    const treeMatch = requestRecord.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/fs\/tree$/
    );
    if (requestRecord.method === "GET" && treeMatch) {
      const workspaceId = decodeURIComponent(treeMatch[1]);
      const entry = state.treeQueue.shift();
      const config =
        resolveQueueEntry(entry, requestRecord, { workspaceId }) ??
        {
          body: {
            path: requestRecord.searchParams.path ?? "/",
            entries: [
              {
                path: `${requestRecord.searchParams.path ?? "/"}/repo`.replace(/\/{2,}/g, "/"),
                type: "dir",
                revision: "rev_1"
              }
            ],
            nextCursor: null
          }
        };
      sendResponse(response, config);
      return;
    }

    sendResponse(response, {
      status: 404,
      body: { message: `Unhandled relay route ${requestRecord.method} ${requestRecord.pathname}` }
    });
  });

  return {
    server,
    state,
    reset(config = {}) {
      state.requests.length = 0;
      state.treeQueue = [...(config.treeQueue ?? [])];
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
    ["pack", `./${relativeWorkspacePath.replace(/^\.?\//, "")}`, "--pack-destination", destinationDir],
    { cwd: repoRoot, env: process.env }
  );
  const tarballName = stdout.trim().split(/\r?\n/).filter(Boolean).at(-1);
  assert.ok(tarballName, `npm pack did not print a tarball name for ${relativeWorkspacePath}`);
  return path.join(destinationDir, tarballName);
}

async function writeConsumerScript(consumerDir) {
  const consumerScript = String.raw`
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import {
  CloudAbortError,
  CloudApiError,
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError,
  RelayfileSetup
} from "@relayfile/sdk";

const scenario = process.env.SDK_E2E_SCENARIO;
const resultFile = process.env.SDK_E2E_RESULT_FILE;
const cloudApiUrl = process.env.SDK_E2E_CLOUD_URL;
const workspaceId = process.env.SDK_E2E_WORKSPACE_ID ?? "ws_e2e";

function setupOptions(extra = {}) {
  return {
    cloudApiUrl,
    requestTimeoutMs: 500,
    retry: {
      maxRetries: 2,
      baseDelayMs: 25
    },
    ...extra
  };
}

function serializeError(error) {
  return {
    name: error?.name,
    message: error?.message,
    code: error?.code,
    field: error?.field,
    httpStatus: error?.httpStatus,
    httpBody: error?.httpBody
  };
}

async function expectError(run, assertion) {
  try {
    await run();
  } catch (error) {
    await assertion(error);
    return serializeError(error);
  }
  throw new Error("Expected the scenario to fail, but it succeeded.");
}

async function main() {
  let result;

  switch (scenario) {
    case "create-workspace": {
      const setup = new RelayfileSetup(
        setupOptions({ accessToken: "cld_at_test" })
      );
      const handle = await setup.createWorkspace({
        name: "sdk-e2e",
        agentName: "sdk-e2e-agent"
      });
      result = {
        workspaceId: handle.workspaceId,
        token: handle.getToken(),
        relayfileUrl: handle.info.relayfileUrl
      };
      break;
    }

    case "join-workspace": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId, {
        agentName: "join-only-agent"
      });
      result = {
        workspaceId: handle.workspaceId,
        token: handle.getToken()
      };
      break;
    }

    case "list-tree": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const tree = await handle.client().listTree(handle.workspaceId, {
        path: "/github"
      });
      assert.equal(tree.path, "/github");
      result = {
        token: handle.getToken(),
        path: tree.path
      };
      break;
    }

    case "connect-already": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const connection = await handle.connectIntegration("github", {
        connectionId: "conn_existing"
      });
      assert.equal(connection.alreadyConnected, true);
      result = connection;
      break;
    }

    case "connect-oauth": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const connection = await handle.connectIntegration("github");
      assert.equal(connection.alreadyConnected, false);
      assert.equal(connection.connectionId, "conn_oauth");
      result = connection;
      break;
    }

    case "connect-notion": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const connection = await handle.connectNotion();
      assert.equal(connection.alreadyConnected, false);
      assert.equal(connection.connectionId, "conn_notion");
      result = connection;
      break;
    }

    case "mount-env-and-invite": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId, {
        agentName: "lead-agent",
        scopes: ["fs:read", "fs:write", "relaycast:write"]
      });
      result = {
        mountEnv: handle.mountEnv({
          localDir: "/workspace/notion",
          remotePath: "/notion"
        }),
        invite: handle.agentInvite({ agentName: "review-agent" })
      };
      break;
    }

    case "wait-delayed": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      await handle.connectIntegration("github");
      const polls = [];
      await handle.waitForConnection("github", {
        pollIntervalMs: 10,
        timeoutMs: 500,
        onPoll: (elapsed) => {
          polls.push(elapsed);
        }
      });
      result = { polls };
      break;
    }

    case "wait-timeout": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      result = {
        error: await expectError(
          () =>
            handle.waitForConnection("github", {
              connectionId: "conn_timeout",
              pollIntervalMs: 10,
              timeoutMs: 60
            }),
          async (error) => {
            assert.ok(error instanceof IntegrationConnectionTimeoutError);
          }
        )
      };
      break;
    }

    case "wait-abort": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const controller = new AbortController();
      setTimeout(() => controller.abort(), 30);
      result = {
        error: await expectError(
          () =>
            handle.waitForConnection("github", {
              connectionId: "conn_abort",
              pollIntervalMs: 25,
              timeoutMs: 500,
              signal: controller.signal
            }),
          async (error) => {
            assert.ok(error instanceof CloudAbortError);
          }
        )
      };
      break;
    }

    case "wait-retry-after": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      await handle.waitForConnection("github", {
        connectionId: "conn_retry",
        pollIntervalMs: 5,
        timeoutMs: 2_000
      });
      result = { ready: true };
      break;
    }

    case "wait-5xx": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      await handle.waitForConnection("github", {
        connectionId: "conn_retry_5xx",
        pollIntervalMs: 5,
        timeoutMs: 1_000
      });
      result = { ready: true };
      break;
    }

    case "wait-401": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      result = {
        error: await expectError(
          () =>
            handle.waitForConnection("github", {
              connectionId: "conn_unauthorized",
              pollIntervalMs: 5,
              timeoutMs: 200
            }),
          async (error) => {
            assert.ok(error instanceof CloudApiError);
            assert.equal(error.httpStatus, 401);
          }
        )
      };
      break;
    }

    case "is-connected-and-disconnect": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      const first = await handle.isConnected("github", "conn_state");
      const second = await handle.isConnected("github", "conn_state");
      await handle.disconnectIntegration("github", "conn_state");
      result = { first, second };
      break;
    }

    case "malformed-create": {
      const setup = new RelayfileSetup(setupOptions());
      result = {
        error: await expectError(
          () => setup.createWorkspace(),
          async (error) => {
            assert.ok(error instanceof MalformedCloudResponseError);
            assert.equal(error.field, "workspaceId");
          }
        )
      };
      break;
    }

    case "malformed-join": {
      const setup = new RelayfileSetup(setupOptions());
      result = {
        error: await expectError(
          () => setup.joinWorkspace(workspaceId),
          async (error) => {
            assert.ok(error instanceof MalformedCloudResponseError);
            assert.equal(error.field, "token");
          }
        )
      };
      break;
    }

    case "malformed-status": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      result = {
        error: await expectError(
          () => handle.isConnected("github", "conn_bad_status"),
          async (error) => {
            assert.ok(error instanceof MalformedCloudResponseError);
            assert.equal(error.field, "ready");
          }
        )
      };
      break;
    }

    case "cloud-api-error": {
      const setup = new RelayfileSetup(setupOptions());
      const handle = await setup.joinWorkspace(workspaceId);
      result = {
        error: await expectError(
          () => handle.connectIntegration("github"),
          async (error) => {
            assert.ok(error instanceof CloudApiError);
            assert.equal(error.httpStatus, 418);
            assert.deepEqual(error.httpBody, {
              message: "teapot",
              detail: "parsed"
            });
          }
        )
      };
      break;
    }

    case "refresh-token": {
      const realDateNow = Date.now;
      let now = realDateNow();
      Date.now = () => now;
      try {
        const setup = new RelayfileSetup(setupOptions());
        const handle = await setup.joinWorkspace(workspaceId, {
          agentName: "refresh-agent",
          scopes: ["fs:read"],
          permissions: {
            readonly: ["/readonly/**"],
            ignored: ["/ignored/**"]
          }
        });
        const initialToken = handle.getToken();
        now += 55 * 60 * 1000 + 1;
        const tree = await handle.client().listTree(handle.workspaceId, {
          path: "/refresh"
        });
        const refreshedToken = handle.getToken();
        assert.notEqual(refreshedToken, initialToken);
        result = {
          initialToken,
          refreshedToken,
          path: tree.path
        };
      } finally {
        Date.now = realDateNow;
      }
      break;
    }

    default:
      throw new Error("Unknown scenario: " + scenario);
  }

  await writeFile(resultFile, JSON.stringify({ ok: true, result }, null, 2));
}

main().catch(async (error) => {
  const payload = {
    ok: false,
    error: serializeError(error)
  };
  await writeFile(resultFile, JSON.stringify(payload, null, 2));
  console.error(error);
  process.exitCode = 1;
});
`;

  const consumerScriptPath = path.join(consumerDir, "consumer.mjs");
  await writeFile(consumerScriptPath, consumerScript);
  return consumerScriptPath;
}

async function runScenario(consumerDir, consumerScriptPath, cloudApiUrl, scenario) {
  const resultFile = path.join(consumerDir, `${scenario}.json`);
  await runCommand(process.execPath, [consumerScriptPath], {
    cwd: consumerDir,
    env: {
      ...process.env,
      SDK_E2E_SCENARIO: scenario,
      SDK_E2E_CLOUD_URL: cloudApiUrl,
      SDK_E2E_RESULT_FILE: resultFile,
      SDK_E2E_WORKSPACE_ID: "ws_e2e"
    }
  });

  const payload = JSON.parse(await readFile(resultFile, "utf8"));
  assert.equal(payload.ok, true, `${scenario} consumer run failed: ${JSON.stringify(payload.error)}`);
  return payload.result;
}

function expectRequestCount(source, count) {
  const requests = activityLog.filter((entry) => entry.source === source);
  assert.equal(requests.length, count, `Expected ${count} ${source} requests, received ${requests.length}`);
  return requests;
}

async function main() {
  const relayServer = createRelayfileServer();
  const relayBaseUrlRef = { current: "" };
  const cloudServer = createCloudServer(() => relayBaseUrlRef.current);

  try {
    await runCommand(
      npmCommand,
      ["run", "build", "--workspace=packages/sdk/typescript"],
      { cwd: repoRoot, env: process.env }
    );
    await runCommand(
      npmCommand,
      ["run", "build", "--workspace=packages/core"],
      { cwd: repoRoot, env: process.env }
    );

    relayBaseUrlRef.current = await listen(relayServer.server);
    const cloudBaseUrl = await listen(cloudServer.server);

    const packedDir = path.join(tempRoot, "packed");
    const consumerDir = path.join(tempRoot, "consumer");
    await mkdir(packedDir, { recursive: true });
    await mkdir(consumerDir, { recursive: true });

    const [coreTarball, sdkTarball] = await Promise.all([
      packWorkspace("packages/core", packedDir),
      packWorkspace("packages/sdk/typescript", packedDir)
    ]);

    await writeFile(
      path.join(consumerDir, "package.json"),
      JSON.stringify(
        {
          name: "sdk-setup-e2e-consumer",
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

    cloudServer.reset();
    relayServer.reset();
    const createWorkspaceResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "create-workspace"
    );
    const createRequests = expectRequestCount("cloud", 2);
    assert.deepEqual(
      createRequests.map((request) => `${request.method} ${request.pathname}`),
      ["POST /api/v1/workspaces", "POST /api/v1/workspaces/ws_e2e/join"]
    );
    assert.equal(createRequests[0].headers.authorization, "Bearer cld_at_test");
    assert.equal(createRequests[1].headers.authorization, "Bearer cld_at_test");
    assert.equal(createRequests[0].headers["x-relayfile-sdk-version"], "0.5.3");
    assert.equal(createRequests[1].headers["x-relayfile-sdk-version"], "0.5.3");
    assert.equal(createWorkspaceResult.workspaceId, "ws_e2e");

    cloudServer.reset();
    relayServer.reset();
    await runScenario(consumerDir, consumerScriptPath, cloudBaseUrl, "join-workspace");
    const joinRequests = expectRequestCount("cloud", 1);
    assert.equal(joinRequests[0].pathname, "/api/v1/workspaces/ws_e2e/join");

    cloudServer.reset();
    relayServer.reset();
    const listTreeResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "list-tree"
    );
    const listTreeCloudRequests = expectRequestCount("cloud", 1);
    const listTreeRelayRequests = expectRequestCount("relay", 1);
    assert.equal(listTreeCloudRequests[0].pathname, "/api/v1/workspaces/ws_e2e/join");
    assert.equal(listTreeRelayRequests[0].pathname, "/v1/workspaces/ws_e2e/fs/tree");
    assert.equal(listTreeRelayRequests[0].searchParams.path, "/github");
    assert.equal(
      listTreeRelayRequests[0].headers.authorization,
      `Bearer ${listTreeResult.token}`
    );

    cloudServer.reset({
      statusQueues: {
        "github:conn_existing": [{ body: { ready: true } }]
      }
    });
    relayServer.reset();
    const alreadyConnectedResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "connect-already"
    );
    const alreadyConnectedRequests = expectRequestCount("cloud", 2);
    assert.deepEqual(
      alreadyConnectedRequests.map((request) => `${request.method} ${request.pathname}`),
      [
        "POST /api/v1/workspaces/ws_e2e/join",
        "GET /api/v1/workspaces/ws_e2e/integrations/github/status"
      ]
    );
    assert.equal(alreadyConnectedResult.alreadyConnected, true);

    cloudServer.reset({
      connectQueue: [
        {
          body: {
            token: "session_token",
            expiresAt: "2026-04-30T01:00:00.000Z",
            connectLink: "https://connect.test/github",
            connectionId: "conn_oauth"
          }
        }
      ]
    });
    relayServer.reset();
    const connectOauthResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "connect-oauth"
    );
    const connectOauthRequests = expectRequestCount("cloud", 2);
    assert.equal(
      connectOauthRequests[1].pathname,
      "/api/v1/workspaces/ws_e2e/integrations/connect-session"
    );
    assert.deepEqual(connectOauthRequests[1].body, {
      allowedIntegrations: ["github"]
    });
    assert.deepEqual(connectOauthResult, {
      alreadyConnected: false,
      connectLink: "https://connect.test/github",
      sessionToken: "session_token",
      expiresAt: "2026-04-30T01:00:00.000Z",
      connectionId: "conn_oauth"
    });

    cloudServer.reset({
      connectQueue: [
        {
          body: {
            token: "session_token",
            expiresAt: "2026-04-30T01:00:00.000Z",
            connectLink: "https://connect.test/notion",
            connectionId: "conn_notion"
          }
        }
      ]
    });
    relayServer.reset();
    const connectNotionResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "connect-notion"
    );
    const connectNotionRequests = expectRequestCount("cloud", 2);
    assert.deepEqual(connectNotionRequests[1].body, {
      allowedIntegrations: ["notion"]
    });
    assert.deepEqual(connectNotionResult, {
      alreadyConnected: false,
      connectLink: "https://connect.test/notion",
      sessionToken: "session_token",
      expiresAt: "2026-04-30T01:00:00.000Z",
      connectionId: "conn_notion"
    });

    cloudServer.reset();
    relayServer.reset();
    const mountEnvAndInviteResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "mount-env-and-invite"
    );
    expectRequestCount("cloud", 1);
    assert.deepEqual(mountEnvAndInviteResult.mountEnv, {
      RELAYFILE_BASE_URL: relayBaseUrlRef.current,
      RELAYFILE_TOKEN: "rf_jwt_join_1",
      RELAYFILE_WORKSPACE: "ws_e2e",
      RELAYFILE_REMOTE_PATH: "/notion",
      RELAYFILE_LOCAL_DIR: "/workspace/notion",
      RELAYCAST_API_KEY: "rc_test",
      RELAY_API_KEY: "rc_test",
      RELAYCAST_BASE_URL: "https://api.relaycast.dev",
      RELAY_BASE_URL: "https://api.relaycast.dev"
    });
    assert.deepEqual(mountEnvAndInviteResult.invite, {
      workspaceId: "ws_e2e",
      cloudApiUrl: cloudBaseUrl,
      relayfileUrl: relayBaseUrlRef.current,
      relaycastApiKey: "rc_test",
      relaycastBaseUrl: "https://api.relaycast.dev",
      agentName: "review-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"],
      relayfileToken: "rf_jwt_join_1"
    });

    cloudServer.reset({
      connectQueue: [
        {
          body: {
            token: "session_token",
            expiresAt: "2026-04-30T01:00:00.000Z",
            connectLink: "https://connect.test/github",
            connectionId: "conn_delayed"
          }
        }
      ],
      statusQueues: {
        "github:conn_delayed": [
          { body: { ready: false } },
          { body: { ready: false } },
          { body: { ready: true } }
        ]
      }
    });
    relayServer.reset();
    const waitDelayedResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "wait-delayed"
    );
    const waitDelayedRequests = expectRequestCount("cloud", 5);
    assert.equal(waitDelayedResult.polls.length, 3);
    assert.equal(waitDelayedResult.polls[0], 0);
    assert.ok(waitDelayedResult.polls[1] >= 0);
    assert.ok(waitDelayedResult.polls[2] >= waitDelayedResult.polls[1]);
    assert.deepEqual(
      waitDelayedRequests
        .filter((request) => request.method === "GET")
        .map((request) => request.searchParams.connectionId),
      ["conn_delayed", "conn_delayed", "conn_delayed"]
    );

    cloudServer.reset({
      statusQueues: {
        "github:conn_timeout": [
          { body: { ready: false } },
          { body: { ready: false } },
          { body: { ready: false } },
          { body: { ready: false } }
        ]
      }
    });
    relayServer.reset();
    const timeoutResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "wait-timeout"
    );
    assert.equal(timeoutResult.error.name, "IntegrationConnectionTimeoutError");

    cloudServer.reset({
      statusQueues: {
        "github:conn_abort": [
          { body: { ready: false } },
          { body: { ready: false } }
        ]
      }
    });
    relayServer.reset();
    const abortResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "wait-abort"
    );
    assert.equal(abortResult.error.name, "CloudAbortError");

    cloudServer.reset({
      statusQueues: {
        "github:conn_retry": [
          {
            status: 429,
            headers: { "retry-after": "1" },
            body: { message: "slow down" }
          },
          { body: { ready: true } }
        ]
      }
    });
    relayServer.reset();
    await runScenario(consumerDir, consumerScriptPath, cloudBaseUrl, "wait-retry-after");
    const retryAfterRequests = activityLog.filter(
      (entry) =>
        entry.source === "cloud" &&
        entry.pathname === "/api/v1/workspaces/ws_e2e/integrations/github/status"
    );
    assert.equal(retryAfterRequests.length, 2);
    assert.ok(
      retryAfterRequests[1].receivedAt - retryAfterRequests[0].receivedAt >= 900,
      "Retry-After delay was not honored"
    );

    cloudServer.reset({
      statusQueues: {
        "github:conn_retry_5xx": [
          { status: 503, body: { message: "retry 1" } },
          { status: 502, body: { message: "retry 2" } },
          { body: { ready: true } }
        ]
      }
    });
    relayServer.reset();
    await runScenario(consumerDir, consumerScriptPath, cloudBaseUrl, "wait-5xx");
    const retry5xxRequests = activityLog.filter(
      (entry) =>
        entry.source === "cloud" &&
        entry.pathname === "/api/v1/workspaces/ws_e2e/integrations/github/status"
    );
    assert.equal(retry5xxRequests.length, 3);

    cloudServer.reset({
      statusQueues: {
        "github:conn_unauthorized": [
          {
            status: 401,
            body: { message: "unauthorized" }
          }
        ]
      }
    });
    relayServer.reset();
    const unauthorizedResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "wait-401"
    );
    const unauthorizedRequests = activityLog.filter(
      (entry) =>
        entry.source === "cloud" &&
        entry.pathname === "/api/v1/workspaces/ws_e2e/integrations/github/status"
    );
    assert.equal(unauthorizedRequests.length, 1);
    assert.equal(unauthorizedResult.error.httpStatus, 401);

    cloudServer.reset({
      statusQueues: {
        "github:conn_state": [
          { body: { ready: false } },
          { body: { ready: true } }
        ]
      }
    });
    relayServer.reset();
    const stateResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "is-connected-and-disconnect"
    );
    const stateRequests = expectRequestCount("cloud", 4);
    assert.equal(stateResult.first, false);
    assert.equal(stateResult.second, true);
    assert.equal(stateRequests[3].method, "DELETE");
    assert.equal(
      stateRequests[3].pathname,
      "/api/v1/workspaces/ws_e2e/integrations/github/status"
    );

    cloudServer.reset({
      createQueue: [
        {
          body: {
            relayfileUrl: relayBaseUrlRef.current,
            relaycastApiKey: "rc_test",
            createdAt: "2026-04-30T00:00:00.000Z"
          }
        }
      ]
    });
    relayServer.reset();
    const malformedCreateResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "malformed-create"
    );
    assert.equal(malformedCreateResult.error.name, "MalformedCloudResponseError");
    assert.equal(malformedCreateResult.error.field, "workspaceId");

    cloudServer.reset({
      joinQueue: [
        {
          body: {
            workspaceId: "ws_e2e",
            relayfileUrl: relayBaseUrlRef.current,
            wsUrl: "wss://relayfile.test/ws",
            relaycastApiKey: "rc_test"
          }
        }
      ]
    });
    relayServer.reset();
    const malformedJoinResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "malformed-join"
    );
    assert.equal(malformedJoinResult.error.name, "MalformedCloudResponseError");
    assert.equal(malformedJoinResult.error.field, "token");

    cloudServer.reset({
      statusQueues: {
        "github:conn_bad_status": [
          {
            body: {
              ready: "later"
            }
          }
        ]
      }
    });
    relayServer.reset();
    const malformedStatusResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "malformed-status"
    );
    assert.equal(malformedStatusResult.error.name, "MalformedCloudResponseError");
    assert.equal(malformedStatusResult.error.field, "ready");

    cloudServer.reset({
      connectQueue: [
        {
          status: 418,
          body: {
            message: "teapot",
            detail: "parsed"
          }
        }
      ]
    });
    relayServer.reset();
    const cloudApiErrorResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "cloud-api-error"
    );
    assert.equal(cloudApiErrorResult.error.name, "CloudApiError");
    assert.equal(cloudApiErrorResult.error.httpStatus, 418);
    assert.deepEqual(cloudApiErrorResult.error.httpBody, {
      message: "teapot",
      detail: "parsed"
    });

    cloudServer.reset({
      joinQueue: [
        {
          body: {
            workspaceId: "ws_e2e",
            token: "rf_jwt_initial",
            relayfileUrl: relayBaseUrlRef.current,
            wsUrl: "wss://relayfile.test/ws",
            relaycastApiKey: "rc_test"
          }
        },
        {
          body: {
            workspaceId: "ws_e2e",
            token: "rf_jwt_refreshed",
            relayfileUrl: relayBaseUrlRef.current,
            wsUrl: "wss://relayfile.test/ws",
            relaycastApiKey: "rc_test"
          }
        }
      ]
    });
    relayServer.reset();
    const refreshResult = await runScenario(
      consumerDir,
      consumerScriptPath,
      cloudBaseUrl,
      "refresh-token"
    );
    const refreshCloudRequests = activityLog.filter(
      (entry) => entry.source === "cloud"
    );
    const refreshRelayRequests = activityLog.filter(
      (entry) => entry.source === "relay"
    );
    assert.equal(refreshCloudRequests.length, 2);
    assert.equal(refreshRelayRequests.length, 1);
    assert.deepEqual(refreshCloudRequests[0].body, refreshCloudRequests[1].body);
    assert.ok(
      refreshCloudRequests[1].receivedAt <= refreshRelayRequests[0].receivedAt,
      "refresh join must happen before the relayfile request"
    );
    assert.equal(
      refreshRelayRequests[0].headers.authorization,
      `Bearer ${refreshResult.refreshedToken}`
    );
    assert.equal(refreshResult.initialToken, "rf_jwt_initial");
    assert.equal(refreshResult.refreshedToken, "rf_jwt_refreshed");

    console.log("SDK_SETUP_E2E_OK");
    console.log("SDK_E2E_PROOF_READY");
  } finally {
    await Promise.allSettled([
      closeServer(cloudServer.server),
      closeServer(relayServer.server),
      rm(tempRoot, { recursive: true, force: true })
    ]);
  }
}

await main();
