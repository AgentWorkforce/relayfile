import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import {
  access,
  mkdtemp,
  mkdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import {
  createMockCloudServer,
  createMockRelayfileServer,
  createMockRelaycastServer,
  listenServer,
} from "./agent-workspace-mocks.mjs";

const scriptPath = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(scriptPath);
const packageDir = path.resolve(scriptsDir, "..");
const repoRoot = path.resolve(packageDir, "../../..");
const npmCommand = process.platform === "win32" ? "npm.cmd" : "npm";
const nodeCommand = process.execPath;
const seededNotionPath = "/notion/research/brief.md";
const seededNotionContent = "# Mock Notion brief\n";

function commandError(command, args, error, stdout, stderr) {
  const details = [
    `Command failed: ${command} ${args.join(" ")}`,
    stdout ? `stdout:\n${stdout}` : "",
    stderr ? `stderr:\n${stderr}` : "",
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
        maxBuffer: 10 * 1024 * 1024,
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

function startProcess(label, command, args, options = {}) {
  const child = spawn(command, args, {
    cwd: options.cwd,
    env: options.env,
    stdio: ["ignore", "pipe", "pipe"],
  });

  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => {
    stdout += String(chunk);
  });
  child.stderr.on("data", (chunk) => {
    stderr += String(chunk);
  });

  const exitPromise = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      resolve({ code, signal });
    });
  });

  return {
    label,
    child,
    getStdout() {
      return stdout;
    },
    getStderr() {
      return stderr;
    },
    async wait() {
      const { code, signal } = await exitPromise;
      if (code !== 0) {
        throw commandError(
          command,
          args,
          new Error(`${label} exited with code ${code} signal ${signal ?? "none"}`),
          stdout,
          stderr
        );
      }
      return { stdout, stderr };
    },
    async stop() {
      if (child.exitCode !== null || child.signalCode !== null) {
        await exitPromise;
        return;
      }
      child.kill("SIGTERM");
      const result = await Promise.race([
        exitPromise,
        sleep(1_000).then(() => "timeout"),
      ]);
      if (result === "timeout") {
        child.kill("SIGKILL");
        await exitPromise;
      }
    },
  };
}

function sleep(ms) {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function pathExists(targetPath) {
  try {
    await access(targetPath);
    return true;
  } catch {
    return false;
  }
}

async function waitForFile(targetPath, timeoutMs, description) {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (await pathExists(targetPath)) {
      return targetPath;
    }
    await sleep(25);
  }
  throw new Error(`Timed out waiting for ${description} at ${targetPath}`);
}

async function waitForJsonFile(targetPath, timeoutMs, description) {
  await waitForFile(targetPath, timeoutMs, description);
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    try {
      return JSON.parse(await readFile(targetPath, "utf8"));
    } catch {
      await sleep(25);
    }
  }
  throw new Error(`Timed out waiting for readable JSON in ${targetPath}`);
}

async function waitForJsonFileOrProcessExit(
  targetPath,
  timeoutMs,
  description,
  processHandle
) {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (await pathExists(targetPath)) {
      try {
        return JSON.parse(await readFile(targetPath, "utf8"));
      } catch {
        // Keep polling until the writer finishes.
      }
    }

    if (processHandle.child.exitCode !== null || processHandle.child.signalCode !== null) {
      if (processHandle.child.exitCode === 0) {
        await sleep(50);
        if (await pathExists(targetPath)) {
          return JSON.parse(await readFile(targetPath, "utf8"));
        }
        throw new Error(
          `${processHandle.label} exited before producing ${description} at ${targetPath}`
        );
      }
      await processHandle.wait();
    }

    await sleep(25);
  }

  if (processHandle.child.exitCode !== null || processHandle.child.signalCode !== null) {
    await processHandle.wait();
  }
  throw new Error(`Timed out waiting for ${description}`);
}

async function waitForCondition(predicate, description, timeoutMs = 5_000) {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (predicate()) {
      return;
    }
    await sleep(25);
  }
  throw new Error(`Timed out waiting for ${description}`);
}

async function postJson(url, body) {
  const response = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
  const payload = await response.json();
  assert.equal(
    response.ok,
    true,
    `Expected POST ${url} to succeed, received ${response.status}: ${JSON.stringify(payload)}`
  );
  return payload;
}

async function packWorkspace(relativeWorkspacePath, destinationDir) {
  const { stdout } = await runCommand(
    npmCommand,
    [
      "pack",
      `./${relativeWorkspacePath.replace(/^\.?\//, "")}`,
      "--pack-destination",
      destinationDir,
    ],
    { cwd: repoRoot, env: process.env }
  );
  const tarballName = stdout
    .trim()
    .split(/\r?\n/)
    .filter(Boolean)
    .at(-1);
  assert.ok(
    tarballName,
    `npm pack did not print a tarball name for ${relativeWorkspacePath}`
  );
  return path.join(destinationDir, tarballName);
}

async function writeConsumerScripts(consumerDir) {
  const relaycastClient = String.raw`
const DEFAULT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 25;

function authHeaders(apiKey) {
  return {
    authorization: "Bearer " + apiKey,
    "content-type": "application/json"
  };
}

async function readJson(response) {
  const text = await response.text();
  return text === "" ? {} : JSON.parse(text);
}

export async function registerAgent({ baseUrl, apiKey, workspaceId, agentName, scopes }) {
  const response = await fetch(
    baseUrl + "/v1/workspaces/" + encodeURIComponent(workspaceId) + "/agents/register",
    {
    method: "POST",
    headers: authHeaders(apiKey),
    body: JSON.stringify({ agentName, scopes })
    }
  );
  const payload = await readJson(response);
  if (!response.ok) {
    throw new Error(
      "registerAgent failed: " + response.status + " " + JSON.stringify(payload)
    );
  }
  return payload;
}

export async function sendMessage({ baseUrl, apiKey, workspaceId, message }) {
  const response = await fetch(
    baseUrl + "/v1/workspaces/" + encodeURIComponent(workspaceId) + "/messages",
    {
    method: "POST",
    headers: authHeaders(apiKey),
    body: JSON.stringify(message)
    }
  );
  const payload = await readJson(response);
  if (!response.ok) {
    throw new Error(
      "sendMessage failed: " + response.status + " " + JSON.stringify(payload)
    );
  }
  return payload.message;
}

export async function waitForMessage({
  baseUrl,
  apiKey,
  workspaceId,
  agentName,
  predicate,
  timeoutMs = DEFAULT_TIMEOUT_MS
}) {
  const startedAt = Date.now();
  let afterId = 0;
  while (Date.now() - startedAt < timeoutMs) {
    const response = await fetch(
      baseUrl +
        "/v1/workspaces/" +
        encodeURIComponent(workspaceId) +
        "/messages?agentName=" +
        encodeURIComponent(agentName) +
        "&afterId=" +
        afterId,
      {
        headers: {
          authorization: "Bearer " + apiKey
        }
      }
    );
    const payload = await readJson(response);
    if (!response.ok) {
      throw new Error(
        "waitForMessage failed: " + response.status + " " + JSON.stringify(payload)
      );
    }
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    for (const message of messages) {
      const numericId = Number(String(message.id ?? "0").replace(/^msg_/, ""));
      if (numericId > afterId) {
        afterId = numericId;
      }
      if (predicate(message)) {
        return message;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
  }
  throw new Error("Timed out waiting for relaycast message for " + agentName);
}
`;

  const leadAgentScript = String.raw`
import { writeFile } from "node:fs/promises";
import { RelayfileSetup } from "@relayfile/sdk";
import { registerAgent, sendMessage, waitForMessage } from "./relaycast-client.mjs";

const cloudApiUrl = process.env.SDK_E2E_CLOUD_URL;
const leadSetupFile = process.env.LEAD_SETUP_FILE;
const leadReadyFile = process.env.LEAD_READY_FILE;
const leadResultFile = process.env.LEAD_RESULT_FILE;
const leadLocalDir = process.env.LEAD_LOCAL_DIR;

async function main() {
  const setup = new RelayfileSetup({
    cloudApiUrl,
    requestTimeoutMs: 1_000,
    retry: { maxRetries: 0, baseDelayMs: 25 }
  });

  const workspace = await setup.createWorkspace({
    name: "golden-path-consumer",
    agentName: "lead-agent",
    scopes: ["fs:read", "fs:write", "relaycast:read", "relaycast:write"]
  });

  const notion = await workspace.connectNotion();
  const leadMountEnv = workspace.mountEnv({
    localDir: leadLocalDir,
    remotePath: "/notion",
    mode: "poll"
  });
  const invite = workspace.agentInvite({
    agentName: "review-agent",
    scopes: ["fs:read", "relaycast:read", "relaycast:write"]
  });

  await writeFile(
    leadSetupFile,
    JSON.stringify(
      {
        workspaceId: workspace.workspaceId,
        connectionId: notion.connectionId,
        connectLink: notion.connectLink,
        leadMountEnv,
        invite
      },
      null,
      2
    ),
    "utf8"
  );

  await workspace.waitForNotion({
    connectionId: notion.connectionId,
    pollIntervalMs: 25,
    timeoutMs: 5_000
  });

  await writeFile(
    leadReadyFile,
    JSON.stringify(
      {
        workspaceId: workspace.workspaceId,
        connectionId: notion.connectionId,
        ready: true
      },
      null,
      2
    ),
    "utf8"
  );

  const relaycastBaseUrl = leadMountEnv.RELAYCAST_BASE_URL;
  const relaycastApiKey = leadMountEnv.RELAYCAST_API_KEY;
  await registerAgent({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: workspace.workspaceId,
    agentName: "lead-agent",
    scopes: ["fs:read", "fs:write", "relaycast:read", "relaycast:write"]
  });

  const leadReadyMessage = await sendMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: workspace.workspaceId,
    message: {
      from: "lead-agent",
      to: "review-agent",
      type: "lead.ready",
      text: "lead ready",
      path: "/notion/research/brief.md"
    }
  });

  const invitedReadyMessage = await waitForMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: workspace.workspaceId,
    agentName: "lead-agent",
    predicate: (message) =>
      message.from === "review-agent" && message.type === "review.ready"
  });

  const ackMessage = await sendMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: workspace.workspaceId,
    message: {
      from: "lead-agent",
      to: "review-agent",
      type: "lead.ack",
      text: "lead ack",
      path: invitedReadyMessage.path
    }
  });

  await writeFile(
    leadResultFile,
    JSON.stringify(
      {
        ok: true,
        workspaceId: workspace.workspaceId,
        connectionId: notion.connectionId,
        connectLink: notion.connectLink,
        leadReadyMessage,
        invitedReadyMessage,
        ackMessage
      },
      null,
      2
    ),
    "utf8"
  );
}

await main();
`;

  const invitedAgentScript = String.raw`
import { readFile, writeFile } from "node:fs/promises";
import { RelayFileClient } from "@relayfile/sdk";
import { registerAgent, sendMessage, waitForMessage } from "./relaycast-client.mjs";

const inviteFile = process.env.INVITE_FILE;
const invitedResultFile = process.env.INVITED_RESULT_FILE;

async function main() {
  const invite = JSON.parse(await readFile(inviteFile, "utf8"));
  const relaycastBaseUrl = invite.relaycastBaseUrl;
  const relaycastApiKey = invite.relaycastApiKey;

  await registerAgent({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: invite.workspaceId,
    agentName: invite.agentName,
    scopes: invite.scopes
  });

  const leadReadyMessage = await waitForMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: invite.workspaceId,
    agentName: invite.agentName,
    predicate: (message) =>
      message.from === "lead-agent" && message.type === "lead.ready"
  });

  const client = new RelayFileClient({
    baseUrl: invite.relayfileUrl,
    token: invite.relayfileToken
  });
  const file = await client.readFile(invite.workspaceId, "/notion/research/brief.md");
  const reviewReadyMessage = await sendMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: invite.workspaceId,
    message: {
      from: invite.agentName,
      to: "lead-agent",
      type: "review.ready",
      text: "review ready",
      path: file.path
    }
  });

  const ackMessage = await waitForMessage({
    baseUrl: relaycastBaseUrl,
    apiKey: relaycastApiKey,
    workspaceId: invite.workspaceId,
    agentName: invite.agentName,
    predicate: (message) =>
      message.from === "lead-agent" && message.type === "lead.ack"
  });

  await writeFile(
    invitedResultFile,
    JSON.stringify(
      {
        ok: true,
        workspaceId: invite.workspaceId,
        leadReadyMessage,
        reviewReadyMessage,
        ackMessage,
        file
      },
      null,
      2
    ),
    "utf8"
  );
}

await main();
`;

  const mountHarnessRunner = String.raw`
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import {
  MountHarnessPermissionError,
  startMountHarness
} from "@relayfile/sdk/dist/mount-harness.js";

async function waitForStopSignal(stopFile) {
  for (;;) {
    try {
      await readFile(stopFile, "utf8");
      return;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
  }
}

async function main() {
  const events = [];
  const scopes = process.env.HARNESS_SCOPES
    ? JSON.parse(process.env.HARNESS_SCOPES)
    : [];
  const resultFile = process.env.HARNESS_RESULT_FILE;
  const stopFile = process.env.HARNESS_STOP_FILE;
  const expectedRelativePath = process.env.HARNESS_EXPECTED_RELATIVE_PATH;
  const harness = await startMountHarness({
    env: process.env,
    pollIntervalMs: 25,
    scopes,
    onEvent: (event) => {
      events.push(event);
    }
  });

  let observedContent = null;
  if (expectedRelativePath) {
    observedContent = await readFile(
      path.join(process.env.RELAYFILE_LOCAL_DIR, expectedRelativePath),
      "utf8"
    );
  }

  let readOnlyError = null;
  const writeRelativePath = process.env.HARNESS_TEST_READ_ONLY_RELATIVE_PATH;
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
        localDir: process.env.RELAYFILE_LOCAL_DIR,
        observedContent,
        readOnlyError,
        events
      },
      null,
      2
    ),
    "utf8"
  );

  await waitForStopSignal(stopFile);
  await harness.stop();
}

await main();
`;

  await writeFile(path.join(consumerDir, "relaycast-client.mjs"), relaycastClient, "utf8");
  await writeFile(path.join(consumerDir, "lead-agent.mjs"), leadAgentScript, "utf8");
  await writeFile(path.join(consumerDir, "invited-agent.mjs"), invitedAgentScript, "utf8");
  await writeFile(
    path.join(consumerDir, "mount-harness-runner.mjs"),
    mountHarnessRunner,
    "utf8"
  );
}

function findRequest(requests, predicate, description) {
  const request = requests.find(predicate);
  assert.ok(request, `Missing request for ${description}`);
  return request;
}

async function main() {
  const tempRoot = await mkdtemp(
    path.join(tmpdir(), "relayfile-agent-workspace-golden-path-e2e-")
  );
  const packedDir = path.join(tempRoot, "packed");
  const consumerDir = path.join(tempRoot, "consumer");
  const leadLocalDir = path.join(tempRoot, "lead-notion");
  const invitedLocalDir = path.join(tempRoot, "invited-notion");
  const leadSetupFile = path.join(tempRoot, "lead-setup.json");
  const leadReadyFile = path.join(tempRoot, "lead-ready.json");
  const leadResultFile = path.join(tempRoot, "lead-result.json");
  const inviteFile = path.join(tempRoot, "invite.json");
  const invitedResultFile = path.join(tempRoot, "invited-result.json");
  const leadHarnessResultFile = path.join(tempRoot, "lead-harness-result.json");
  const leadHarnessStopFile = path.join(tempRoot, "lead-harness.stop");
  const invitedHarnessResultFile = path.join(tempRoot, "invited-harness-result.json");
  const invitedHarnessStopFile = path.join(tempRoot, "invited-harness.stop");
  const relayBaseUrlRef = { current: "" };
  const relaycastBaseUrlRef = { current: "" };
  const spawnedProcesses = [];

  const relay = createMockRelayfileServer();
  const relaycast = createMockRelaycastServer();
  const cloud = createMockCloudServer({
    relayfileBaseUrl: () => relayBaseUrlRef.current,
    relaycastBaseUrl: () => relaycastBaseUrlRef.current,
  });

  try {
    await mkdir(packedDir, { recursive: true });
    await mkdir(consumerDir, { recursive: true });

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

    relayBaseUrlRef.current = await listenServer(relay.server);
    relaycastBaseUrlRef.current = await listenServer(relaycast.server);
    const cloudBaseUrl = await listenServer(cloud.server);
    relay.seedFile(seededNotionPath, seededNotionContent);

    const [coreTarball, sdkTarball] = await Promise.all([
      packWorkspace("packages/core", packedDir),
      packWorkspace("packages/sdk/typescript", packedDir),
    ]);

    await writeFile(
      path.join(consumerDir, "package.json"),
      JSON.stringify(
        {
          name: "agent-workspace-consumer",
          private: true,
          type: "module",
        },
        null,
        2
      ),
      "utf8"
    );

    await runCommand(npmCommand, ["install", coreTarball, sdkTarball], {
      cwd: consumerDir,
      env: process.env,
    });

    await writeConsumerScripts(consumerDir);

    const leadProcess = startProcess(
      "lead-agent",
      nodeCommand,
      ["lead-agent.mjs"],
      {
        cwd: consumerDir,
        env: {
          ...process.env,
          SDK_E2E_CLOUD_URL: cloudBaseUrl,
          LEAD_SETUP_FILE: leadSetupFile,
          LEAD_READY_FILE: leadReadyFile,
          LEAD_RESULT_FILE: leadResultFile,
          LEAD_LOCAL_DIR: leadLocalDir,
        },
      }
    );
    spawnedProcesses.push(leadProcess);

    const leadSetup = await waitForJsonFileOrProcessExit(
      leadSetupFile,
      10_000,
      "lead agent setup payload",
      leadProcess
    );

    assert.equal(leadSetup.workspaceId, "ws_agent_workspace_demo");
    assert.equal(leadSetup.connectionId, "conn_notion");
    assert.equal(leadSetup.connectLink, "https://connect.mock.local/notion");
    assert.equal(leadSetup.leadMountEnv.RELAYFILE_BASE_URL, relayBaseUrlRef.current);
    assert.equal(leadSetup.leadMountEnv.RELAYFILE_WORKSPACE, "ws_agent_workspace_demo");
    assert.equal(leadSetup.leadMountEnv.RELAYFILE_REMOTE_PATH, "/notion");
    assert.equal(leadSetup.leadMountEnv.RELAYFILE_LOCAL_DIR, leadLocalDir);
    assert.equal(leadSetup.leadMountEnv.RELAYFILE_MOUNT_MODE, "poll");
    assert.equal(leadSetup.leadMountEnv.RELAYCAST_API_KEY, "rc_mock_key");
    assert.equal(leadSetup.leadMountEnv.RELAY_API_KEY, "rc_mock_key");
    assert.equal(leadSetup.leadMountEnv.RELAYCAST_BASE_URL, relaycastBaseUrlRef.current);
    assert.equal(leadSetup.leadMountEnv.RELAY_BASE_URL, relaycastBaseUrlRef.current);

    assert.equal(leadSetup.invite.workspaceId, "ws_agent_workspace_demo");
    assert.equal(leadSetup.invite.cloudApiUrl, cloudBaseUrl);
    assert.equal(leadSetup.invite.relayfileUrl, relayBaseUrlRef.current);
    assert.equal(leadSetup.invite.relaycastApiKey, "rc_mock_key");
    assert.equal(leadSetup.invite.relaycastBaseUrl, relaycastBaseUrlRef.current);
    assert.equal(leadSetup.invite.agentName, "review-agent");
    assert.deepEqual(leadSetup.invite.scopes, [
      "fs:read",
      "relaycast:read",
      "relaycast:write",
    ]);
    assert.equal(leadSetup.invite.relayfileToken, "rf_token_1_rw");

    const createRequest = findRequest(
      cloud.state.requests,
      (request) =>
        request.method === "POST" && request.pathname === "/api/v1/workspaces",
      "workspace creation"
    );
    assert.equal(createRequest.headers["x-relayfile-sdk-version"], "0.5.3");
    assert.deepEqual(createRequest.body, {
      name: "golden-path-consumer",
    });

    const joinRequest = findRequest(
      cloud.state.requests,
      (request) =>
        request.method === "POST" &&
        request.pathname === "/api/v1/workspaces/ws_agent_workspace_demo/join",
      "workspace join"
    );
    assert.equal(joinRequest.headers["x-relayfile-sdk-version"], "0.5.3");
    assert.deepEqual(joinRequest.body, {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:read", "relaycast:write"],
    });

    const connectRequest = findRequest(
      cloud.state.requests,
      (request) =>
        request.method === "POST" &&
        request.pathname ===
          "/api/v1/workspaces/ws_agent_workspace_demo/integrations/connect-session",
      "connectNotion"
    );
    assert.equal(connectRequest.headers["x-relayfile-sdk-version"], "0.5.3");
    assert.equal(connectRequest.headers.authorization, "Bearer rf_token_1_rw");
    assert.deepEqual(connectRequest.body, {
      allowedIntegrations: ["notion"],
    });

    await waitForCondition(
      () =>
        cloud.state.requests.some(
          (request) =>
            request.method === "GET" &&
            request.pathname ===
              "/api/v1/workspaces/ws_agent_workspace_demo/integrations/notion/status"
        ),
      "waitForNotion poll request"
    );

    await postJson(`${cloudBaseUrl}/mock/integrations/notion/webhook`, {
      workspaceId: leadSetup.workspaceId,
      connectionId: leadSetup.connectionId,
      state: "ready",
      ready: true,
    });

    const webhookRequest = findRequest(
      cloud.state.requests,
      (request) =>
        request.method === "POST" &&
        request.pathname === "/mock/integrations/notion/webhook",
      "mock notion webhook"
    );
    assert.deepEqual(webhookRequest.body, {
      workspaceId: "ws_agent_workspace_demo",
      connectionId: "conn_notion",
      state: "ready",
      ready: true,
    });

    await waitForJsonFileOrProcessExit(
      leadReadyFile,
      10_000,
      "lead notion readiness",
      leadProcess
    );

    const leadHarnessProcess = startProcess(
      "lead-mount-harness",
      nodeCommand,
      ["mount-harness-runner.mjs"],
      {
        cwd: consumerDir,
        env: {
          ...process.env,
          ...leadSetup.leadMountEnv,
          HARNESS_RESULT_FILE: leadHarnessResultFile,
          HARNESS_STOP_FILE: leadHarnessStopFile,
          HARNESS_EXPECTED_RELATIVE_PATH: "research/brief.md",
          HARNESS_SCOPES: JSON.stringify([
            "fs:read",
            "fs:write",
            "relaycast:read",
            "relaycast:write",
          ]),
        },
      }
    );
    spawnedProcesses.push(leadHarnessProcess);

    await writeFile(inviteFile, JSON.stringify(leadSetup.invite, null, 2), "utf8");

    const invitedHarnessProcess = startProcess(
      "invited-mount-harness",
      nodeCommand,
      ["mount-harness-runner.mjs"],
      {
        cwd: consumerDir,
        env: {
          ...process.env,
          RELAYFILE_BASE_URL: leadSetup.invite.relayfileUrl,
          RELAYFILE_TOKEN: leadSetup.invite.relayfileToken,
          RELAYFILE_WORKSPACE: leadSetup.invite.workspaceId,
          RELAYFILE_REMOTE_PATH: "/notion",
          RELAYFILE_LOCAL_DIR: invitedLocalDir,
          RELAYFILE_MOUNT_MODE: "poll",
          HARNESS_RESULT_FILE: invitedHarnessResultFile,
          HARNESS_STOP_FILE: invitedHarnessStopFile,
          HARNESS_EXPECTED_RELATIVE_PATH: "research/brief.md",
          HARNESS_SCOPES: JSON.stringify(leadSetup.invite.scopes),
          HARNESS_TEST_READ_ONLY_RELATIVE_PATH: "review/notes.md",
          HARNESS_TEST_READ_ONLY_CONTENT: "should fail",
        },
      }
    );
    spawnedProcesses.push(invitedHarnessProcess);

    const invitedAgentProcess = startProcess(
      "invited-agent",
      nodeCommand,
      ["invited-agent.mjs"],
      {
        cwd: consumerDir,
        env: {
          ...process.env,
          INVITE_FILE: inviteFile,
          INVITED_RESULT_FILE: invitedResultFile,
        },
      }
    );
    spawnedProcesses.push(invitedAgentProcess);

    const leadHarnessResult = await waitForJsonFileOrProcessExit(
      leadHarnessResultFile,
      10_000,
      "lead harness sync result",
      leadHarnessProcess
    );
    const invitedHarnessResult = await waitForJsonFileOrProcessExit(
      invitedHarnessResultFile,
      10_000,
      "invited harness sync result",
      invitedHarnessProcess
    );
    const invitedResult = await waitForJsonFileOrProcessExit(
      invitedResultFile,
      10_000,
      "invited agent result",
      invitedAgentProcess
    );
    const leadResult = await waitForJsonFileOrProcessExit(
      leadResultFile,
      10_000,
      "lead agent result",
      leadProcess
    );

    assert.equal(leadHarnessResult.ok, true);
    assert.equal(leadHarnessResult.observedContent, seededNotionContent);
    assert.equal(
      await readFile(path.join(leadLocalDir, "research/brief.md"), "utf8"),
      seededNotionContent
    );
    assert.ok(
      leadHarnessResult.events.some((event) => event.type === "started"),
      "lead harness should emit a started event"
    );
    assert.ok(
      leadHarnessResult.events.some((event) => event.type === "sync.completed"),
      "lead harness should emit a sync.completed event"
    );

    assert.equal(invitedHarnessResult.ok, true);
    assert.equal(invitedHarnessResult.observedContent, seededNotionContent);
    assert.equal(
      await readFile(path.join(invitedLocalDir, "research/brief.md"), "utf8"),
      seededNotionContent
    );
    assert.deepEqual(invitedHarnessResult.readOnlyError, {
      code: "permission_denied",
      path: "/notion/review/notes.md",
      scopes: ["fs:read", "relaycast:read", "relaycast:write"],
    });
    assert.ok(
      invitedHarnessResult.events.some((event) => event.type === "permission.denied"),
      "invited harness should surface a permission.denied event"
    );

    assert.equal(invitedResult.ok, true);
    assert.equal(invitedResult.workspaceId, "ws_agent_workspace_demo");
    assert.equal(invitedResult.file.path, seededNotionPath);
    assert.equal(invitedResult.file.content, seededNotionContent);
    assert.equal(invitedResult.file.contentType, "text/markdown");
    assert.equal(invitedResult.file.encoding, "utf-8");
    assert.equal(invitedResult.leadReadyMessage.type, "lead.ready");
    assert.equal(invitedResult.reviewReadyMessage.type, "review.ready");
    assert.equal(invitedResult.ackMessage.type, "lead.ack");

    assert.equal(leadResult.ok, true);
    assert.equal(leadResult.workspaceId, "ws_agent_workspace_demo");
    assert.equal(leadResult.connectionId, "conn_notion");
    assert.equal(leadResult.connectLink, "https://connect.mock.local/notion");
    assert.equal(leadResult.leadReadyMessage.type, "lead.ready");
    assert.equal(leadResult.invitedReadyMessage.type, "review.ready");
    assert.equal(leadResult.ackMessage.type, "lead.ack");

    const statusRequests = cloud.state.requests.filter(
      (request) =>
        request.method === "GET" &&
        request.pathname ===
          "/api/v1/workspaces/ws_agent_workspace_demo/integrations/notion/status"
    );
    assert.ok(statusRequests.length >= 2, "waitForNotion should poll until ready");
    for (const request of statusRequests) {
      assert.equal(request.headers["x-relayfile-sdk-version"], "0.5.3");
      assert.equal(request.headers.authorization, "Bearer rf_token_1_rw");
      assert.equal(request.searchParams.connectionId, "conn_notion");
    }

    const relayTreeRequests = relay.state.requests.filter(
      (request) =>
        request.method === "GET" &&
        request.pathname === "/v1/workspaces/ws_agent_workspace_demo/fs/tree"
    );
    assert.ok(relayTreeRequests.length >= 2, "mount harnesses should list /notion");
    assert.ok(
      relayTreeRequests.some((request) => request.searchParams.path === "/notion"),
      "mount harnesses should list the /notion root"
    );
    assert.ok(
      relayTreeRequests.some(
        (request) => request.searchParams.path === "/notion/research"
      ),
      "mount harnesses should recurse into /notion/research"
    );
    for (const request of relayTreeRequests) {
      assert.equal(request.headers.authorization, "Bearer rf_token_1_rw");
      assert.ok(request.searchParams.path.startsWith("/notion"));
    }

    const relayFileRequests = relay.state.requests.filter(
      (request) =>
        request.method === "GET" &&
        request.pathname === "/v1/workspaces/ws_agent_workspace_demo/fs/file" &&
        request.searchParams.path === seededNotionPath
    );
    assert.ok(
      relayFileRequests.length >= 3,
      "lead harness, invited harness, and invited agent should all read the seeded notion file"
    );
    for (const request of relayFileRequests) {
      assert.equal(request.headers.authorization, "Bearer rf_token_1_rw");
    }

    const relaycastRegisterRequests = relaycast.state.requests.filter(
      (request) =>
        request.method === "POST" &&
        request.pathname === "/v1/workspaces/ws_agent_workspace_demo/agents/register"
    );
    assert.equal(relaycastRegisterRequests.length, 2);
    assert.deepEqual(
      relaycastRegisterRequests.map((request) => request.body),
      [
        {
          agentName: "lead-agent",
          scopes: ["fs:read", "fs:write", "relaycast:read", "relaycast:write"],
        },
        {
          agentName: "review-agent",
          scopes: ["fs:read", "relaycast:read", "relaycast:write"],
        },
      ]
    );
    for (const request of relaycastRegisterRequests) {
      assert.equal(request.headers.authorization, "Bearer rc_mock_key");
    }

    const relaycastMessageRequests = relaycast.state.requests.filter(
      (request) =>
        request.method === "POST" &&
        request.pathname === "/v1/workspaces/ws_agent_workspace_demo/messages"
    );
    assert.equal(relaycastMessageRequests.length, 3);
    assert.deepEqual(
      relaycast.state.messages,
      [
        {
          id: "msg_1",
          workspaceId: "ws_agent_workspace_demo",
          from: "lead-agent",
          to: "review-agent",
          type: "lead.ready",
          text: "lead ready",
          path: seededNotionPath,
        },
        {
          id: "msg_2",
          workspaceId: "ws_agent_workspace_demo",
          from: "review-agent",
          to: "lead-agent",
          type: "review.ready",
          text: "review ready",
          path: seededNotionPath,
        },
        {
          id: "msg_3",
          workspaceId: "ws_agent_workspace_demo",
          from: "lead-agent",
          to: "review-agent",
          type: "lead.ack",
          text: "lead ack",
          path: seededNotionPath,
        },
      ]
    );
    for (const request of relaycastMessageRequests) {
      assert.equal(request.headers.authorization, "Bearer rc_mock_key");
    }

    const relaycastPollRequests = relaycast.state.requests.filter(
      (request) =>
        request.method === "GET" &&
        request.pathname === "/v1/workspaces/ws_agent_workspace_demo/messages"
    );
    assert.ok(relaycastPollRequests.length >= 3);
    assert.ok(
      relaycastPollRequests.some(
        (request) => request.searchParams.agentName === "lead-agent"
      )
    );
    assert.ok(
      relaycastPollRequests.some(
        (request) => request.searchParams.agentName === "review-agent"
      )
    );

    await writeFile(leadHarnessStopFile, "stop\n", "utf8");
    await writeFile(invitedHarnessStopFile, "stop\n", "utf8");

    await Promise.all([
      leadHarnessProcess.wait(),
      invitedHarnessProcess.wait(),
      invitedAgentProcess.wait(),
      leadProcess.wait(),
    ]);

    console.log("AGENT_WORKSPACE_E2E_OK");
    console.log("GOLDEN_PATH_E2E_READY");
  } finally {
    await Promise.allSettled(
      spawnedProcesses.map((processHandle) => processHandle.stop())
    );
    await Promise.allSettled([
      cloud.close(),
      relay.close(),
      relaycast.close(),
    ]);
    await rm(tempRoot, { recursive: true, force: true });
  }
}

await main();
